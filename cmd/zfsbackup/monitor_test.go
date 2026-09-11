package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMonitorCollection(t *testing.T) {
	const zfsScript = `#!/bin/sh
case " $* " in
  *" -t filesystem "*)
    [ "$ZFSBACKUP_TEST_FS_ERROR" != 1 ] || exit 1
    case "$*" in *missing) exit 1 ;; esac
    printf '%s\n' '{"datasets":{"tank/data":{"name":"tank/data"}}}'
    ;;
  *" -t snapshot "*)
    [ "$ZFSBACKUP_TEST_SNAP_ERROR" != 1 ] || exit 1
    printf '%s\n' "$ZFSBACKUP_TEST_SNAPSHOTS"
    ;;
  *) exit 2 ;;
esac
`
	const zpoolScript = `#!/bin/sh
[ "$ZFSBACKUP_TEST_POOL_ERROR" != 1 ] || exit 1
if [ "$ZFSBACKUP_TEST_POOL_FAULTED" = 1 ]; then
  printf '%s\n' '{"pools":{"tank":{"name":"tank","capacity":"-","health":"FAULTED"}}}'
else
  printf '%s\n' '{"pools":{"tank":{"name":"tank","capacity":"10","health":"ONLINE"}}}'
fi
`
	for _, tc := range []struct {
		name       string
		env        []string
		include    []string
		empty      bool
		wantError  bool
		wantMetric string
	}{
		{name: "snapshot", wantMetric: `LastSnapTimestamp{fs="tank/data"} 123`},
		{name: "no snapshots", empty: true, wantMetric: `LastSnapTimestamp{fs="tank/data"} 0`},
		{name: "snapshot listing fails", env: []string{"ZFSBACKUP_TEST_SNAP_ERROR=1"}, wantError: true, wantMetric: "HasBrokenPool 0"},
		{name: "faulted pool discovery fails", env: []string{"ZFSBACKUP_TEST_FS_ERROR=1", "ZFSBACKUP_TEST_POOL_FAULTED=1"}, wantError: true, wantMetric: "HasBrokenPool 1"},
		{name: "pool listing fails", env: []string{"ZFSBACKUP_TEST_POOL_ERROR=1"}, wantError: true, wantMetric: `LastSnapTimestamp{fs="tank/data"} 123`},
		{name: "partial discovery failure", include: []string{"missing", "tank"}, wantError: true, wantMetric: `LastSnapTimestamp{fs="tank/data"} 123`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, script := range map[string]string{"zfs": zfsScript, "zpool": zpoolScript} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			include := tc.include
			if include == nil {
				include = []string{"tank"}
			}
			outputFile := filepath.Join(dir, "metrics.prom")
			cfg, err := json.Marshal(map[string]any{"include": include, "monitor": map[string]string{"prometheus_output": outputFile}})
			if err != nil {
				t.Fatal(err)
			}
			configFile := filepath.Join(dir, "config.json")
			if err := os.WriteFile(configFile, cfg, 0o600); err != nil {
				t.Fatal(err)
			}
			snapshots := `{"datasets":{"tank/data@snap":{"creation":"123","createtxg":"1"}}}`
			if tc.empty {
				snapshots = `{"datasets":{}}`
			}
			cmd := cliCommand(t, "monitor", "--config", configFile)
			cmd.Env = append(cmd.Env, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "ZFSBACKUP_TEST_SNAPSHOTS="+snapshots)
			cmd.Env = append(cmd.Env, tc.env...)
			before := time.Now().Unix()
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("monitor: err=%v, output=%s", err, output)
			}
			metrics, err := os.ReadFile(outputFile)
			if err != nil {
				t.Fatalf("read metrics: %v, output=%s", err, output)
			}
			success := "1"
			if tc.wantError {
				success = "0"
			}
			for _, want := range []string{tc.wantMetric, "MonitorSuccess " + success} {
				if !strings.Contains(string(metrics), want+"\n") {
					t.Errorf("missing %q in metrics:\n%s", want, metrics)
				}
			}
			if !strings.Contains(string(output), string(metrics)) {
				t.Errorf("stdout does not contain published metrics: %s", output)
			}
			if tc.empty {
				foundAge := false
				for _, line := range strings.Split(string(metrics), "\n") {
					if value, found := strings.CutPrefix(line, `LastSnapAge{fs="tank/data"} `); found {
						foundAge = true
						age, err := strconv.ParseInt(value, 10, 64)
						if err != nil || age < before || age > time.Now().Unix() {
							t.Errorf("snapshot-less age %q does not use epoch sentinel", value)
						}
					}
				}
				if !foundAge {
					t.Error("snapshot-less filesystem has no age metric")
				}
			}
		})
	}
}
