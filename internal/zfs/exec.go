// Package zfs provides ZFS operations and process-execution infrastructure
// used by all zfsbackup sub-commands.
package zfs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	suffixMap = map[string]time.Duration{
		"s": time.Second,
		"m": time.Minute,
		"h": time.Hour,
		"d": 24 * time.Hour,
		"w": 7 * 24 * time.Hour,
		"y": 365 * 24 * time.Hour,
	}

	execPathCache sync.Map
	execPathMu    sync.Mutex
)

// ParseDuration parses a duration string with a unit suffix (e.g. "30d", "2h", "5m").
func ParseDuration(duration string) (time.Duration, error) {
	if len(duration) < 2 {
		return 0, fmt.Errorf("%q is not a valid duration: too short", duration)
	}
	suf := string(duration[len(duration)-1])
	num := duration[:len(duration)-1]
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a valid duration: %w", duration, err)
	}
	mult, found := suffixMap[suf]
	if !found {
		return 0, fmt.Errorf("%q is not a valid duration: unrecognised suffix %q", duration, suf)
	}
	return time.Duration(n) * mult, nil
}

// findExecutablePath searches PATH plus /sbin and /usr/sbin for name without
// modifying any global state. Returns the path and true if found.
func findExecutablePath(name string) (string, bool) {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	dirs = append(dirs, "/sbin", "/usr/sbin")
	for _, dir := range dirs {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

// getExecutablePath returns the absolute path of the named binary by searching
// the process PATH plus /sbin and /usr/sbin, without modifying any global
// state. Results are cached after the first lookup so concurrent callers pay
// the search cost at most once per binary name.
func getExecutablePath(name string) string {
	if cached, ok := execPathCache.Load(name); ok {
		return cached.(string)
	}
	execPathMu.Lock()
	defer execPathMu.Unlock()
	if cached, ok := execPathCache.Load(name); ok {
		return cached.(string)
	}
	p, ok := findExecutablePath(name)
	if !ok {
		Fatal("executable not found in PATH", "binary", name)
	}
	execPathCache.Store(name, p)
	return p
}

// MbufferPresent reports whether mbuffer is available on the system,
// using the same extended PATH search as getExecutablePath.
func MbufferPresent() bool {
	_, ok := findExecutablePath("mbuffer")
	return ok
}

// DefaultExecCommand builds a command that inherits the environment, appends
// LC_ALL=C for predictable output, and forwards stderr to the parent process.
func DefaultExecCommand(ctx context.Context, arg0 string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, getExecutablePath(arg0), args...)
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	slog.Debug("exec", "cmd", cmd.Args)
	return cmd
}

// SetupLogger configures slog to write structured text output to stderr.
func SetupLogger(debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level:     level,
		AddSource: true,
	})
	slog.SetDefault(slog.New(h))
}

// Fatal logs msg at Error level and terminates the process.
func Fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

// MyFatalFn logs err at Error level and terminates the process.
func MyFatalFn(err interface{}) {
	Fatal(fmt.Sprintf("%v", err))
}

// MyFatalFnF logs a formatted message at Error level and terminates.
func MyFatalFnF(txt string, args ...interface{}) {
	MyFatalFn(fmt.Sprintf(txt, args...))
}

// FatalIfError calls MyFatalFn when err is non-nil.
// txt must contain a %w verb to wrap the error.
func FatalIfError(err error, txt string) {
	if err != nil {
		MyFatalFn(fmt.Errorf(txt, err))
	}
}

// LoadConfig reads, exclusively flocks, and JSON-decodes the config file at
// filePath into v (which must be a pointer). Unknown fields cause a fatal
// error. Calls FatalIfError on any failure.
func LoadConfig(filePath string, v any) {
	f, err := os.Open(filePath)
	FatalIfError(err, "cannot open config file: %w")
	defer f.Close()
	FatalIfError(syscall.Flock(int(f.Fd()), syscall.LOCK_EX), "cannot lock config file: %w")
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // unlock errors are benign; file closes immediately after
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	FatalIfError(d.Decode(v), "cannot parse config: %w")
}

