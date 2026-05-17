package deleter

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/mikispag/zfsbackup/internal/config"
)

// makeSnap builds a snapshot with a timestamp offset from base.
// txg is set to secondsFromBase+1 to keep it strictly increasing.
func makeSnap(name string, secondsFromBase int64, base time.Time) snapshot {
	var txg big.Int
	txg.SetInt64(secondsFromBase + 1)
	return snapshot{
		Name:      name,
		Timestamp: base.Add(time.Duration(secondsFromBase) * time.Second),
		CreateTxg: txg,
	}
}

// preservedNames returns the names of all preserved snapshots.
func preservedNames(snaps []snapshot) []string {
	var names []string
	for _, s := range snaps {
		if s.preserve {
			names = append(names, s.Name)
		}
	}
	return names
}

// deletedNames returns the names of all non-preserved snapshots.
func deletedNames(snaps []snapshot) []string {
	var names []string
	for _, s := range snaps {
		if !s.preserve {
			names = append(names, s.Name)
		}
	}
	return names
}

// ─── preserveTopN ────────────────────────────────────────────────────────────

func TestPreserveTopN_topNLessThanLen(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	dfp := &deleteFsProcessor{
		cfg: &config.DeleterConfig{PreserveTopN: 3},
		snaps: []snapshot{
			makeSnap("s1", 1, base),
			makeSnap("s2", 2, base),
			makeSnap("s3", 3, base),
			makeSnap("s4", 4, base),
			makeSnap("s5", 5, base),
		},
	}
	dfp.preserveTopN()
	for i, s := range dfp.snaps {
		want := i >= 2 // last 3 (indices 2,3,4)
		if s.preserve != want {
			t.Errorf("snaps[%d] (%s): preserve=%v want %v", i, s.Name, s.preserve, want)
		}
	}
}

func TestPreserveTopN_zero_nonePreserved(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	dfp := &deleteFsProcessor{
		cfg:   &config.DeleterConfig{PreserveTopN: 0},
		snaps: []snapshot{makeSnap("s1", 1, base), makeSnap("s2", 2, base)},
	}
	dfp.preserveTopN()
	for _, s := range dfp.snaps {
		if s.preserve {
			t.Errorf("snap %q should not be preserved with topN=0", s.Name)
		}
	}
}

func TestPreserveTopN_greaterThanLen_allPreserved(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	dfp := &deleteFsProcessor{
		cfg:   &config.DeleterConfig{PreserveTopN: 10},
		snaps: []snapshot{makeSnap("s1", 1, base), makeSnap("s2", 2, base)},
	}
	dfp.preserveTopN()
	for _, s := range dfp.snaps {
		if !s.preserve {
			t.Errorf("snap %q should be preserved when topN > len(snaps)", s.Name)
		}
	}
}

func TestPreserveTopN_exactlyN_allPreserved(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	dfp := &deleteFsProcessor{
		cfg: &config.DeleterConfig{PreserveTopN: 3},
		snaps: []snapshot{
			makeSnap("s1", 1, base),
			makeSnap("s2", 2, base),
			makeSnap("s3", 3, base),
		},
	}
	dfp.preserveTopN()
	for _, s := range dfp.snaps {
		if !s.preserve {
			t.Errorf("snap %q should be preserved when topN == len(snaps)", s.Name)
		}
	}
}

// ─── preserveNewerThan ───────────────────────────────────────────────────────

