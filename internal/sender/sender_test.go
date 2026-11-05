package sender

import (
	"regexp"
	"strings"
	"testing"

	"github.com/mikispag/zfsbackup/internal/config"
)

// --- autoPlaceholderName ---

func TestAutoPlaceholderName_stable(t *testing.T) {
	a := autoPlaceholderName("ssh backup@host -- ")
	b := autoPlaceholderName("ssh backup@host -- ")
	if a != b {
		t.Errorf("same input produced different names: %q vs %q", a, b)
	}
}

func TestAutoPlaceholderName_whitespaceNormalized(t *testing.T) {
	a := autoPlaceholderName("ssh  backup@host  --  ")
	b := autoPlaceholderName("ssh backup@host -- ")
	if a != b {
		t.Errorf("whitespace variants should produce the same name: %q vs %q", a, b)
	}
}

func TestAutoPlaceholderName_differentReceivers_differentNames(t *testing.T) {
	a := autoPlaceholderName("ssh backup@host1 -- ")
	b := autoPlaceholderName("ssh backup@host2 -- ")
	if a == b {
		t.Errorf("different receivers produced the same name %q", a)
	}
}

func TestAutoPlaceholderName_formatNoHyphens(t *testing.T) {
	// zfsGcPlaceholder splits on "-" and takes the last component; the name
	// must be a single hyphen-free token to be extracted correctly.
	name := autoPlaceholderName("ssh backup@host -- ")
	if matched, _ := regexp.MatchString(`^[a-z0-9]+$`, name); !matched {
		t.Errorf("placeholder name %q contains hyphens or non-alphanumeric chars", name)
	}
}

// --- compressionType ---

func TestCompressionType_empty_returnsNone(t *testing.T) {
	if got := compressionType(&config.DestinationConfig{}); got != "none" {
		t.Errorf("got %q; want \"none\"", got)
	}
}

func TestCompressionType_zstd_returnsZstd(t *testing.T) {
	if got := compressionType(&config.DestinationConfig{Compression: "zstd"}); got != "zstd" {
		t.Errorf("got %q; want \"zstd\"", got)
	}
}

func TestCompressionType_none_returnsNone(t *testing.T) {
	if got := compressionType(&config.DestinationConfig{Compression: "none"}); got != "none" {
		t.Errorf("got %q; want \"none\"", got)
	}
}

// --- PotentialSnapsToSend ---

func makeFSP(snapshotRe string, sources []source) *fsProcessor {
	job := &config.SenderConfig{}
	if snapshotRe != "" {
		job.SnapshotRegex = snapshotRe
	}
	fsp := NewFSProcessor("tank", job, &config.DestinationConfig{})
	fsp.sources = sources
	return fsp
}

func TestPotentialSnapsToSend_onlySnapshotsReturned(t *testing.T) {
	sources := []source{
		{Name: "tank@daily-2024", GUID: "1"},
		{Name: "tank#daily-2024", GUID: "1"},
	}
	fsp := makeFSP(".*", sources)
	got := fsp.PotentialSnapsToSend()
	if len(got) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(got))
	}
	if !got[0].isSnapshot() {
		t.Errorf("expected a snapshot, got %q", got[0].Name)
	}
}

func TestPotentialSnapsToSend_regexpFilter(t *testing.T) {
	sources := []source{
		{Name: "tank@daily-2024-01-01", GUID: "1"},
		{Name: "tank@hourly-2024-01-01", GUID: "2"},
		{Name: "tank@daily-2024-01-02", GUID: "3"},
	}
	fsp := makeFSP("daily-.*", sources)
	got := fsp.PotentialSnapsToSend()
	if len(got) != 2 {
		t.Fatalf("expected 2 daily snapshots, got %d", len(got))
	}
	for _, s := range got {
		if s.GUID == "2" {
			t.Errorf("hourly snapshot should not be in result")
		}
	}
}

func TestPotentialSnapsToSend_emptyReMatchesAll(t *testing.T) {
	sources := []source{
		{Name: "tank@snap1", GUID: "1"},
		{Name: "tank@snap2", GUID: "2"},
		{Name: "tank@snap3", GUID: "3"},
	}
	// empty SnapshotRegex defaults to ".*"
	fsp := makeFSP("", sources)
	got := fsp.PotentialSnapsToSend()
	if len(got) != 3 {
		t.Errorf("empty re should match all snapshots; got %d", len(got))
	}
}

