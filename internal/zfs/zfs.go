package zfs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
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
// of rows, each row being a slice of field values. Used for zfs-get(8) queries
// where JSON output has a different schema from zfs-list(8).
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

// zfsPropValue represents a single property value in JSON output from
// zfs-list(8) or zpool-list(8).
type zfsPropValue struct {
	Value string `json:"value"`
}

// zfsListJSON is the top-level structure returned by 'zfs list -j'.
type zfsListJSON struct {
	Datasets map[string]struct {
		Properties map[string]zfsPropValue `json:"properties"`
	} `json:"datasets"`
}

// zpoolListJSON is the top-level structure returned by 'zpool list -j'.
type zpoolListJSON struct {
	Pools map[string]struct {
		Properties map[string]zfsPropValue `json:"properties"`
	} `json:"pools"`
}

// ZfsList runs "zfs list -j -p" for the given properties and object type under
// fullds, returning property values as a slice of rows in the order of props.
//
// Sort flags (-s key / -S key) are intercepted: the sort key is requested from
// ZFS (added to the property list if the caller omitted it), and the resulting
// rows are sorted in Go. This is necessary because JSON object keys are
// unordered; the ZFS-side sort order is not preserved in the parsed output.
func ZfsList(props []string, t string, fullds string, flags ...string) ([][]string, error) {
	// Identify sort key and direction; strip -s/-S from the command so ZFS
	// does not waste time sorting output whose order we will discard.
	sortKey, descending := "", false
	filteredFlags := make([]string, 0, len(flags))
	for i := 0; i < len(flags); i++ {
		if (flags[i] == "-s" || flags[i] == "-S") && i+1 < len(flags) {
			sortKey = flags[i+1]
			descending = flags[i] == "-S"
			i++ // skip the argument
			continue
		}
		filteredFlags = append(filteredFlags, flags[i])
	}

	// Include the sort key in the request if the caller did not ask for it,
	// so we have the values needed to order the result slice.
	reqProps := props
	addedSortProp := sortKey != "" && !containsStr(props, sortKey)
	if addedSortProp {
		extra := make([]string, len(props)+1)
		copy(extra, props)
		extra[len(props)] = sortKey
		reqProps = extra
	}

	args := []string{"list", "-j", "-p", "-o", strings.Join(reqProps, ","), "-t", t}
	if t == "snapshot" || t == "bookmark" {
		args = append(args, "-r", "-d1")
	}
	args = append(args, filteredFlags...)
	args = append(args, fullds)

	out, err := DefaultExecCommand(context.Background(), "zfs", args...).Output()
	if err != nil {
		return nil, err
	}

	var result zfsListJSON
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("cannot parse zfs list output: %w", err)
	}

	type entry struct {
		key  string // JSON map key (dataset name), used as sort fallback
		vals map[string]string
	}
	entries := make([]entry, 0, len(result.Datasets))
	for k, ds := range result.Datasets {
		v := make(map[string]string, len(ds.Properties))
		for pname, pval := range ds.Properties {
			v[pname] = pval.Value
		}
		entries = append(entries, entry{key: k, vals: v})
	}

	// Sort by the requested key; fall back to the dataset name for callers
	// that do not specify a sort flag.
	effectiveKey := sortKey
	if effectiveKey == "" {
		effectiveKey = "name"
	}
	sort.SliceStable(entries, func(i, j int) bool {
		vi := entries[i].vals[effectiveKey]
		if vi == "" {
			vi = entries[i].key // fall back to map key if property absent
		}
		vj := entries[j].vals[effectiveKey]
		if vj == "" {
			vj = entries[j].key
		}
		// Numeric comparison for integer properties (createtxg, creation, etc.).
		ni, erri := strconv.ParseInt(vi, 10, 64)
		nj, errj := strconv.ParseInt(vj, 10, 64)
		if erri == nil && errj == nil {
			if descending {
				return ni > nj
			}
			return ni < nj
		}
		if descending {
			return vi > vj
		}
		return vi < vj
	})

	// Build result rows using only the originally-requested properties.
	rows := make([][]string, len(entries))
	for i, e := range entries {
		row := make([]string, len(props))
		for j, p := range props {
			row[j] = e.vals[p]
		}
		rows[i] = row
	}
	return rows, nil
}

// ZpoolList runs "zpool list -j -p" for the given properties, returning rows
// sorted by pool name. Analogous to ZfsList for the zpool(8) command.
func ZpoolList(props []string) ([][]string, error) {
	args := []string{"list", "-j", "-p", "-o", strings.Join(props, ",")}
	out, err := DefaultExecCommand(context.Background(), "zpool", args...).Output()
	if err != nil {
		return nil, err
	}

	var result zpoolListJSON
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("cannot parse zpool list output: %w", err)
	}

	type entry struct {
		name  string
		props map[string]string
	}
	entries := make([]entry, 0, len(result.Pools))
	for name, pool := range result.Pools {
		p := make(map[string]string, len(pool.Properties))
		for k, v := range pool.Properties {
			p[k] = v.Value
		}
		if p["name"] == "" {
			p["name"] = name // fall back to map key
		}
		entries = append(entries, entry{name: name, props: p})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].name < entries[j].name
	})

	rows := make([][]string, len(entries))
	for i, e := range entries {
		row := make([]string, len(props))
		for j, prop := range props {
			row[j] = e.props[prop]
		}
		rows[i] = row
	}
	return rows, nil
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

// containsStr reports whether s appears in the slice.
func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