// MaybeMbuffer wraps input with mbuffer when mbufferArgs is non-empty.
// Each element of mbufferArgs may contain a whitespace-separated flag/value
// pair (e.g. "-s 1M") and is split before being passed to exec.
func MaybeMbuffer(ctx context.Context, mbufferArgs []string, input io.ReadCloser) (io.ReadCloser, *exec.Cmd) {
	if len(mbufferArgs) == 0 {
		return input, nil
	}
	var flatArgs []string
	for _, a := range mbufferArgs {
		flatArgs = append(flatArgs, strings.Fields(a)...)
	}
	cmd := DefaultExecCommand(ctx, "mbuffer", flatArgs...)
	cmd.Stdin = input
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		Fatal("cannot create mbuffer stdout pipe", "err", err)
	}
	if err := cmd.Start(); err != nil {
		Fatal("cannot start mbuffer", "err", err, "cmd", cmd.Args)
	}
	return pipe, cmd
}

// MaybeCompress wraps input with a compressor if compressionType is not "none".
// compressionLevel may be nil to use the compressor's default level.
func MaybeCompress(ctx context.Context, compressionType string, compressionLevel *int, input io.ReadCloser) (io.ReadCloser, *exec.Cmd) {
	var args []string
	if compressionLevel != nil {
		args = append(args, fmt.Sprintf("-%d", *compressionLevel))
	}
	return internalCompress(ctx, CompressionProg(compressionType), args, input)
}

// MaybeDecompress wraps input with a decompressor for the given format name.
func MaybeDecompress(ctx context.Context, format string, input io.ReadCloser) (io.ReadCloser, *exec.Cmd) {
	return internalCompress(ctx, CompressionProg(format), []string{"-d"}, input)
}

func internalCompress(ctx context.Context, prog string, args []string, input io.ReadCloser) (io.ReadCloser, *exec.Cmd) {
	if prog == "" {
		return input, nil
	}
	cmd := DefaultExecCommand(ctx, prog, args...)
	cmd.Stdin = input
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		Fatal("cannot create compressor stdout pipe", "err", err)
	}
	if err := cmd.Start(); err != nil {
		Fatal("cannot start compressor", "err", err, "cmd", cmd.Args)
	}
	return pipe, cmd
}

// CompressionProg maps a compression type name to the corresponding binary.
// Returns an empty string for "none".
func CompressionProg(t string) string {
	progs := map[string]string{
		"none": "",
		"zstd": "zstd",
	}
	prog, ok := progs[strings.ToLower(t)]
	if !ok {
		Fatal("unknown compression type; only NONE and ZSTD are supported", "type", t)
	}
	return prog
}

// ExpandFsToProcess lists all ZFS filesystems that match include but not
// exclude, sorted so that parent datasets appear before their children.
func ExpandFsToProcess(include, exclude []string) []string {
	var fsToProcess []string
	for _, rootFS := range include {
		FatalIfError(IsValidZFSDataset(rootFS), "invalid dataset name %w")
		outp, err := ZfsList([]string{"name"}, "filesystem", rootFS, "-r")
		FatalIfError(err, "%w: cannot list filesystems under "+rootFS)
	FOUNDFS:
		for _, fields := range outp {
			FatalIfError(IsValidZFSDataset(fields[0]), "invalid dataset name %w")
			for _, exclusion := range exclude {
				FatalIfError(IsValidZFSDataset(exclusion), "invalid dataset name %w")
				if fields[0] == exclusion || strings.HasPrefix(fields[0], exclusion+"/") {
					continue FOUNDFS
				}
			}
			fsToProcess = append(fsToProcess, fields[0])
		}
	}
	sort.Strings(fsToProcess)
	// Deduplicate: overlapping include entries (e.g. "tank" and "tank/data")
	// cause the same dataset to appear more than once after recursive listing.
	out := fsToProcess[:0]
	for i, fs := range fsToProcess {
		if i == 0 || fs != fsToProcess[i-1] {
			out = append(out, fs)
		}
	}
	return out
}