func TestPotentialSnapsToSend_noMatch(t *testing.T) {
	sources := []source{
		{Name: "tank@daily-2024", GUID: "1"},
	}
	fsp := makeFSP("weekly-.*", sources)
	got := fsp.PotentialSnapsToSend()
	if len(got) != 0 {
		t.Errorf("expected 0 matches, got %d", len(got))
	}
}

// --- baseFor ---

func TestBaseFor_prefersSnapshotOverBookmarkWithSameGUID(t *testing.T) {
	fsp := makeFSP("", []source{
		{Name: "tank#daily-02-peer", GUID: "2"},
		{Name: "tank@daily-02", GUID: "2"},
		{Name: "tank@daily-03", GUID: "3"},
	})
	if base, ok := fsp.baseFor("2"); !ok || base.Name != "tank@daily-02" {
		t.Errorf("baseFor(2) = %v, %v; want the snapshot tank@daily-02", base, ok)
	}
	fsp.sources = fsp.sources[:1]
	if base, ok := fsp.baseFor("2"); !ok || base.Name != "tank#daily-02-peer" {
		t.Errorf("baseFor(2) without the snapshot = %v, %v; want the bookmark", base, ok)
	}
	if base, ok := fsp.baseFor("9"); ok {
		t.Errorf("baseFor(9) = %v; want no base", base)
	}
}

// --- incrementalArgs ---

func TestIncrementalArgs(t *testing.T) {
	for _, tc := range []struct {
		base source
		want string
	}{
		{source{}, ""},
		{source{Name: "tank@snap1", GUID: "1"}, "-i tank@snap1"},
		{source{Name: "tank#bm1", GUID: "1"}, "-i tank#bm1"},
	} {
		if got := strings.Join(incrementalArgs(tc.base), " "); got != tc.want {
			t.Errorf("incrementalArgs(%v) = %q; want %q", tc.base, got, tc.want)
		}
	}
}

// --- ActualSnapsToSend ---

func makeConfig(re string, intermediate bool, syncPlaceholders []string) (*config.SenderConfig, *config.DestinationConfig) {
	job := &config.SenderConfig{}
	if re != "" {
		job.SnapshotRegex = re
	}
	job.SendIntermediate = intermediate
	dst := &config.DestinationConfig{SyncPlaceholders: syncPlaceholders}
	return job, dst
}

// makeActualFSP builds an fsProcessor via NewFSProcessor (which compiles snapRe)
// then injects sources for ActualSnapsToSend tests.
func makeActualFSP(job *config.SenderConfig, dst *config.DestinationConfig, sources []source) *fsProcessor {
	fsp := NewFSProcessor("tank", job, dst)
	fsp.sources = sources
	return fsp
}

func TestActualSnapsToSend_noIncrementalBase_picksMostRecent(t *testing.T) {
	sources := []source{
		{Name: "tank@daily-01", GUID: "1"},
		{Name: "tank@daily-02", GUID: "2"},
		{Name: "tank@daily-03", GUID: "3"},
	}
	job, dst := makeConfig("daily-.*", false, nil)
	fsp := makeActualFSP(job, dst, sources)
	got := fsp.ActualSnapsToSend(source{})
	if len(got) != 1 {
		t.Fatalf("expected 1 snap, got %d: %v", len(got), got)
	}
	if got[0].GUID != "3" {
		t.Errorf("expected most recent snap (GUID=3), got %q", got[0].GUID)
	}
}

func TestActualSnapsToSend_sendIntermediateSnaps_sendsAll(t *testing.T) {
	sources := []source{
		{Name: "tank@daily-01", GUID: "1"},
		{Name: "tank@daily-02", GUID: "2"},
		{Name: "tank@daily-03", GUID: "3"},
	}
	job, dst := makeConfig("daily-.*", true, nil)
	fsp := makeActualFSP(job, dst, sources)
	got := fsp.ActualSnapsToSend(source{})
	if len(got) != 3 {
		t.Errorf("expected 3 snaps with send_intermediate, got %d", len(got))
	}
}

func TestActualSnapsToSend_noMatchingSnaps_returnsEmpty(t *testing.T) {
	sources := []source{{Name: "tank@hourly-01", GUID: "1"}}
	job, dst := makeConfig("daily-.*", false, nil)
	fsp := makeActualFSP(job, dst, sources)
	if got := fsp.ActualSnapsToSend(source{}); len(got) != 0 {
		t.Errorf("expected 0 snaps for non-matching re, got %d", len(got))
	}
}

