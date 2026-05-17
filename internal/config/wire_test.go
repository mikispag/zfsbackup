package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// These tests guard against field renames breaking the sender↔receiver JSON
// wire protocol. The field names are the public contract between the two
// binaries; a rename without updating both sides silently drops data.

func TestIncrementalSuggestions_sendFull_marshalsCorrectly(t *testing.T) {
	is := IncrementalSuggestions{SendFull: true}
	b, err := json.Marshal(is)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if !strings.Contains(string(b), `"send_full":true`) {
		t.Errorf("expected send_full:true in %s", b)
	}
}

func TestIncrementalSuggestions_resumeToken_marshalsCorrectly(t *testing.T) {
	is := IncrementalSuggestions{ResumeToken: "abc123"}
	b, _ := json.Marshal(is)
	if !strings.Contains(string(b), `"resume_token":"abc123"`) {
		t.Errorf("expected resume_token field in %s", b)
	}
}

func TestIncrementalSuggestions_lastSnapshotAndGUID_marshalsCorrectly(t *testing.T) {
	is := IncrementalSuggestions{LastSnapshot: "snap-2024-01-01", GUID: "deadbeef"}
	b, _ := json.Marshal(is)
	s := string(b)
	if !strings.Contains(s, `"last_snapshot":"snap-2024-01-01"`) {
		t.Errorf("expected last_snapshot field in %s", s)
	}
	if !strings.Contains(s, `"guid":"deadbeef"`) {
		t.Errorf("expected guid field in %s", s)
	}
}

func TestIncrementalSuggestions_emptyFields_omitted(t *testing.T) {
	// Empty/zero fields must not appear in JSON (omitempty) so the receiver
	// can distinguish "send_full not set" from "send_full: false".
	is := IncrementalSuggestions{}
	b, _ := json.Marshal(is)
	if string(b) != "{}" {
		t.Errorf("empty IncrementalSuggestions should marshal to {}; got %s", b)
	}
}

func TestIncrementalSuggestions_roundTrip(t *testing.T) {
	orig := IncrementalSuggestions{LastSnapshot: "snap-2024-01-01_12-00-00", GUID: "cafebabe"}
	b, _ := json.Marshal(orig)
	var got IncrementalSuggestions
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if got.LastSnapshot != orig.LastSnapshot || got.GUID != orig.GUID {
		t.Errorf("round-trip mismatch: got %+v; want %+v", got, orig)
	}
}

func TestSetPlaceholders_roundTrip(t *testing.T) {
	orig := SetPlaceholders{
		FS: "tank/data",
		Placeholders: []PlaceholderEntry{
			{Name: "dst1a2b3c4d", GUID: "deadbeef"},
			{Name: "dst99887766", GUID: "cafebabe"},
		},
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var got SetPlaceholders
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if got.FS != orig.FS {
		t.Errorf("FS: got %q; want %q", got.FS, orig.FS)
	}
	if len(got.Placeholders) != 2 {
		t.Fatalf("Placeholders len: got %d; want 2", len(got.Placeholders))
	}
	if got.Placeholders[0].Name != "dst1a2b3c4d" || got.Placeholders[0].GUID != "deadbeef" {
		t.Errorf("Placeholders[0]: got %+v", got.Placeholders[0])
	}
}

func TestSetPlaceholders_FSFieldName(t *testing.T) {
	sp := SetPlaceholders{FS: "tank/data"}
	b, _ := json.Marshal(sp)
	if !strings.Contains(string(b), `"fs":"tank/data"`) {
		t.Errorf("expected fs field in %s", b)
	}
}

func TestPlaceholderEntry_fieldNames(t *testing.T) {
	p := PlaceholderEntry{Name: "dstaabbccdd", GUID: "112233"}
	b, _ := json.Marshal(p)
	s := string(b)
	if !strings.Contains(s, `"name":"dstaabbccdd"`) {
		t.Errorf("expected name field in %s", s)
	}
	if !strings.Contains(s, `"guid":"112233"`) {
		t.Errorf("expected guid field in %s", s)
	}
}
