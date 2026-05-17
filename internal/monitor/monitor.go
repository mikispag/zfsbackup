package monitor

import (
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mikispag/zfsbackup/internal/config"
	"github.com/mikispag/zfsbackup/internal/zfs"
)

type metric struct {
	Name          string
	Dimensions    map[string]string
	Value         int64
	EvalTimestamp time.Time
}

func (m *metric) asPrometheus() string {
	if len(m.Dimensions) == 0 {
		return fmt.Sprintf("%s %v", m.Name, m.Value)
	}
	keys := slices.Sorted(maps.Keys(m.Dimensions))
	dim := make([]string, 0, len(keys))
	for _, k := range keys {
		dim = append(dim, fmt.Sprintf("%s=%q", k, m.Dimensions[k]))
	}
	return fmt.Sprintf("%s{%s} %v", m.Name, strings.Join(dim, ","), m.Value)
}

type mon struct {
	cfg     *config.MonitorConfig
	metrics []metric
}

func (m *mon) zpoolMetrics() error {
	// ZpoolList uses 'zpool list -j -p' and returns rows sorted by pool name.
	outp, err := zfs.ZpoolList([]string{"name", "capacity", "health"})
	if err != nil {
		return fmt.Errorf("zpool list failed: %w", err)
	}
	var brokenPools int64
	for _, line := range outp {
		if len(line) != 3 {
			return fmt.Errorf("unexpected zpool output: %v", line)
		}
		name, cap, health := line[0], line[1], line[2]
		if health != "ONLINE" {
			brokenPools++
		}
		// ZFS reports "-" for capacity when a pool is faulted or unavailable.
		// Skip the capacity metric for that pool but continue so the
		// HasBrokenPool counter still reaches the monitoring system.
		if cap == "-" {
			slog.Warn("pool capacity unavailable; skipping PoolUsedSpacePercent for this pool",
				"pool", name, "health", health)
			continue
		}
		pct, err := strconv.ParseInt(cap, 10, 64)
		if err != nil {
			return fmt.Errorf("cannot parse pool capacity %q: %w", cap, err)
		}
		m.metrics = append(m.metrics, metric{
			Name:          "PoolUsedSpacePercent",
			Dimensions:    map[string]string{"pool": name},
			Value:         pct,
			EvalTimestamp: time.Now(),
		})
	}
	m.metrics = append(m.metrics, metric{
		Name:          "HasBrokenPool",
		Dimensions:    map[string]string{},
		Value:         brokenPools,
		EvalTimestamp: time.Now(),
	})
	return nil
}

func (m *mon) lastSnapMetricsOneFS(fs string) error {
	found, err := zfs.ZfsList([]string{"creation"}, "snapshot", fs, "-r", "-d1", "-S", "createtxg")
	if err != nil {
		slog.Error("cannot list snapshots", "fs", fs, "err", err)
		return err
	}
	if len(found) == 0 {
		return nil
	}
	unixtime, err := strconv.ParseInt(found[0][0], 10, 64)
	if err != nil {
		return fmt.Errorf("cannot parse snapshot creation time: %w", err)
	}
	now := time.Now()
	m.metrics = append(m.metrics,
		metric{Name: "LastSnapAge", Dimensions: map[string]string{"fs": fs}, Value: now.Unix() - unixtime, EvalTimestamp: now},
		metric{Name: "LastSnapTimestamp", Dimensions: map[string]string{"fs": fs}, Value: unixtime, EvalTimestamp: now},
	)
	return nil
}

func (m *mon) lastSnapMetrics() error {
	var errs []string
	for _, fs := range zfs.ExpandFsToProcess(m.cfg.Include, m.cfg.Exclude) {
		if err := m.lastSnapMetricsOneFS(fs); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", fs, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("snapshot metric errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (m *mon) asPrometheus() string {
	var b strings.Builder
	seen := make(map[string]bool)
	for _, met := range m.metrics {
		if !seen[met.Name] {
			fmt.Fprintf(&b, "# HELP %s zfsbackup metric\n", met.Name)
			fmt.Fprintf(&b, "# TYPE %s untyped\n", met.Name)
			seen[met.Name] = true
		}
		fmt.Fprintln(&b, met.asPrometheus())
	}
	return b.String()
}

func (m *mon) run() error {
	var errs []string
	if err := m.zpoolMetrics(); err != nil {
		slog.Error("zpool metrics failed; snapshot metrics will still be emitted", "err", err)
		errs = append(errs, err.Error())
	}
	if err := m.lastSnapMetrics(); err != nil {
		errs = append(errs, err.Error())
	}
	output := m.asPrometheus()
	if path := m.cfg.PrometheusOutput; path != "" {
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(output), 0o644); err != nil {
			return fmt.Errorf("cannot write Prometheus output file %s: %w", tmp, err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return fmt.Errorf("cannot rename Prometheus output file %s -> %s: %w", tmp, path, err)
		}
	}
	fmt.Print(output)
	if len(errs) > 0 {
		return fmt.Errorf("monitor completed with errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Run runs the monitor module for all filesystems in cfg.
func Run(cfg *config.Config) error {
	if cfg.Monitor == nil {
		return fmt.Errorf("monitor: no monitor section in config")
	}
	mc := cfg.Monitor
	include := cfg.ResolveInclude(mc.Include)
	exclude := cfg.ResolveExclude(mc.Exclude)
	// Create an effective MonitorConfig with resolved include/exclude for
	// the internal mon type that calls ExpandFsToProcess directly.
	effective := &config.MonitorConfig{
		Include:          include,
		Exclude:          exclude,
		PrometheusOutput: mc.PrometheusOutput,
	}
	m := &mon{cfg: effective}
	return m.run()
}

func Main() {
	monitorFlags := flag.NewFlagSet("monitor", flag.ExitOnError)
	configFile := monitorFlags.String("config", "", "path to config file")
	debug := monitorFlags.Bool("debug", false, "enable debug logging")
	monitorFlags.Parse(os.Args[2:])
	zfs.SetupLogger(*debug)

	cfg := &config.Config{}
	zfs.LoadConfig(*configFile, cfg)
	if cfg.Monitor == nil {
		zfs.Fatal("no monitor section in config")
	}

	if err := Run(cfg); err != nil {
		zfs.Fatal("monitor run failed", "err", err)
	}
}