func TestActualSnapsToSend_withIncrementalBase_skipsAlreadySentSnaps(t *testing.T) {
	sources := []source{
		{Name: "tank@daily-01", GUID: "1"},
		{Name: "tank@daily-02", GUID: "2"},
		{Name: "tank@daily-03", GUID: "3"},
	}
	base := source{Name: "tank@daily-02", GUID: "2"}
	job, dst := makeConfig("daily-.*", false, nil)
	fsp := makeActualFSP(job, dst, sources)
	got := fsp.ActualSnapsToSend(base)
	if len(got) != 1 {
		t.Fatalf("expected 1 snap after incremental base, got %d", len(got))
	}
	if got[0].GUID != "3" {
		t.Errorf("expected GUID=3, got %q", got[0].GUID)
	}
}

func TestActualSnapsToSend_bookmarkBase_skipsItsSnapshot(t *testing.T) {
	// The base is a placeholder bookmark; the snapshot it was created from has
	// the same GUID and must not be sent again, whichever is listed first.
	sources := []source{
		{Name: "tank@daily-01", GUID: "1"},
		{Name: "tank#daily-02-peer", GUID: "2"},
		{Name: "tank@daily-02", GUID: "2"},
		{Name: "tank@daily-03", GUID: "3"},
	}
	job, dst := makeConfig("daily-.*", true, nil)
	fsp := makeActualFSP(job, dst, sources)
	got := fsp.ActualSnapsToSend(source{Name: "tank#daily-02-peer", GUID: "2"})
	if len(got) != 1 || got[0].GUID != "3" {
		t.Fatalf("expected only GUID=3 after a bookmark base, got %v", got)
	}
}

func TestActualSnapsToSend_placeholderGUID_includesMandatorySnap(t *testing.T) {
	sources := []source{
		{Name: "tank@daily-01", GUID: "1"},
		{Name: "tank@daily-02", GUID: "2"},
		{Name: "tank@daily-03", GUID: "3"},
		{Name: "tank#daily-02-mypeer", GUID: "2"},
	}
	job, dst := makeConfig("daily-.*", false, []string{"mypeer"})
	fsp := makeActualFSP(job, dst, sources)
	got := fsp.ActualSnapsToSend(source{})
	// mandatory should include daily-02 (placeholder) and daily-03 (most recent)
	guids := make(map[string]bool)
	for _, s := range got {
		guids[s.GUID] = true
	}
	if !guids["2"] {
		t.Error("expected GUID=2 (placeholder snap) to be in mandatory")
	}
	if !guids["3"] {
		t.Error("expected GUID=3 (most recent) to be in mandatory")
	}
}

func TestActualSnapsToSend_placeholderNewerThanNewestMatching_preservesOrder(t *testing.T) {
	// daily-02 matches the regex; foo-03 (newer in source order, i.e. higher
	// createtxg) is targeted by a sync placeholder but does not match the regex.
	// The result must list snapshots in createtxg order — daily-02 before foo-03 —
	// so each incremental send uses a valid earlier base.
	sources := []source{
		{Name: "tank@daily-01", GUID: "1"},
		{Name: "tank@daily-02", GUID: "2"},
		{Name: "tank@foo-03", GUID: "3"},
		{Name: "tank#foo-03-mypeer", GUID: "3"},
	}
	job, dst := makeConfig("daily-.*", false, []string{"mypeer"})
	fsp := makeActualFSP(job, dst, sources)
	got := fsp.ActualSnapsToSend(source{})
	if len(got) != 2 {
		t.Fatalf("expected 2 snaps, got %d: %v", len(got), got)
	}
	if got[0].GUID != "2" || got[1].GUID != "3" {
		t.Errorf("expected order [GUID=2, GUID=3] (createtxg-sorted), got [%q, %q]",
			got[0].GUID, got[1].GUID)
	}
}

func TestActualSnapsToSend_placeholderGUIDIsAlsoMostRecent_noDuplicate(t *testing.T) {
	// GUID=2 is both the placeholder target and the most-recent matching snapshot.
	// It must appear exactly once in the result.
	sources := []source{
		{Name: "tank@daily-01", GUID: "1"},
		{Name: "tank@daily-02", GUID: "2"},
		{Name: "tank#daily-02-mypeer", GUID: "2"},
	}
	job, dst := makeConfig("daily-.*", false, []string{"mypeer"})
	fsp := makeActualFSP(job, dst, sources)
	got := fsp.ActualSnapsToSend(source{})
	count := 0
	for _, s := range got {
		if s.GUID == "2" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("GUID=2 (placeholder and most-recent) appears %d times in result; want exactly 1", count)
	}
}
