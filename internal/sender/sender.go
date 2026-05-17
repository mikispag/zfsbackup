package sender

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/mikispag/zfsbackup/internal/config"
	"github.com/mikispag/zfsbackup/internal/zfs"
)

const (
	Unknown  = iota
	Snapshot = iota
	Bookmark = iota
)

type source struct {
	Name    string
	GUID    string
	Srctype int
}

// fsProcessor handles sending all snapshots for one (filesystem, destination)
// pair. job holds the per-job settings that are shared across all destinations;
// dst holds the settings specific to the destination being processed.
type fsProcessor struct {
	fs          string
	job         *config.SenderConfig
	dst         *config.DestinationConfig
	snapRe      *regexp.Regexp // compiled from job.SnapshotRegex in NewFSProcessor
	sources     []source
	resumeToken string
	incremental source
	sendFull    bool
}

func (fsp *fsProcessor) PotentialSnapsToSend() []source {
	var ret []source
	for _, s := range fsp.sources {
		if s.Srctype == Snapshot && fsp.snapRe.MatchString(zfs.SnapshotName(s.Name)) {
			ret = append(ret, s)
		}
	}
	return ret
}

func (fsp *fsProcessor) ActualSnapsToSend() []source {
	re := fsp.snapRe
	interestingGUIDs := make(map[string]bool)

	// Pass 1: identify GUIDs of placeholder bookmarks that must also be sent so
	// the destination accumulates the corresponding snapshots.
	metSource := fsp.incremental.Srctype == Unknown
	for _, s := range fsp.sources {
		if s.GUID == fsp.incremental.GUID {
			metSource = true
			continue
		}
		if !metSource || s.Srctype != Bookmark {
			continue
		}
		for _, p := range fsp.dst.SyncPlaceholders {
			if strings.HasSuffix(s.Name, "-"+p) {
				interestingGUIDs[s.GUID] = true
			}
		}
	}

	// Pass 2: walk source-ordered (createtxg-ordered) snapshots, recording
	// matches and the index of the newest matching snap.
	metSource = fsp.incremental.Srctype == Unknown
	type entry struct {
		snap    source
		matches bool
	}
	var entries []entry
	newestMatchingIdx := -1
	for _, s := range fsp.sources {
		if s.GUID == fsp.incremental.GUID {
			metSource = true
			continue
		}
		if !metSource || s.Srctype != Snapshot {
			continue
		}
		match := re.MatchString(zfs.SnapshotName(s.Name))
		if match {
			newestMatchingIdx = len(entries)
		}
		entries = append(entries, entry{snap: s, matches: match})
	}

	// Pass 3: mark which entries to send.
	toSend := make([]bool, len(entries))
	for i, e := range entries {
		if interestingGUIDs[e.snap.GUID] {
			toSend[i] = true
			slog.Info("sending snapshot due to placeholder", "snap", e.snap.Name)
		}
	}
	if newestMatchingIdx >= 0 {
		if fsp.job.SendIntermediate {
			for i, e := range entries {
				if e.matches {
					toSend[i] = true
				}
			}
		} else {
			toSend[newestMatchingIdx] = true
			slog.Debug("sending most recent matching snapshot", "snap", entries[newestMatchingIdx].snap.Name)
		}
	}

	// Pass 4: emit in source (createtxg) order so incremental sends are valid.
	var mandatory []source
	for i, e := range entries {
		if toSend[i] {
			mandatory = append(mandatory, e.snap)
		}
	}
	return mandatory
}

func (fsp *fsProcessor) sendPlaceholders() error {
	if len(fsp.dst.SyncPlaceholders) == 0 {
		return nil
	}
	sp := &config.SetPlaceholders{FS: fsp.fs}
	bookmarks, err := zfs.ZfsList([]string{"name", "guid"}, "bookmark", fsp.fs)
	if err != nil {
		return err
	}
	potentialPlaceholders := make(map[string]string)
	for _, b := range bookmarks {
		arr := strings.Split(zfs.BookmarkName(b[0]), "-")
		if len(arr) < 2 {
			continue
		}
		potentialPlaceholders[arr[len(arr)-1]] = b[1]
	}
	for _, p := range fsp.dst.SyncPlaceholders {
		guid, found := potentialPlaceholders[p]
		if !found {
			continue
		}
		sp.Placeholders = append(sp.Placeholders, config.PlaceholderEntry{
			Name: p,
			GUID: guid,
		})
	}
	slog.Debug("sending placeholders", "placeholders", sp)
	spBytes, err := json.Marshal(sp)
	if err != nil {
		return fmt.Errorf("cannot marshal SetPlaceholders: %w", err)
	}
	cmd := buildReceiverCmd(context.Background(), fsp.dst.Receiver,
		"--op=set_placeholders", "--dataset="+fsp.fs)
	wrcloser, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("cannot open stdin pipe: %w", err)
	}
	go func() {
		if _, err := wrcloser.Write(spBytes); err != nil {
			slog.Error("failed to write SetPlaceholders payload", "err", err, "fs", fsp.fs)
		}
		wrcloser.Close()
	}()
	out, err := cmd.Output()
	if err != nil {
		slog.Error("set_placeholders failed", "output", string(out), "fs", fsp.fs)
		return fmt.Errorf("cannot send placeholders for %s: %w", fsp.fs, err)
	}
	slog.Debug("set_placeholders response", "output", string(out))
	return nil
}

