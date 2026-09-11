package snapshot

import (
	"testing"

	"github.com/mikispag/zfsbackup/internal/config"
)

func TestRunRejectsInvalidSnapshotName(t *testing.T) {
	for _, pattern := range []string{"", ".", "..", "snap/name", "snap@name", "snap#name", "snap name", "snap-2006/01/02"} {
		t.Run(pattern, func(t *testing.T) {
			cfg := &config.Config{Snapshot: &config.SnapshotConfig{NamePattern: pattern}}
			if err := Run(cfg, false); err == nil {
				t.Fatal("expected invalid snapshot name error")
			}
		})
	}
}

func TestRunAcceptsValidSnapshotName(t *testing.T) {
	for _, pattern := range []string{"snap-2006-01-02_15-04-05", "manual-snapshot", "-snapshot"} {
		t.Run(pattern, func(t *testing.T) {
			cfg := &config.Config{Snapshot: &config.SnapshotConfig{NamePattern: pattern}}
			if err := Run(cfg, false); err != nil {
				t.Fatalf("valid snapshot name: %v", err)
			}
		})
	}
}

func TestRunRejectsInvalidSkipDuration(t *testing.T) {
	for _, duration := range []string{"invalid", "-1h", "9999999999999999h"} {
		t.Run(duration, func(t *testing.T) {
			cfg := &config.Config{Snapshot: &config.SnapshotConfig{
				NamePattern:          "snapshot",
				SkipEmptyYoungerThan: duration,
			}}
			if err := Run(cfg, false); err == nil {
				t.Fatal("expected invalid skip duration error")
			}
		})
	}
}