func TestPreserveNewerThan_nilSetting_noOp(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	dfp := &deleteFsProcessor{
		cfg:   &config.DeleterConfig{},
		snaps: []snapshot{makeSnap("s1", 1, base)},
		now:   base.Add(time.Hour),
	}
	if err := dfp.preserveNewerThan(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dfp.snaps[0].preserve {
		t.Error("snap should not be preserved when PreserveNewerThan is empty")
	}
}

func TestPreserveNewerThan_newerSnapsPreserved(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	dfp := &deleteFsProcessor{
		cfg: &config.DeleterConfig{PreserveNewerThan: "1h"},
		snaps: []snapshot{
			makeSnap("old", 0, base),
			makeSnap("recent", int64(90*time.Minute/time.Second), base),
		},
		now: base.Add(2 * time.Hour),
	}
	if err := dfp.preserveNewerThan(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dfp.snaps[0].preserve {
		t.Error("'old' snap (2h ago) should not be preserved with 1h threshold")
	}
	if !dfp.snaps[1].preserve {
		t.Error("'recent' snap (30min ago) should be preserved with 1h threshold")
	}
}

func TestPreserveNewerThan_allOlderThanThreshold(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	dfp := &deleteFsProcessor{
		cfg:   &config.DeleterConfig{PreserveNewerThan: "30m"},
		snaps: []snapshot{makeSnap("s1", 0, base), makeSnap("s2", 60, base)},
		now:   base.Add(2 * time.Hour),
	}
	if err := dfp.preserveNewerThan(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, s := range dfp.snaps {
		if s.preserve {
			t.Errorf("snap %q (2h old) should not be preserved with 30m threshold", s.Name)
		}
	}
}

func TestPreserveNewerThan_invalidDuration_returnsError(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	dfp := &deleteFsProcessor{
		cfg:   &config.DeleterConfig{PreserveNewerThan: "notaduration"},
		snaps: []snapshot{makeSnap("s1", 1, base)},
		now:   base.Add(time.Hour),
	}
	if err := dfp.preserveNewerThan(); err == nil {
		t.Error("expected error for invalid duration, got nil")
	}
}

// ─── markForPreservation (interval logic) ────────────────────────────────────

// buildHourlySnaps creates n snapshots, each 1 hour apart ending at `end`.
func buildHourlySnaps(n int, end time.Time) []snapshot {
	snaps := make([]snapshot, n)
	for i := 0; i < n; i++ {
		age := time.Duration(n-1-i) * time.Hour
		snaps[i] = makeSnap("snap"+strings.Repeat("0", 3-len(string(rune('0'+i))))+string(rune('0'+i%10)),
			int64((end.Add(-age)).Unix()-end.Add(-time.Duration(n-1)*time.Hour).Unix()),
			end.Add(-time.Duration(n-1)*time.Hour))
	}
	return snaps
}

func TestMarkForPreservation_recentAndOldSnap_multiIntervalRule(t *testing.T) {
	// old is 7 days ago, recent is ≈ now. With a 3-day interval and Count=4,
	// recent lands in interval 0 and old lands in interval 2; both are preserved.
	now := time.Now()
	base := now.Add(-7 * 24 * time.Hour)
	snaps := []snapshot{
		makeSnap("old", 0, base),                                    // 7 days ago
		makeSnap("recent", int64(7*24*time.Hour/time.Second), base), // ≈ now
	}
	dfp := &deleteFsProcessor{
		fs:    "tank",
		cfg:   &config.DeleterConfig{Rules: []config.RetentionRule{{Interval: "3d", Count: 4, AllowHoles: true}}},
		snaps: snaps,
	}
	_, err := dfp.markForPreservation()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !snaps[1].preserve {
		t.Error("most recent snapshot should be preserved by the 3-day rule")
	}
}

func TestMarkForPreservation_invalidPreserveNewerThan_returnsError(t *testing.T) {
	// Exercises the markForPreservation → preserveNewerThan error path.
	base := time.Unix(1_000_000, 0)
	dfp := &deleteFsProcessor{
		fs:    "tank",
		cfg:   &config.DeleterConfig{PreserveNewerThan: "notaduration"},
		snaps: []snapshot{makeSnap("s1", 0, base)},
	}
	_, err := dfp.markForPreservation()
	if err == nil {
		t.Error("expected error for invalid preserve_newer_than, got nil")
	}
}

func TestMarkForPreservation_snapOutsideRetentionWindow_skipped(t *testing.T) {
	// A snapshot older than the retention window produces intervalIdx >= len(intervals)
	// and must be skipped (not preserved, not an error).
	base := time.Unix(1_000_000, 0)
	snaps := []snapshot{
		makeSnap("ancient", 0, base),                                // way outside window
		makeSnap("recent", int64(2*24*time.Hour/time.Second), base), // inside window
	}
	dfp := &deleteFsProcessor{
		fs:    "tank",
		cfg:   &config.DeleterConfig{Rules: []config.RetentionRule{{Interval: "1h", Count: 2}}},
		snaps: snaps,
	}
	toDelete, err := dfp.markForPreservation()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// "ancient" is outside the 2-hour window and should be deleted.
	found := false
	for _, name := range toDelete {
		if name == "ancient" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'ancient' in toDelete list; got %v", toDelete)
	}
}

func TestMarkForPreservation_emptySnaps_returnsNil(t *testing.T) {
	dfp := &deleteFsProcessor{
		cfg:   &config.DeleterConfig{PreserveTopN: 5},
		snaps: nil,
	}
	toDelete, err := dfp.markForPreservation()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(toDelete) != 0 {
		t.Errorf("empty snaps: got toDelete=%v; want empty", toDelete)
	}
}

func TestMarkForPreservation_topNOnly(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	dfp := &deleteFsProcessor{
		cfg: &config.DeleterConfig{PreserveTopN: 2},
		snaps: []snapshot{
			makeSnap("old", 0, base),
			makeSnap("mid", 100, base),
			makeSnap("new", 200, base),
		},
	}
	toDelete, err := dfp.markForPreservation()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(toDelete) != 1 || toDelete[0] != "old" {
		t.Errorf("got toDelete=%v; want [old]", toDelete)
	}
}

func TestMarkForPreservation_intervalRule_keepsOnePerDay(t *testing.T) {
	// 24 hourly snaps → keep 1 per day for 3 days = 3 preserved.
	// The "newest" snap is used as dfp.now.
	end := time.Unix(1_000_000+23*3600, 0) // absolute, doesn't need to be "now"
	snaps := make([]snapshot, 24)
	for i := 0; i < 24; i++ {
		var txg big.Int
		txg.SetInt64(int64(i + 1))
		snaps[i] = snapshot{
			Name:      "snap" + string(rune('a'+i)),
			Timestamp: end.Add(-time.Duration(23-i) * time.Hour),
			CreateTxg: txg,
		}
	}
	dfp := &deleteFsProcessor{
		fs: "tank",
		cfg: &config.DeleterConfig{
			Rules: []config.RetentionRule{
				{Interval: "24h", Count: 3},
			},
		},
		snaps: snaps,
	}
	toDelete, err := dfp.markForPreservation()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	preserved := 24 - len(toDelete)
	// The rule keeps 1 per day for 3 days; interval 0 (current day) is also filled.
	// Total preserved = intervals that are covered (at most 4 since Count+1 slots).
	if preserved < 1 || preserved > 4 {
		t.Errorf("got %d preserved snaps; want between 1 and 4 for a 3-day daily rule", preserved)
	}
}

func TestMarkForPreservation_holeForbidden_returnsError(t *testing.T) {
	// Create a gap in the middle: snaps at day 0, day 1 (missing), day 2.
	// With allow_holes=false this should error.
	base := time.Unix(0, 0) // epoch — safe arbitrary value
	snaps := []snapshot{
		makeSnap("day0", 0, base),
		// day 1 intentionally missing
		makeSnap("day2", int64(2*24*time.Hour/time.Second), base),
	}
	dfp := &deleteFsProcessor{
		fs: "tank",
		cfg: &config.DeleterConfig{
			Rules: []config.RetentionRule{
				{
					Interval:   "24h",
					Count:      3,
					AllowHoles: false,
				},
			},
		},
		snaps: snaps,
	}
	_, err := dfp.markForPreservation()
	if err == nil {
		t.Error("expected error for hole with allow_holes=false; got nil")
	}
}

func TestMarkForPreservation_holeAllowed_noError(t *testing.T) {
	base := time.Unix(0, 0)
	snaps := []snapshot{
		makeSnap("day0", 0, base),
		makeSnap("day2", int64(2*24*time.Hour/time.Second), base),
	}
	dfp := &deleteFsProcessor{
		fs: "tank",
		cfg: &config.DeleterConfig{
			Rules: []config.RetentionRule{
				{
					Interval:   "24h",
					Count:      3,
					AllowHoles: true,
				},
			},
		},
		snaps: snaps,
	}
	_, err := dfp.markForPreservation()
	if err != nil {
		t.Errorf("expected no error for hole with allow_holes=true; got %v", err)
	}
}

func TestMarkForPreservation_invalidRuleDuration_returnsError(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	dfp := &deleteFsProcessor{
		fs: "tank",
		cfg: &config.DeleterConfig{
			Rules: []config.RetentionRule{
				{Interval: "notaduration", Count: 3},
			},
		},
		snaps: []snapshot{makeSnap("s1", 0, base), makeSnap("s2", 3600, base)},
	}
	_, err := dfp.markForPreservation()
	if err == nil {
		t.Error("expected error for invalid interval duration; got nil")
	}
}

// ─── newDeleteFsProcessor ────────────────────────────────────────────────────

func TestNewDeleteFsProcessor_invalidRegexp_returnsError(t *testing.T) {
	dc := &config.DeleterConfig{
		Regex: []string{"[invalid"},
	}
	_, err := newDeleteFsProcessor(dc, "tank")
	if err == nil {
		t.Error("expected error for invalid regexp, got nil")
	}
	if !strings.Contains(err.Error(), "[invalid") {
		t.Errorf("error should mention the bad pattern; got: %v", err)
	}
}

func TestNewDeleteFsProcessor_validRegexp_noError(t *testing.T) {
	dc := &config.DeleterConfig{
		Regex: []string{"snap-.*"},
	}
	dfp, err := newDeleteFsProcessor(dc, "tank")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dfp.regexps) != 1 {
		t.Errorf("expected 1 compiled regexp, got %d", len(dfp.regexps))
	}
}

// ─── deleteManySnaps ─────────────────────────────────────────────────────────

func TestDeleteManySnaps_nilList_returnsNil(t *testing.T) {
	if err := deleteManySnaps("tank", true, nil); err != nil {
		t.Errorf("nil list: got %v; want nil", err)
	}
}

func TestDeleteManySnaps_emptySlice_returnsNil(t *testing.T) {
	if err := deleteManySnaps("tank", true, []string{}); err != nil {
		t.Errorf("empty slice: got %v; want nil", err)
	}
}
