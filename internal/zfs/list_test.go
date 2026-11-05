package zfs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestZfsListBreaksSortTiesByName checks that rows sharing the sort key come
// back in name order on every call, although zfs list -j returns an unordered
// JSON object. A snapshot and its bookmarks always share a createtxg.
func TestZfsListBreaksSortTiesByName(t *testing.T) {
	if os.Getenv("ZFSBACKUP_ZFSLIST_TIE_TEST") == "1" {
		for range 20 {
			rows, err := ZfsList([]string{"name"}, "snapshot,bookmark", "tank", "-s", "createtxg")
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, row := range rows {
				names = append(names, row[0])
			}
			if got, want := strings.Join(names, " "), "tank#snap-peer tank@snap tank@later"; got != want {
				t.Fatalf("got order %q; want %q", got, want)
			}
		}
		return
	}
	dir := t.TempDir()
	payload := `{"datasets":{` +
		`"tank@later":{"properties":{"createtxg":{"value":"20"}}},` +
		`"tank@snap":{"properties":{"createtxg":{"value":"10"}}},` +
		`"tank#snap-peer":{"properties":{"createtxg":{"value":"10"}}}}}`
	if err := os.WriteFile(filepath.Join(dir, "zfs"), []byte("#!/bin/sh\nprintf '%s' '"+payload+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestZfsListBreaksSortTiesByName$")
	cmd.Env = append(os.Environ(), "ZFSBACKUP_ZFSLIST_TIE_TEST=1", "PATH="+dir+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("zfs list order is not deterministic: %v\n%s", err, out)
	}
}
