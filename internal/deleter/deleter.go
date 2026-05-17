package deleter

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"regexp"
	"sort"
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
	CreateTxg big.Int
	preserve  bool
}

type deleteFsProcessor struct {
	fs      string
	cfg     *config.DeleterConfig
	snaps   []snapshot
	now     time.Time
	regexps []*regexp.Regexp
}

func newDeleteFsProcessor(dc *config.DeleterConfig, fs string) (*deleteFsProcessor, error) {
	dfp := &deleteFsProcessor{fs: fs, cfg: dc}
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
		var txg big.Int
		if _, ok := txg.SetString(arr[1], 10); !ok {
			return fmt.Errorf("cannot parse %q as txg number for snapshot %s", arr[1], snapname)
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
	sort.Slice(dfp.snaps, func(i, j int) bool {
		return dfp.snaps[i].Timestamp.Before(dfp.snaps[j].Timestamp)
	})
	for i := 1; i < len(dfp.snaps); i++ {
		if dfp.snaps[i-1].CreateTxg.Cmp(&dfp.snaps[i].CreateTxg) == 1 {
			return fmt.Errorf("%v has createtxg not greater than %v", dfp.snaps[i], dfp.snaps[i-1])
		}
	}
	return nil
}

func (dfp *deleteFsProcessor) preserveTopN() {
	topN := int(dfp.cfg.PreserveTopN)
	if topN > len(dfp.snaps) {
		topN = len(dfp.snaps)
	}
	for i := range dfp.snaps[len(dfp.snaps)-topN:] {
		dfp.snaps[len(dfp.snaps)-topN+i].preserve = true
	}
}

func (dfp *deleteFsProcessor) preserveNewerThan() error {
	if dfp.cfg.PreserveNewerThan == "" {
		return nil
	}
	newerThan, err := zfs.ParseDuration(dfp.cfg.PreserveNewerThan)
	if err != nil {
		return err
	}
	for i := range dfp.snaps {
		if dfp.now.Sub(dfp.snaps[i].Timestamp) < newerThan {
			dfp.snaps[i].preserve = true
		}
	}
	return nil
}

// markForPreservation applies all configured retention rules to dfp.snaps,
// setting the preserve flag on each snapshot that should be kept, and returns
// the names of snapshots that should be deleted. dfp.snaps must already be
// populated. This is separated from ProcessFs so it can be tested without ZFS.
func (dfp *deleteFsProcessor) markForPreservation() ([]string, error) {
	if len(dfp.snaps) == 0 {
		return nil, nil
	}
	dfp.now = dfp.snaps[len(dfp.snaps)-1].Timestamp
	dfp.preserveTopN()
	if err := dfp.preserveNewerThan(); err != nil {
		return nil, err
	}

	for _, r := range dfp.cfg.Rules {
		slog.Debug("applying retention rule", "rule", r)
		intervalSize, err := zfs.ParseDuration(r.Interval)
		if err != nil {
			return nil, err
		}
		nowUnix := dfp.now.Unix()
		intervals := make([]bool, r.Count+1)
		for i := range dfp.snaps {
			intervalIdx := (nowUnix - dfp.snaps[i].Timestamp.Unix()) / int64(intervalSize.Seconds())
			slog.Debug("snap interval", "snap", dfp.snaps[i].Name, "interval", intervalIdx)
			if intervalIdx < 0 || intervalIdx >= int64(len(intervals)) {
				continue
			}
			if !intervals[intervalIdx] {
				dfp.snaps[i].preserve = true
				intervals[intervalIdx] = true
			}
		}

		holeMsg := fmt.Sprintf("hole in retention rule %v for %s; intervals: %v", r, dfp.fs, intervals)
		seenGap, warnHole := false, false
		for i := 1; i < len(intervals)-1; i++ {
			if !intervals[i] {
				seenGap = true
			} else if seenGap {
				if r.AllowHoles {
					warnHole = true
				} else {
					return nil, fmt.Errorf("%s", holeMsg)
				}
			}
		}
		if warnHole {
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
	dc := cfg.Deleter
	include := cfg.ResolveInclude(dc.Include)
	exclude := cfg.ResolveExclude(dc.Exclude)
	// ExpandFsToProcess already returns a sorted slice; no additional sort needed.
	fsToProcess := zfs.ExpandFsToProcess(include, exclude)

	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	sem := make(chan struct{}, parallelism)
	for _, fs := range fsToProcess {
		fs := fs
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			dfp, err := newDeleteFsProcessor(dc, fs)
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", fs, err))
				mu.Unlock()
				return
			}
			if err := dfp.ProcessFs(dryRun); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", fs, err))
				mu.Unlock()
			}
		}()
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
	deleterFlags.Parse(flag.Args()[1:])
	zfs.SetupLogger(*debug)

	cfg := &config.Config{}
	zfs.LoadConfig(*configFile, cfg)
	if cfg.Deleter == nil {
		zfs.Fatal("no deleter section in config")
	}

	if err := Run(cfg, *parallelism, *dryRun); err != nil {
		zfs.MyFatalFnF("deleter completed with errors:\n%s", err.Error())
	}
}
