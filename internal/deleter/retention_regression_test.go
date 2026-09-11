package deleter

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/mikispag/zfsbackup/internal/config"
)

func TestRetentionKeepsNewestInExactlyCountBuckets(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	dfp := &deleteFsProcessor{
		cfg: &config.DeleterConfig{Rules: []config.RetentionRule{{Interval: "1h", Count: 2}}},
		snaps: []snapshot{
			makeSnap("outside", 0, base),
			makeSnap("older_previous", 3600, base),
			makeSnap("newer_previous", 5400, base),
			makeSnap("older_current", 6000, base),
			makeSnap("latest", 9000, base),
		},
	}
	deleted, err := dfp.markForPreservation()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"outside", "older_previous", "older_current"}; !slices.Equal(deleted, want) {
		t.Fatalf("deleted %v, want %v", deleted, want)
	}
}

func TestRetentionRejectsInvalidSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.DeleterConfig
	}{
		{"zero interval", config.DeleterConfig{Rules: []config.RetentionRule{{Interval: "0s", Count: 2}}}},
		{"negative interval", config.DeleterConfig{Rules: []config.RetentionRule{{Interval: "-1h", Count: 2}}}},
		{"zero count", config.DeleterConfig{Rules: []config.RetentionRule{{Interval: "1h", Count: 0}}}},
		{"negative count", config.DeleterConfig{Rules: []config.RetentionRule{{Interval: "1h", Count: -2}}}},
		{"negative top n", config.DeleterConfig{PreserveTopN: -1}},
		{"negative newer than", config.DeleterConfig{PreserveNewerThan: "-1h"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("retention panicked: %v", p)
				}
			}()
			dfp := &deleteFsProcessor{cfg: &tc.cfg, snaps: []snapshot{makeSnap("latest", 0, time.Unix(0, 0))}}
			if deleted, err := dfp.markForPreservation(); err == nil || len(deleted) != 0 {
				t.Fatalf("got deletions %v, error %v; want validation error and no deletions", deleted, err)
			}
		})
	}
}

func TestRetentionLargeCountDoesNotAllocateBuckets(t *testing.T) {
	defer func() {
		if p := recover(); p != nil {
			t.Errorf("retention panicked: %v", p)
		}
	}()
	dfp := &deleteFsProcessor{
		cfg:   &config.DeleterConfig{Rules: []config.RetentionRule{{Interval: "1h", Count: math.MaxInt64}}},
		snaps: []snapshot{makeSnap("latest", 0, time.Unix(0, 0))},
	}
	if deleted, err := dfp.markForPreservation(); err != nil || len(deleted) != 0 {
		t.Fatalf("got deletions %v, error %v; want latest preserved", deleted, err)
	}
}

func TestRetentionHoleBeforeOldestRetainedBucket(t *testing.T) {
	dfp := &deleteFsProcessor{
		cfg: &config.DeleterConfig{Rules: []config.RetentionRule{{Interval: "1h", Count: 3}}},
		snaps: []snapshot{
			makeSnap("older", 0, time.Unix(0, 0)),
			makeSnap("latest", 7200, time.Unix(0, 0)),
		},
	}
	if _, err := dfp.markForPreservation(); err == nil {
		t.Fatal("expected a gap error before bucket 2")
	}
}
