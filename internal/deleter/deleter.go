package deleter

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mikispag/zfsbackup/internal/config"
	"github.com/mikispag/zfsbackup/internal/zfs"
)

type snapshot struct {
	Name      string
	Timestamp time.Time
	CreateTxg uint64
	preserve  bool
}

// policy is a validated DeleterConfig with its durations parsed once.
type policy struct {
	topN      int64
	newerThan time.Duration // zero when preserve_newer_than is unset
	rules     []rule
}

type rule struct {
	config.RetentionRule               // as configured, for messages
	interval             time.Duration // parsed RetentionRule.Interval
}

func newPolicy(dc *config.DeleterConfig) (*policy, error) {
	if dc.PreserveTopN < 0 {
		return nil, fmt.Errorf("preserve_top_n must not be negative")
	}
	p := &policy{topN: dc.PreserveTopN}
	if dc.PreserveNewerThan != "" {
		newerThan, err := zfs.ParseDuration(dc.PreserveNewerThan)
		if err != nil {
			return nil, fmt.Errorf("invalid preserve_newer_than: %w", err)
		}
		p.newerThan = newerThan
	}
	for _, r := range dc.Rules {
		interval, err := zfs.ParseDuration(r.Interval)
		if err != nil {
			return nil, fmt.Errorf("invalid retention interval: %w", err)
		}
		if interval <= 0 || r.Count <= 0 {
			return nil, fmt.Errorf("retention interval and count must be positive: %v", r)
		}
		p.rules = append(p.rules, rule{RetentionRule: r, interval: interval})
	}
	return p, nil
}

type deleteFsProcessor struct {
	fs      string
	policy  *policy
	snaps   []snapshot
	now     time.Time
	regexps []*regexp.Regexp
}

func newDeleteFsProcessor(dc *config.DeleterConfig, fs string) (*deleteFsProcessor, error) {
	p, err := newPolicy(dc)
	if err != nil {
		return nil, err
	}
	dfp := &deleteFsProcessor{fs: fs, policy: p}
	for _, r := range dc.Regex {
		re, err := regexp.Compile(r)
		if err != nil {
			return nil, fmt.Errorf("invalid regexp %q: %w", r, err)
		}
		dfp.regexps = append(dfp.regexps, re)
	}
	return dfp, nil
}

