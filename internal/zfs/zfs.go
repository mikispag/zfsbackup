package zfs

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

var dsRegexp = regexp.MustCompile(`^[A-Z0-9a-z_.:-]+(/[A-Z0-9a-z_.:-]+)*$`)

// IsValidZFSDataset returns an error if name is not a valid ZFS dataset path.
func IsValidZFSDataset(name string) error {
	if !dsRegexp.MatchString(name) {
		return fmt.Errorf("%q is not a valid ZFS dataset name", name)
	}
	for _, component := range strings.Split(name, "/") {
		if component == "." || component == ".." {
			return fmt.Errorf("%q is not a valid ZFS dataset name: %q is not a valid component", name, component)
		}
	}
	return nil
}

// ZfsCreate creates a new ZFS filesystem.
// If disableMount is true the dataset is created with canmount=off.
func ZfsCreate(fs string, disableMount bool) error {
	args := []string{"create"}
	if disableMount {
		args = append(args, "-o", "canmount=off")
	}
	args = append(args, fs)
	_, err := DefaultExecCommand(context.Background(), "zfs", args...).Output()
	return err
}

// ZfsGet returns the value of a single ZFS property on a dataset.
func ZfsGet(fs, prop string) (string, error) {
	b, err := DefaultExecCommand(context.Background(), "zfs", "get", "-H", "-o", "value", prop, fs).Output()
	return string(b), err
}

// ZfsDestroy destroys a ZFS dataset or snapshot.
// Additional flags (e.g. "-r", "-d", "-v") may be passed via flags.
func ZfsDestroy(target string, flags ...string) error {
	args := append([]string{"destroy"}, flags...)
	args = append(args, target)
	_, err := DefaultExecCommand(context.Background(), "zfs", args...).Output()
	return err
}

// ParseTabular runs a command and returns its tab-separated output as a slice
// of rows, each row being a slice of field values.
func ParseTabular(arg0 string, args []string) ([][]string, error) {
	out, err := DefaultExecCommand(context.Background(), arg0, args...).Output()
	if err != nil {
		return nil, err
	}
	var ret [][]string
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		ret = append(ret, strings.Split(line, "\t"))
	}
	return ret, nil
}

// ParseTabularBatched runs ParseTabular in chunks to avoid ARG_MAX limits when
// datasets is large. fixedArgs are the command arguments before the dataset
// names; each chunk appends as many dataset names as fit within argMaxSafe bytes.
func ParseTabularBatched(arg0 string, fixedArgs, datasets []string) ([][]string, error) {
	if len(datasets) == 0 {
		return nil, nil
	}
	const argMaxSafe = 65536
	overhead := len(arg0)
	for _, a := range fixedArgs {
		overhead += len(a) + 1
	}

	var (
		all      [][]string
		batch    []string
		batchLen = overhead
	)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		rows, err := ParseTabular(arg0, append(fixedArgs, batch...))
		if err != nil {
			return err
		}
		all = append(all, rows...)
		batch = batch[:0]
		batchLen = overhead
		return nil
	}
	for _, ds := range datasets {
		if len(batch) > 0 && batchLen+len(ds)+1 > argMaxSafe {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		batch = append(batch, ds)
		batchLen += len(ds) + 1
	}
	return all, flush()
}

// ZfsList runs "zfs list" for the given properties and object type under fullds,
// returning the tab-separated output as a slice of rows.
func ZfsList(props []string, t string, fullds string, flags ...string) ([][]string, error) {
	args := []string{"list", "-H", "-p", "-o", strings.Join(props, ","), "-t", t}
	if t == "snapshot" || t == "bookmark" {
		args = append(args, "-r", "-d1")
	}
	args = append(args, flags...)
	args = append(args, fullds)
	return ParseTabular("zfs", args)
}

// Exists reports whether a ZFS filesystem exists.
func Exists(fullds string) bool {
	outp, err := ZfsList([]string{"name"}, "filesystem", fullds)
	return err == nil && len(outp) == 1
}

// PoolName returns the pool component of a ZFS object name.
func PoolName(fullname string) string {
	return strings.SplitN(fullname, "/", 2)[0]
}

// FSName returns the filesystem component of a snapshot or bookmark name.
func FSName(fullname string) string {
	for _, sep := range []string{"@", "#"} {
		if parts := strings.SplitN(fullname, sep, 2); len(parts) == 2 {
			return parts[0]
		}
	}
	return fullname
}

// SnapshotName returns the snapshot component of a full snapshot name
// (everything after the @).
func SnapshotName(fullname string) string {
	parts := strings.SplitN(fullname, "@", 2)
	if len(parts) != 2 {
		MyFatalFn(fmt.Sprintf("invalid snapshot name %q: missing @", fullname))
	}
	return parts[1]
}

// BookmarkName returns the bookmark component of a full bookmark name
// (everything after the #).
func BookmarkName(fullname string) string {
	parts := strings.SplitN(fullname, "#", 2)
	if len(parts) != 2 {
		MyFatalFn(fmt.Sprintf("invalid bookmark name %q: missing #", fullname))
	}
	return parts[1]
}

// zfsGcPlaceholder removes any placeholder bookmarks on the same filesystem
// that share the same placeholder suffix but differ from toKeep.
func zfsGcPlaceholder(toKeep string) error {
	arr := strings.Split(toKeep, "-")
	if len(arr) < 2 {
		return fmt.Errorf("%q is not a valid placeholder bookmark", toKeep)
	}
	placeholder := arr[len(arr)-1]
	bookmarks, err := ZfsList([]string{"name"}, "bookmark", FSName(toKeep))
	if err != nil {
		return err
	}
	for _, b := range bookmarks {
		if b[0] == toKeep {
			continue
		}
		if strings.HasSuffix(BookmarkName(b[0]), "-"+placeholder) {
			if err := ZfsDestroy(b[0]); err != nil {
				return err
			}
		}
	}
	return nil
}

// zfsBookmarkIdempotent creates bookmark dst from src and returns nil if the
// bookmark already exists, making placeholder operations safe to retry after
// interrupted backup runs.
func zfsBookmarkIdempotent(src, dst string) error {
	if _, err := DefaultExecCommand(context.Background(), "zfs", "bookmark", src, dst).Output(); err != nil {
		existing, listErr := ZfsList([]string{"name"}, "bookmark", FSName(src))
		if listErr != nil {
			slog.Debug("cannot list bookmarks after failed bookmark creation; assuming bookmark does not exist",
				"err", listErr, "src", src)
			return err
		}
		fullDst := FSName(src) + dst
		for _, row := range existing {
			if row[0] == fullDst {
				return nil
			}
		}
		return err
	}
	return nil
}

// ZfsSetBeforeBookmark creates a "before-send" bookmark for src, named
// #<snapname>-before-<placeholderName>. Idempotent.
func ZfsSetBeforeBookmark(src, placeholderName string) error {
	return zfsBookmarkIdempotent(src, "#"+SnapshotName(src)+"-before-"+placeholderName)
}

// ZfsSetPlaceholder creates a placeholder bookmark for src, named
// #<snapname>-<placeholderName>, and garbage-collects older placeholders
// with the same suffix. Idempotent.
func ZfsSetPlaceholder(src, placeholderName string) error {
	newbm := "#" + SnapshotName(src) + "-" + placeholderName
	if err := zfsBookmarkIdempotent(src, newbm); err != nil {
		return err
	}
	return zfsGcPlaceholder(FSName(src) + newbm)
}