func (fsp *fsProcessor) GetIncrementalSuggestions() error {
	cmd := buildReceiverCmd(context.Background(), fsp.dst.Receiver,
		"--op=incremental_suggestions", "--dataset="+fsp.fs)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("cannot get incremental suggestions for %s: %w", fsp.fs, err)
	}
	is := &config.IncrementalSuggestions{}
	if err := json.Unmarshal(out, is); err != nil {
		return fmt.Errorf("cannot decode IncrementalSuggestions: %w", err)
	}
	slog.Debug("incremental suggestions", "suggestions", is, "fs", fsp.fs)

	if is.SendFull {
		fsp.incremental = source{}
		fsp.sendFull = true
		return nil
	}
	resumable := fsp.job.Resumable == nil || *fsp.job.Resumable
	if is.ResumeToken != "" && resumable {
		fsp.resumeToken = is.ResumeToken
	} else if is.ResumeToken != "" {
		slog.Warn("receiver has a resume token but resumable is disabled", "fs", fsp.fs)
	}
	if is.LastSnapshot != "" && is.GUID != "" {
		for _, s := range fsp.sources {
			if s.GUID == is.GUID {
				fsp.incremental = s
				return nil
			}
		}
	}
	return nil
}

func (fsp *fsProcessor) FillSources() error {
	found, err := zfs.ZfsList([]string{"name", "guid"}, "snapshot,bookmark", fsp.fs, "-r", "-d1", "-s", "createtxg")
	if err != nil {
		slog.Error("cannot list snapshots/bookmarks", "fs", fsp.fs, "err", err)
		return err
	}
	for _, arr := range found {
		var srctype int
		switch {
		case strings.Contains(arr[0], "@"):
			srctype = Snapshot
		case strings.Contains(arr[0], "#"):
			srctype = Bookmark
		default:
			return fmt.Errorf("%s is neither a snapshot nor a bookmark", arr[0])
		}
		fsp.sources = append(fsp.sources, source{Name: arr[0], GUID: arr[1], Srctype: srctype})
	}
	return nil
}

func (fsp *fsProcessor) getIncrementalParam() []string {
	switch fsp.incremental.Srctype {
	case Unknown:
		return nil
	case Bookmark, Snapshot:
		return []string{"-i", fsp.incremental.Name}
	default:
		zfs.Fatal("unexpected incremental source type", "srctype", fsp.incremental.Srctype)
		return nil
	}
}

func (fsp *fsProcessor) setPlaceholdersBefore(snap source) error {
	for _, p := range fsp.dst.Placeholders {
		if err := zfs.ZfsSetBeforeBookmark(snap.Name, p); err != nil {
			return err
		}
	}
	return nil
}

func (fsp *fsProcessor) setPlaceholdersAfter(snap source) error {
	for _, p := range fsp.dst.Placeholders {
		if err := zfs.ZfsSetPlaceholder(snap.Name, p); err != nil {
			return err
		}
	}
	return nil
}

func compressionType(dst *config.DestinationConfig) string {
	if dst.Compression == "" {
		return "none"
	}
	return dst.Compression
}

