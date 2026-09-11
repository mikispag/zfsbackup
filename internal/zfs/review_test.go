package zfs

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFindExecutablePathAbsolute(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf ok"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, ok := findExecutablePath(bin); !ok || got != bin {
		t.Fatalf("findExecutablePath = %q, %v; want %q, true", got, ok, bin)
	}
}

func TestParseTabularBatchedIncludesFinalBatch(t *testing.T) {
	for _, size := range []int{3, 40000} {
		a := strings.Repeat("a", size)
		want := [][]string{{a}, {a}, {"last"}}
		rows, err := ParseTabularBatched("printf", []string{"%s\n"}, []string{a, a, "last"})
		if err != nil || !reflect.DeepEqual(rows, want) {
			t.Errorf("dataset length %d: got %d rows, error %v; want all %d rows", size, len(rows), err, len(want))
		}
	}
}

func TestDefaultExecCommandMissingExecutable(t *testing.T) {
	err := DefaultExecCommand(context.Background(), filepath.Join(t.TempDir(), "missing")).Run()
	if err == nil {
		t.Fatal("expected an ordinary error for a missing executable")
	}
}

func fakeBookmarkZFS(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "zfs")
	script := `#!/bin/sh
case "$1" in
bookmark) printf '%s\n' "$@" > "$TEST_ZFS_CALL"; exit "$TEST_ZFS_BOOKMARK_EXIT" ;;
list) case "$*" in
  *missing*) exit 1 ;;
  *"-t snapshot"*) printf '%s\n' "$TEST_ZFS_SNAPSHOTS" ;;
  *) printf '%s\n' "$TEST_ZFS_LIST" ;;
esac ;;
get) printf '%s\n' "$TEST_ZFS_GUID" ;;
destroy) printf '%s\n' "$2" >> "$TEST_ZFS_CALL" ;;
*) exit 2 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	old, existed := execPathCache.Load("zfs")
	execPathCache.Store("zfs", bin)
	t.Cleanup(func() {
		if existed {
			execPathCache.Store("zfs", old)
		} else {
			execPathCache.Delete("zfs")
		}
	})
	call := filepath.Join(dir, "call")
	t.Setenv("TEST_ZFS_CALL", call)
	t.Setenv("TEST_ZFS_BOOKMARK_EXIT", "0")
	t.Setenv("TEST_ZFS_LIST", `{"datasets":{}}`)
	t.Setenv("TEST_ZFS_SNAPSHOTS", `{"datasets":{}}`)
	t.Setenv("TEST_ZFS_GUID", "42")
	return call
}

func TestBookmarkIdempotenceRequiresMatchingGUID(t *testing.T) {
	fakeBookmarkZFS(t)
	t.Setenv("TEST_ZFS_BOOKMARK_EXIT", "1")
	for _, guid := range []string{"42", "43"} {
		t.Setenv("TEST_ZFS_LIST", `{"datasets":{"tank/data#snap-dst":{"properties":{"guid":{"value":"`+guid+`"}}}}}`)
		err := zfsBookmarkIdempotent("tank/data@snap", "#snap-dst")
		if (err == nil) != (guid == "42") {
			t.Errorf("existing GUID %s: error = %v", guid, err)
		}
	}
}

func TestPlaceholderFromBookmark(t *testing.T) {
	call := fakeBookmarkZFS(t)
	for _, src := range []string{"tank/data#snap", "tank/data#snap-dst"} {
		if err := ZfsSetPlaceholder(src, "dst"); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(call)
		if err != nil || string(got) != "bookmark\n"+src+"\n#snap-dst\n" {
			t.Errorf("bookmark call = %q, error = %v", got, err)
		}
	}
}

func TestInvalidPlaceholderInputs(t *testing.T) {
	for _, src := range []string{"tank", "tank@snap/child", "tank/../data@snap", "tank@snap#bad", "@snap", "tank@"} {
		if err := ZfsSetPlaceholder(src, "dst"); err == nil {
			t.Errorf("accepted invalid source %q", src)
		}
	}
	for _, suffix := range []string{"", "two-parts", "../dst", "dst@bad", ".", ".."} {
		if err := ZfsSetBeforeBookmark("tank@snap", suffix); err == nil {
			t.Errorf("accepted invalid suffix %q", suffix)
		}
	}
}

func TestPlaceholderGCReplacesOwnedCheckpoint(t *testing.T) {
	call := fakeBookmarkZFS(t)
	t.Setenv("TEST_ZFS_SNAPSHOTS", `{"datasets":{"tank/data@old":{},"tank/data@new":{}}}`)
	t.Setenv("TEST_ZFS_LIST", `{"datasets":{
		"tank/data#old-dst":{}, "tank/data#new-before-dst":{},
		"tank/data#new-dst":{}, "tank/data#old-peer":{}
	}}`)
	if err := ZfsSetPlaceholder("tank/data@new", "dst"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(call)
	if err != nil {
		t.Fatal(err)
	}
	want := "bookmark\ntank/data@new\n#new-dst\ntank/data#new-before-dst\ntank/data#old-dst\n"
	if string(got) != want {
		t.Errorf("commands = %q; want %q (snapshots and peer bookmark retained)", got, want)
	}
}