func (dfp *deleteFsProcessor) getValidatedSnaps() error {
	found, err := zfs.ZfsList([]string{"name", "createtxg", "creation"}, "snapshot", dfp.fs, "-s", "createtxg")
	if err != nil {
		return err
	}
	dfp.snaps = dfp.snaps[:0]
	for _, arr := range found {
		snapname := strings.TrimPrefix(arr[0], dfp.fs+"@")
		txg, err := strconv.ParseUint(arr[1], 10, 64)
		if err != nil {
			return fmt.Errorf("cannot parse %q as txg number for snapshot %s: %w", arr[1], snapname, err)
		}
		unixtime, err := strconv.ParseInt(arr[2], 10, 64)
		if err != nil {
			return fmt.Errorf("cannot parse creation timestamp for %s: %w", snapname, err)
		}
		matched := false
		for _, re := range dfp.regexps {
			if re.MatchString(snapname) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		dfp.snaps = append(dfp.snaps, snapshot{
			Name:      snapname,
			Timestamp: time.Unix(unixtime, 0),
			CreateTxg: txg,
		})
	}
	slices.SortFunc(dfp.snaps, func(a, b snapshot) int {
		if cmp := a.Timestamp.Compare(b.Timestamp); cmp != 0 {
			return cmp
		}
		return cmp.Compare(a.CreateTxg, b.CreateTxg)
	})
	for i := 1; i < len(dfp.snaps); i++ {
		if dfp.snaps[i-1].CreateTxg > dfp.snaps[i].CreateTxg {
			return fmt.Errorf("%v has createtxg not greater than %v", dfp.snaps[i], dfp.snaps[i-1])
		}
	}
	return nil
}

func (dfp *deleteFsProcessor) preserveTopN() {
	topN := int(min(dfp.policy.topN, int64(len(dfp.snaps))))
	for i := len(dfp.snaps) - topN; i < len(dfp.snaps); i++ {
		dfp.snaps[i].preserve = true
	}
}

func (dfp *deleteFsProcessor) preserveNewerThan() {
	if dfp.policy.newerThan == 0 {
		return
	}
	for i := range dfp.snaps {
		if dfp.now.Sub(dfp.snaps[i].Timestamp) < dfp.policy.newerThan {
			dfp.snaps[i].preserve = true
		}
	}
}

// markForPreservation applies the retention policy to dfp.snaps, setting the
// preserve flag on each snapshot that should be kept, and returns the names of
// snapshots that should be deleted. dfp.snaps must already be populated. This
// is separated from ProcessFs so it can be tested without ZFS.
func (dfp *deleteFsProcessor) markForPreservation() ([]string, error) {
	if len(dfp.snaps) == 0 {
		return nil, nil
	}
	dfp.now = dfp.snaps[len(dfp.snaps)-1].Timestamp
	dfp.preserveTopN()
	dfp.preserveNewerThan()

	for _, r := range dfp.policy.rules {
		slog.Debug("applying retention rule", "rule", r.RetentionRule)
		intervalSize := r.interval
		nowUnix := dfp.now.Unix()
		lastInterval := int64(-1)
		holeMsg := ""
		// Walk newest first so each bucket retains its most recent snapshot.
		// Tracking bucket transitions also avoids allocating Count entries.
		for i := len(dfp.snaps) - 1; i >= 0; i-- {
			intervalIdx := (nowUnix - dfp.snaps[i].Timestamp.Unix()) / int64(intervalSize/time.Second)
			slog.Debug("snap interval", "snap", dfp.snaps[i].Name, "interval", intervalIdx)
			if intervalIdx < 0 || intervalIdx >= r.Count || intervalIdx == lastInterval {
				continue
			}
			if intervalIdx > lastInterval+1 && holeMsg == "" {
				holeMsg = fmt.Sprintf("hole in retention rule %v for %s; intervals: %d to %d", r.RetentionRule, dfp.fs, lastInterval+1, intervalIdx-1)
				if !r.AllowHoles {
					return nil, fmt.Errorf("%s", holeMsg)
				}
			}
			dfp.snaps[i].preserve = true
			lastInterval = intervalIdx
		}
		if holeMsg != "" {
			slog.Warn(holeMsg)
		}
	}

	var toDelete []string
	for _, s := range dfp.snaps {
		if !s.preserve {
			toDelete = append(toDelete, s.Name)
		}
	}
	return toDelete, nil
}

// ProcessFs applies retention rules to dfp.fs and deletes snapshots that
// should no longer be kept. When dryRun is true, deletions are printed but
// not executed.
func (dfp *deleteFsProcessor) ProcessFs(dryRun bool) error {
	slog.Info("processing filesystem", "fs", dfp.fs)
	if err := dfp.getValidatedSnaps(); err != nil {
		return err
	}
	toDelete, err := dfp.markForPreservation()
	if err != nil {
		return err
	}
	slog.Debug("retention rules applied", "fs", dfp.fs)
	return deleteManySnaps(dfp.fs, dryRun, toDelete)
}

func deleteManySnaps(fs string, dryRun bool, toDelete []string) error {
	if len(toDelete) == 0 {
		return nil
	}
	target := fs + "@" + strings.Join(toDelete, ",")
	const argMax = 131072
	if len(target) < argMax/2 || len(toDelete) == 1 {
		flags := []string{"-d", "-v"}
		if dryRun {
			flags = append(flags, "-n")
		}
		return zfs.ZfsDestroy(target, flags...)
	}
	mid := len(toDelete) / 2
	if err := deleteManySnaps(fs, dryRun, toDelete[:mid]); err != nil {
		return err
	}
	return deleteManySnaps(fs, dryRun, toDelete[mid:])
}

// Run applies the deleter config to all matching filesystems.
// Per-filesystem errors are collected and returned as a joined error.
func Run(cfg *config.Config, parallelism int, dryRun bool) error {
	if cfg.Deleter == nil {
		return fmt.Errorf("deleter: no deleter section in config")
	}
	dc := cfg.Deleter
	if parallelism < 1 {
		return fmt.Errorf("parallelism must be positive")
	}
	processor, err := newDeleteFsProcessor(dc, "")
	if err != nil {
		return err
	}
	include := cfg.ResolveInclude(dc.Include)
	exclude := cfg.ResolveExclude(dc.Exclude)
	// ExpandFsToProcess already returns a sorted slice; no additional sort needed.
	fsToProcess, discoveryErr := zfs.ExpandFsToProcess(include, exclude)

	var (
		mu   sync.Mutex
		errs = []error{discoveryErr}
		wg   sync.WaitGroup
	)
	sem := make(chan struct{}, parallelism)
	for _, fs := range fsToProcess {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			dfp := *processor
			dfp.fs = fs
			if err := dfp.ProcessFs(dryRun); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", fs, err))
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	return errors.Join(errs...)
}

func Main() {
	deleterFlags := flag.NewFlagSet("deleter", flag.ExitOnError)
	configFile := deleterFlags.String("config", "", "path to config file")
	dryRun := deleterFlags.Bool("dry-run", true, "print deletions without executing (default: true for safety)")
	parallelism := deleterFlags.Int("parallelism", 1, "number of filesystems to process in parallel")
	debug := deleterFlags.Bool("debug", false, "enable debug logging")
	deleterFlags.Parse(os.Args[2:])
	zfs.SetupLogger(*debug)
	if deleterFlags.NArg() != 0 {
		zfs.Fatal("unexpected arguments", "args", deleterFlags.Args())
	}

	cfg := &config.Config{}
	lock := zfs.LoadConfig(*configFile, cfg)
	defer lock.Close()
	if cfg.Deleter == nil {
		zfs.Fatal("no deleter section in config")
	}

	if err := Run(cfg, *parallelism, *dryRun); err != nil {
		zfs.MyFatalFnF("deleter completed with errors:\n%s", err.Error())
	}
}