func (fsp *fsProcessor) send(zfsArgs []string, fast bool) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eg, ctx := zfs.NewGroup(ctx)

	zfsCmd := zfs.DefaultExecCommand(ctx, "zfs", zfsArgs...)
	preader, err := zfsCmd.StdoutPipe()
	if err != nil {
		return err
	}

	bufferedReader, bufferCmd := zfs.MaybeMbuffer(ctx, fsp.dst.MbufferArgs, preader)
	ct := compressionType(fsp.dst)
	reader, comprCmd := zfs.MaybeCompress(ctx, ct, fsp.dst.CompressionLevel, bufferedReader)

	receiverArgs := []string{"--op=receive", "--dataset=" + fsp.fs,
		"--compression=" + ct}
	if fast {
		receiverArgs = append(receiverArgs, "--fast")
	}
	cmd := buildReceiverCmd(ctx, fsp.dst.Receiver, receiverArgs...)
	cmd.Stdin = reader
	cmd.Stdout = os.Stdout

	if err := zfsCmd.Start(); err != nil {
		return fmt.Errorf("cannot start zfs send: %w", err)
	}
	// Register Wait() for all started processes before attempting cmd.Start()
	// so that any early-return error path can drain them via cancel+eg.Wait().
	eg.Go(func() error {
		if err := zfsCmd.Wait(); err != nil {
			return fmt.Errorf("zfs send %v failed: %w", zfsArgs, err)
		}
		return nil
	})
	if bufferCmd != nil {
		eg.Go(func() error {
			if err := bufferCmd.Wait(); err != nil {
				return fmt.Errorf("mbuffer %v failed: %w", bufferCmd.Args, err)
			}
			return nil
		})
	}
	if comprCmd != nil {
		eg.Go(func() error {
			if err := comprCmd.Wait(); err != nil {
				return fmt.Errorf("compressor %v failed: %w", comprCmd.Args, err)
			}
			return nil
		})
	}

	if err := cmd.Start(); err != nil {
		cancel()
		eg.Wait() //nolint:errcheck — errors expected from process kill
		return fmt.Errorf("cannot start remote receiver: %w", err)
	}
	eg.Go(func() error {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("remote receive failed: %w", err)
		}
		return nil
	})
	return eg.Wait()
}

func (fsp *fsProcessor) ProcessFs() error {
	if err := fsp.FillSources(); err != nil {
		return err
	}
	if len(fsp.PotentialSnapsToSend()) == 0 {
		return nil
	}
	retriesLeft := 20
	for {
		retry, err := fsp.GetNextAndSend()
		if err != nil {
			return err
		}
		if !retry {
			return nil
		}
		retriesLeft--
		if retriesLeft == 0 {
			return fmt.Errorf("too many retries of resumable receive")
		}
	}
}

func (fsp *fsProcessor) GetNextAndSend() (retry bool, err error) {
	fsp.resumeToken = ""
	fsp.incremental = source{}
	fsp.sendFull = false

	if err := fsp.GetIncrementalSuggestions(); err != nil {
		return false, err
	}

	if !fsp.sendFull && fsp.resumeToken == "" && fsp.incremental.Srctype == Unknown {
		slog.Warn("receiver has dataset with no common base; skipping — remove the empty destination manually to enable full backup", "fs", fsp.fs)
		return false, nil
	}

	if fsp.resumeToken != "" {
		sendArgs := []string{"send", "-t", fsp.resumeToken}
		if err := fsp.send(sendArgs, false); err != nil {
			return false, err
		}
		return true, nil
	}

	includeProps := fsp.job.IncludeProperties == nil || *fsp.job.IncludeProperties
	fast := false
	for _, snap := range fsp.ActualSnapsToSend() {
		// Flags must precede the snapshot operand; some ZFS implementations
		// do not accept POSIX argument permutation.
		sendArgs := []string{"send", "-v", "-L"}
		if includeProps {
			sendArgs = append(sendArgs, "-p")
		}
		if fsp.dst.RawSend {
			sendArgs = append(sendArgs, "-e", "-c", "-w")
		}
		sendArgs = append(sendArgs, fsp.getIncrementalParam()...)
		sendArgs = append(sendArgs, snap.Name)

		if err := fsp.setPlaceholdersBefore(snap); err != nil {
			return false, err
		}
		if err := fsp.send(sendArgs, fast); err != nil {
			return false, err
		}
		fsp.incremental = snap
		if err := fsp.setPlaceholdersAfter(snap); err != nil {
			return false, err
		}
		fast = true
	}

	if err := fsp.sendPlaceholders(); err != nil {
		return false, err
	}
	return false, nil
}

// NewFSProcessor creates an fsProcessor for the given filesystem, job-level
// config, and destination. It compiles the snapshot regex once so it is not
// recompiled on every snapshot list traversal.
func NewFSProcessor(fs string, job *config.SenderConfig, dst *config.DestinationConfig) *fsProcessor {
	txtre := job.SnapshotRegex
	if txtre == "" {
		txtre = ".*"
	}
	return &fsProcessor{
		fs:     fs,
		job:    job,
		dst:    dst,
		snapRe: regexp.MustCompile(fmt.Sprintf("^%s$", txtre)),
	}
}

// autoPlaceholderName returns a stable, hyphen-free suffix for a placeholder
// bookmark derived from the receiver command. The same receiver string always
// produces the same suffix, so the bookmark name is consistent across runs.
// Hyphens are intentionally absent: zfsGcPlaceholder extracts the suffix by
// splitting on "-" and taking the last component, so the suffix must be a
// single hyphen-free token.
func autoPlaceholderName(cmdReceiver string) string {
	h := fnv.New64a()
	h.Write([]byte(strings.Join(strings.Fields(cmdReceiver), " ")))
	return fmt.Sprintf("dst%016x", h.Sum64())
}

