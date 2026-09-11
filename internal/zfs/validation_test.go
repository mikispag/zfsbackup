package zfs

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestDatasetOperandCannotBeAnOption(t *testing.T) {
	for _, name := range []string{"-", "-r", "--help", "-pool/data"} {
		if err := IsValidZFSDataset(name); err == nil {
			t.Errorf("accepted option-like dataset %q", name)
		}
	}
	if err := IsValidZFSDataset("tank/-child"); err != nil {
		t.Errorf("safe child component rejected: %v", err)
	}
}

func TestDiscoveryReturnsOtherRootsOnFailure(t *testing.T) {
	fakeBookmarkZFS(t)
	t.Setenv("TEST_ZFS_LIST", `{"datasets":{"tank":{},"tank/data":{},"tank/data/private":{},"tank/database":{}}}`)
	got, err := ExpandFsToProcess([]string{"missing", "tank", "tank"}, []string{"tank/data"})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("missing root error = %v", err)
	}
	if !slices.Equal(got, []string{"tank", "tank/database"}) {
		t.Errorf("discovered filesystems = %v", got)
	}
}

func TestDiscoveryRejectsInvalidFiltersBeforeListing(t *testing.T) {
	for _, filters := range [][2][]string{
		{{"-r"}, nil}, {{"tank"}, {"tank/../data"}}, {nil, {"-r"}},
	} {
		got, err := ExpandFsToProcess(filters[0], filters[1])
		if err == nil || len(got) != 0 {
			t.Errorf("invalid filters %v: filesystems=%v, error=%v", filters, got, err)
		}
	}
}

func TestZFSWrappersRejectInvalidDatasetOperands(t *testing.T) {
	for _, target := range []string{"-r", "tank/../data"} {
		if err := ZfsCreate(target, true); err == nil {
			t.Errorf("create accepted %q", target)
		}
		if _, err := ZfsGet(target+"@snap", "guid"); err == nil {
			t.Errorf("get accepted %q", target)
		}
		if err := ZfsDestroy(target + "@a,b"); err == nil {
			t.Errorf("destroy accepted %q", target)
		}
		if _, err := ZfsList([]string{"name"}, "filesystem", target); err == nil {
			t.Errorf("list accepted %q", target)
		}
	}
}

func TestZFSListRejectsIncompleteJSON(t *testing.T) {
	fakeBookmarkZFS(t)
	for _, payload := range []string{`{}`, `{"datasets":null}`, `{"datasets":{"tank":{}}}`} {
		t.Setenv("TEST_ZFS_LIST", payload)
		if _, err := ZfsList([]string{"name", "guid"}, "filesystem", "tank"); err == nil {
			t.Errorf("accepted incomplete list output %s", payload)
		}
	}
}

func TestLoadConfigRetainsLock(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(filePath, []byte(`{"value":7}`), 0600); err != nil {
		t.Fatal(err)
	}
	var cfg struct{ Value int }
	lock := LoadConfig(filePath, &cfg)
	defer lock.Close()
	if cfg.Value != 7 {
		t.Fatalf("decoded value = %d", cfg.Value)
	}
	probe, err := os.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("another invocation acquired config lock before work completed: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("config lock not released after work completed: %v", err)
	}
}
