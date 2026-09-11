package deleter

import (
	"testing"

	"github.com/mikispag/zfsbackup/internal/config"
)

func TestRunRejectsInvalidConfigWithoutDatasets(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.DeleterConfig
	}{
		{"regexp", config.DeleterConfig{Regex: []string{"["}}},
		{"top n", config.DeleterConfig{PreserveTopN: -1}},
		{"newer than", config.DeleterConfig{PreserveNewerThan: "invalid"}},
		{"interval", config.DeleterConfig{Rules: []config.RetentionRule{{Interval: "invalid", Count: 1}}}},
		{"zero interval", config.DeleterConfig{Rules: []config.RetentionRule{{Interval: "0s", Count: 1}}}},
		{"count", config.DeleterConfig{Rules: []config.RetentionRule{{Interval: "1h", Count: 0}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Run(&config.Config{Deleter: &tc.cfg}, 1, false); err == nil {
				t.Fatal("expected invalid config error even without matching datasets")
			}
		})
	}
}

func TestRunRejectsInvalidParallelism(t *testing.T) {
	for _, parallelism := range []int{-1, 0} {
		if err := Run(&config.Config{Deleter: &config.DeleterConfig{}}, parallelism, false); err == nil {
			t.Errorf("parallelism %d: expected validation error", parallelism)
		}
	}
}

func TestMarkForPreservationValidatesRulesWithoutSnapshots(t *testing.T) {
	dfp := &deleteFsProcessor{cfg: &config.DeleterConfig{
		Rules: []config.RetentionRule{{Interval: "0s", Count: 1}},
	}}
	if _, err := dfp.markForPreservation(); err == nil {
		t.Fatal("expected invalid rule error even without snapshots")
	}
}