// buildReceiverCmd constructs a receiver command from cmdReceiver (split on
// whitespace) with extra args appended. Uses exec.CommandContext directly so
// it handles both absolute paths and PATH lookup without a shell.
func buildReceiverCmd(ctx context.Context, cmdReceiver string, args ...string) *exec.Cmd {
	parts := strings.Fields(cmdReceiver)
	if len(parts) == 0 {
		zfs.Fatal("receiver is empty")
	}
	allArgs := append(parts[1:], args...)
	cmd := exec.CommandContext(ctx, parts[0], allArgs...)
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	slog.Debug("exec", "cmd", cmd.Args)
	return cmd
}

// initSenderDefaults applies per-job and per-destination defaults and validates
// the config in-place. Called by both Run and Main so the logic lives in one
// place.
func initSenderDefaults(sc *config.SenderConfig) {
	if len(sc.Destinations) == 0 {
		zfs.Fatal("sender config must have at least one destination")
	}
	if sc.Resumable == nil {
		t := true
		sc.Resumable = &t
	}
	if sc.IncludeProperties == nil {
		t := true
		sc.IncludeProperties = &t
	}
	for i := range sc.Destinations {
		dst := &sc.Destinations[i]
		if len(dst.Placeholders) == 0 && dst.Receiver != "" {
			name := autoPlaceholderName(dst.Receiver)
			dst.Placeholders = []string{name}
			slog.Info("placeholder bookmarks auto-enabled", "destination", dst.Receiver, "name", name)
		}
		for _, p := range dst.Placeholders {
			if strings.Contains(p, "-") {
				zfs.Fatal("placeholder suffix must not contain hyphens — zfsGcPlaceholder splits on \"-\" and uses the last component as the key",
					"suffix", p)
			}
		}
		if len(dst.MbufferArgs) == 0 && zfs.MbufferPresent() {
			dst.MbufferArgs = []string{"-p 90", "-m 3%"}
		}
	}
}

// processFilesystem sends to every configured destination for a single
// filesystem, collecting per-destination errors. If multiple destinations are
// configured, a failure on one does not prevent the others from being tried.
func processFilesystem(fs string, sc *config.SenderConfig) error {
	var errs []error
	for i := range sc.Destinations {
		dst := &sc.Destinations[i]
		fsp := NewFSProcessor(fs, sc, dst)
		if err := fsp.ProcessFs(); err != nil {
			if len(sc.Destinations) > 1 {
				errs = append(errs, fmt.Errorf("destination %q: %w", dst.Receiver, err))
			} else {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Run runs the sender for all filesystems in cfg using the given parallelism.
// If limitFs is non-empty only that one filesystem is processed; pass "" to
// process all. Per-filesystem errors are collected and returned as a joined error.
func Run(cfg *config.Config, parallelism int, limitFs string) error {
	if cfg.Sender == nil {
		return fmt.Errorf("sender: no sender section in config")
	}
	sc := cfg.Sender
	initSenderDefaults(sc)

	if parallelism < 1 {
		parallelism = 1
	}

	include := cfg.ResolveInclude(sc.Include)
	exclude := cfg.ResolveExclude(sc.Exclude)
	fsToProcess := zfs.ExpandFsToProcess(include, exclude)

	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	sem := make(chan struct{}, parallelism)
	for _, fs := range fsToProcess {
		if limitFs != "" && fs != limitFs {
			continue
		}
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := processFilesystem(fs, sc); err != nil {
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
	senderFlags := flag.NewFlagSet("sender", flag.ExitOnError)
	configFile := senderFlags.String("config", "", "path to config file")
	limitFs := senderFlags.String("limit-fs", "", "restrict operation to this filesystem")
	parallelism := senderFlags.Int("parallelism", 1, "number of filesystems to process in parallel")
	debug := senderFlags.Bool("debug", false, "enable debug logging")
	senderFlags.Parse(flag.Args()[1:])
	zfs.SetupLogger(*debug)

	cfg := &config.Config{}
	zfs.LoadConfig(*configFile, cfg)
	if cfg.Sender == nil {
		zfs.Fatal("no sender section in config")
	}
	if *limitFs != "" {
		zfs.FatalIfError(zfs.IsValidZFSDataset(*limitFs), "invalid --limit-fs value: %w")
	}

	if err := Run(cfg, *parallelism, *limitFs); err != nil {
		zfs.Fatal("backup completed with errors", "err", err)
	}
}
