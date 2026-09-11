package sender

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikispag/zfsbackup/internal/config"
)

func TestResumeValidatesTargetAndPreservesRawSend(t *testing.T) {
	if os.Getenv("ZFSBACKUP_RESUME_TEST") == "1" {
		dir := os.Getenv("ZFSBACKUP_RESUME_DIR")
		resumable := true
		job := &config.SenderConfig{SnapshotRegex: "daily", Resumable: &resumable}
		dst := &config.DestinationConfig{Receiver: filepath.Join(dir, "receiver"), RawSend: true, Placeholders: []string{"backup"}}
		if os.Getenv("ZFSBACKUP_RESUME_SYNC") == "1" {
			dst.SyncPlaceholders = []string{"peer"}
		}
		fsp := NewFSProcessor("tank", job, dst)
		fsp.sources = []source{
			{Name: "tank@daily", GUID: "1", Srctype: Snapshot},
			{Name: "tank@private", GUID: "2", Srctype: Snapshot},
			{Name: "tank#private-peer", GUID: "2", Srctype: Bookmark},
		}
		retry, err := fsp.GetNextAndSend()
		wantError := os.Getenv("ZFSBACKUP_RESUME_ERROR") == "1"
		if (err != nil) != wantError || (!wantError && !retry) {
			t.Fatalf("resume retry=%v error=%v; want error=%v", retry, err, wantError)
		}
		return
	}
	for _, tc := range []struct {
		name      string
		output    string
		wantError bool
		sync      bool
	}{
		{"full", "full\ttank@daily\t1024\nsize\t1024\n", false, false},
		{"incremental", "incremental\ttank#older-backup\ttank@daily\t1024\n", false, false},
		{"other dataset", "full\tsecret@daily\t1024\n", true, false},
		{"excluded snapshot", "full\ttank@private\t1024\n", true, false},
		{"mandatory snapshot", "full\ttank@private\t1024\n", false, true},
		{"missing target", "resume token contents:\n\ttoname = tank@daily\n", true, false},
		{"ambiguous target", "full\ttank@daily\t1\nfull\tsecret@daily\t1\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			zfsScript := `#!/bin/sh
printf '%s\n' "$*" >> "$ZFSBACKUP_RESUME_DIR/commands"
case "$1" in
send) case " $* " in *' -nP '*) printf '%s' "$ZFSBACKUP_RESUME_OUTPUT";; *) printf stream;; esac;;
list) printf '%s' '{"datasets":{}}';;
esac
`
			receiverScript := `#!/bin/sh
case "$1" in
--op=incremental_suggestions) printf '%s' '{"resume_token":"token"}';;
--op=receive) cat >/dev/null; : > "$ZFSBACKUP_RESUME_DIR/received";;
*) exit 1;;
esac
`
			for name, script := range map[string]string{"zfs": zfsScript, "receiver": receiverScript} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestResumeValidatesTargetAndPreservesRawSend$")
			cmd.Env = append(os.Environ(), "ZFSBACKUP_RESUME_TEST=1", "ZFSBACKUP_RESUME_DIR="+dir,
				"ZFSBACKUP_RESUME_OUTPUT="+tc.output, "PATH="+dir+":"+os.Getenv("PATH"))
			if tc.wantError {
				cmd.Env = append(cmd.Env, "ZFSBACKUP_RESUME_ERROR=1")
			}
			if tc.sync {
				cmd.Env = append(cmd.Env, "ZFSBACKUP_RESUME_SYNC=1")
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("resume validation failed: %v\n%s", err, out)
			}
			_, receivedErr := os.Stat(filepath.Join(dir, "received"))
			if tc.wantError {
				if !os.IsNotExist(receivedErr) {
					t.Fatal("invalid resume target reached receiver")
				}
				return
			}
			commands, err := os.ReadFile(filepath.Join(dir, "commands"))
			if err != nil || receivedErr != nil {
				t.Fatalf("resume failed: %v, %v", err, receivedErr)
			}
			snapshot := "daily"
			if tc.sync {
				snapshot = "private"
			}
			for _, expected := range []string{"send -nP -w -t token", "send -w -t token",
				"bookmark tank@" + snapshot + " #" + snapshot + "-before-backup\n",
				"bookmark tank@" + snapshot + " #" + snapshot + "-backup\n"} {
				if !strings.Contains(string(commands), expected) {
					t.Fatalf("missing %q in commands:\n%s", expected, commands)
				}
			}
		})
	}
}

func TestProcessFsSendsMandatorySnapshotOutsideRegex(t *testing.T) {
	if os.Getenv("ZFSBACKUP_MANDATORY_TEST") == "1" {
		fsp := NewFSProcessor("tank", &config.SenderConfig{SnapshotRegex: "daily"},
			&config.DestinationConfig{Receiver: os.Getenv("ZFSBACKUP_MANDATORY_RECEIVER"), SyncPlaceholders: []string{"peer"}})
		if err := fsp.ProcessFs(); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	zfsScript := `#!/bin/sh
case "$1" in
list) case " $* " in
*' -t bookmark '*) printf '%s' '{"datasets":{"tank#manual-peer":{"properties":{"guid":{"value":"1"},"createtxg":{"value":"10"}}}}}';;
*) printf '%s' '{"datasets":{"tank@manual":{"properties":{"guid":{"value":"1"},"createtxg":{"value":"10"}}},"tank#manual-peer":{"properties":{"guid":{"value":"1"},"createtxg":{"value":"10"}}}}}';;
esac;;
send) printf stream;;
esac
`
	receiverScript := `#!/bin/sh
case "$1" in
--op=incremental_suggestions) printf '%s' '{"send_full":true}';;
--op=receive) cat >/dev/null; : > "$ZFSBACKUP_MANDATORY_SENT";;
--op=set_placeholders) cat >/dev/null; test -f "$ZFSBACKUP_MANDATORY_SENT";;
*) exit 1;;
esac
`
	for name, script := range map[string]string{"zfs": zfsScript, "receiver": receiverScript} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessFsSendsMandatorySnapshotOutsideRegex$")
	cmd.Env = append(os.Environ(), "ZFSBACKUP_MANDATORY_TEST=1", "ZFSBACKUP_MANDATORY_RECEIVER="+filepath.Join(dir, "receiver"),
		"ZFSBACKUP_MANDATORY_SENT="+filepath.Join(dir, "sent"), "PATH="+dir+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mandatory send failed: %v\n%s", err, out)
	}
}
