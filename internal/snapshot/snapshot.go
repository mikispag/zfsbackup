package snapshot

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/mikispag/zfsbackup/internal/config"
	"github.com/mikispag/zfsbackup/internal/zfs"
)

func MaybeSkipSnaps(cfg *config.SnapshotConfig, fsToProcess []string) map[string]bool {
	fsToSkip := make(map[string]bool)
	if cfg.SkipEmptyYoungerThan == "" {
		return fsToSkip
	}
	noEmptyNewerThan, err := zfs.ParseDuration(cfg.SkipEmptyYoungerThan)
	if err != nil {
		slog.Error("cannot parse skip_empty_younger_than; continuing without skipping",
			"err", err, "value", cfg.SkipEmptyYoungerThan)
		return fsToSkip
	}

	out, err := zfs.ParseTabularBatched("zfs",
		[]string{"get", "-H", "-o", "name,value", "written"}, fsToProcess)
	if err != nil {
		slog.Error("cannot query 'written' property; continuing without skipping", "err", err)
		return fsToSkip
	}

	var emptyFS []string
	for _, row := range out {
		if row[1] == "0" {
			emptyFS = append(emptyFS, row[0])
		}
	}

	latestSnapTimes := make(map[string]int64)
	snapOut, err := zfs.ParseTabularBatched("zfs",
		[]string{"get", "-H", "-p", "-r", "-d1", "-o", "name,value", "-t", "snapshot", "creation"},
		emptyFS)
	if err != nil {
		slog.Error("cannot query snapshot creation times; continuing without skipping", "err", err)
		return fsToSkip
	}
	for _, row := range snapOut {
		t, err := strconv.ParseInt(row[1], 10, 64)
		if err != nil {
			slog.Error("cannot parse snapshot creation timestamp", "err", err, "value", row[1])
			continue
		}
		fs := zfs.FSName(row[0])
		if t > latestSnapTimes[fs] {
			latestSnapTimes[fs] = t
		}
	}

	cutoff := time.Now().Add(-noEmptyNewerThan).Unix()
	for fs, t := range latestSnapTimes {
		if t > cutoff {
			fsToSkip[fs] = true
		}
	}
	return fsToSkip
}

// Run runs the snapshot module for the given config.
func Run(cfg *config.Config, dryRun bool) error {
	sc := cfg.Snapshot
	include := cfg.ResolveInclude(sc.Include)
	exclude := cfg.ResolveExclude(sc.Exclude)
	fsToProcess := zfs.ExpandFsToProcess(include, exclude)
	fsToSkip := MaybeSkipSnaps(sc, fsToProcess)

	snapName := time.Now().Format(sc.NamePattern)
	perPoolArgs := make(map[string][]string)
	for _, fs := range fsToProcess {
		if fsToSkip[fs] {
			continue
		}
		perPoolArgs[zfs.PoolName(fs)] = append(perPoolArgs[zfs.PoolName(fs)], fs+"@"+snapName)
	}

	sortedPools := make([]string, 0, len(perPoolArgs))
	for pool := range perPoolArgs {
		sortedPools = append(sortedPools, pool)
	}
	sort.Strings(sortedPools)

	var errs []error
	for _, pool := range sortedPools {
		poolArgs := perPoolArgs[pool]
		args := append([]string{"snapshot"}, poolArgs...)
		cmd := zfs.DefaultExecCommand(context.Background(), "zfs", args...)
		if dryRun {
			slog.Info("dry-run: would execute", "cmd", cmd.Args)
			continue
		}
		if _, err := cmd.Output(); err != nil {
			slog.Error("zfs snapshot failed", "err", err, "snapshots", poolArgs)
			errs = append(errs, err)
		}
	}

	if len(fsToSkip) > 0 {
		skipped := make([]string, 0, len(fsToSkip))
		for fs := range fsToSkip {
			skipped = append(skipped, fs)
		}
		slog.Info("skipped empty unchanged filesystems", "filesystems", skipped)
	}

	return errors.Join(errs...)
}

func Main() {
	snapshotFlags := flag.NewFlagSet("snapshot", flag.ExitOnError)
	configFile := snapshotFlags.String("config", "", "path to config file")
	dryRun := snapshotFlags.Bool("dry-run", false, "print commands without executing")
	debug := snapshotFlags.Bool("debug", false, "enable debug logging")
	snapshotFlags.Parse(flag.Args()[1:])
	zfs.SetupLogger(*debug)

	cfg := &config.Config{}
	zfs.LoadConfig(*configFile, cfg)
	if cfg.Snapshot == nil {
		zfs.Fatal("no snapshot section in config")
	}

	if err := Run(cfg, *dryRun); err != nil {
		zfs.MyFatalFnF("zfs snapshot completed with errors: %v", err)
	}
}
