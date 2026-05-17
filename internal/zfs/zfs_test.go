package zfs

import (
	"testing"
)

func TestIsValidZFSDataset_valid(t *testing.T) {
	cases := []string{
		"tank",
		"tank/data",
		"zroot/ROOT",
		"pool/a/b/c",
		"a:b",
		"a.b",
		"pool123",
		"POOL",
		"tank/data/sub",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if err := IsValidZFSDataset(name); err != nil {
				t.Errorf("IsValidZFSDataset(%q) = %v; want nil", name, err)
			}
		})
	}
}

func TestIsValidZFSDataset_invalid(t *testing.T) {
	cases := []string{
		"",
		"tank/",
		"/tank",
		"tank//data",
		"tank with space",
		"tank\tdata",
		"tank@snap",
		"tank#bookmark",
		// Dot and double-dot are valid chars in the regex but invalid as
		// standalone path components (they would be path traversal).
		".",
		"..",
		"tank/.",
		"tank/..",
		"tank/./data",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if err := IsValidZFSDataset(name); err == nil {
				t.Errorf("IsValidZFSDataset(%q) = nil; want error", name)
			}
		})
	}
}

func TestPoolName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"tank", "tank"},
		{"tank/a/b", "tank"},
		{"zroot/ROOT/default", "zroot"},
		{"pool", "pool"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := PoolName(tc.input)
			if got != tc.want {
				t.Errorf("PoolName(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestFSName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"tank@snap", "tank"},
		{"tank#bm", "tank"},
		{"tank/data@snap-2024", "tank/data"},
		{"tank/data#bm-name", "tank/data"},
		{"tank/data", "tank/data"},
		{"tank", "tank"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := FSName(tc.input)
			if got != tc.want {
				t.Errorf("FSName(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSnapshotName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"tank@snap-2024", "snap-2024"},
		{"tank/data@daily-2024-01-01", "daily-2024-01-01"},
		{"pool/a/b@s", "s"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := SnapshotName(tc.input)
			if got != tc.want {
				t.Errorf("SnapshotName(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestBookmarkName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"tank#bm-name", "bm-name"},
		{"tank/data#my-bookmark", "my-bookmark"},
		{"pool/a#x", "x"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := BookmarkName(tc.input)
			if got != tc.want {
				t.Errorf("BookmarkName(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseTabular_echoOutput(t *testing.T) {
	// Use "printf" with known tab-separated output to test ParseTabular without ZFS.
	// We use echo via /bin/sh so this is portable on Linux/macOS.
	rows, err := ParseTabular("printf", []string{"a\tb\tc\n1\t2\t3\n"})
	if err != nil {
		t.Fatalf("ParseTabular error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows; want 2", len(rows))
	}
	want0 := []string{"a", "b", "c"}
	want1 := []string{"1", "2", "3"}
	for i, w := range want0 {
		if rows[0][i] != w {
			t.Errorf("rows[0][%d] = %q; want %q", i, rows[0][i], w)
		}
	}
	for i, w := range want1 {
		if rows[1][i] != w {
			t.Errorf("rows[1][%d] = %q; want %q", i, rows[1][i], w)
		}
	}
}

func TestExtractProps_topLevelAndProperties(t *testing.T) {
	// 'zfs list -j' places "name", "pool", "type" at the top level of each
	// dataset object and puts everything requested via -o that is a real ZFS
	// property (creation, used, ...) inside the "properties" subobject.
	raw := []byte(`{
		"name": "tank/data",
		"pool": "tank",
		"type": "FILESYSTEM",
		"createtxg": "42",
		"properties": {
			"creation": {"value": "1700000000"},
			"used": {"value": "1024"}
		}
	}`)
	got, err := extractProps(raw)
	if err != nil {
		t.Fatalf("extractProps error: %v", err)
	}
	want := map[string]string{
		"name":      "tank/data",
		"pool":      "tank",
		"type":      "FILESYSTEM",
		"createtxg": "42",
		"creation":  "1700000000",
		"used":      "1024",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("extractProps[%q] = %q; want %q", k, got[k], v)
		}
	}
}

func TestExtractProps_propertiesWinOverTopLevel(t *testing.T) {
	// If the same key appears at both levels (rare, but documented to be
	// possible in some ZFS versions), the properties block is authoritative.
	raw := []byte(`{
		"name": "tank/data",
		"properties": {
			"name": {"value": "tank/data-from-props"}
		}
	}`)
	got, err := extractProps(raw)
	if err != nil {
		t.Fatalf("extractProps error: %v", err)
	}
	if got["name"] != "tank/data-from-props" {
		t.Errorf("got name=%q; want properties value", got["name"])
	}
}

func TestExtractProps_skipsNonScalarTopLevel(t *testing.T) {
	// Some ZFS versions add nested objects at the top level (e.g. "dataset_options").
	// Those must not crash extraction or leak into the flat result.
	raw := []byte(`{
		"name": "tank",
		"dataset_options": {"foo": "bar"}
	}`)
	got, err := extractProps(raw)
	if err != nil {
		t.Fatalf("extractProps error: %v", err)
	}
	if _, ok := got["dataset_options"]; ok {
		t.Errorf("non-scalar top-level field should not appear in extracted map: %v", got)
	}
	if got["name"] != "tank" {
		t.Errorf("got name=%q; want \"tank\"", got["name"])
	}
}

func TestParseTabular_emptyOutput(t *testing.T) {
	rows, err := ParseTabular("printf", []string{""})
	if err != nil {
		t.Fatalf("ParseTabular error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows for empty output, got %d", len(rows))
	}
}
