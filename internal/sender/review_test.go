package sender

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikispag/zfsbackup/internal/config"
)

func TestPotentialSnapsToSendAnchorsAlternatives(t *testing.T) {
	fsp := makeFSP("daily|weekly", []source{
		{Name: "tank@daily-extra", GUID: "1", Srctype: Snapshot},
		{Name: "tank@extra-weekly", GUID: "2", Srctype: Snapshot},
		{Name: "tank@weekly", GUID: "3", Srctype: Snapshot},
	})
	if got := fsp.PotentialSnapsToSend(); len(got) != 1 || got[0].GUID != "3" {
		t.Fatalf("snapshot_re must match the entire snapshot name: %v", got)
	}
}

func TestGetNextAndSendRejectsUnusableReceiverState(t *testing.T) {
	for _, payload := range []string{`{}`, `{"resume_token":"pending"}`} {
		t.Run(payload, func(t *testing.T) {
			receiver := filepath.Join(t.TempDir(), "receiver")
			if err := os.WriteFile(receiver, []byte("#!/bin/sh\nprintf '%s' '"+payload+"'\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			resumable := false
			fsp := &fsProcessor{fs: "tank", job: &config.SenderConfig{Resumable: &resumable}, dst: &config.DestinationConfig{Receiver: receiver}}
			if _, err := fsp.GetNextAndSend(); err == nil {
				t.Fatal("unusable receiver state reported a successful backup")
			}
		})
	}
}

func TestSendPlaceholdersUsesNewestTXG(t *testing.T) {
	if os.Getenv("ZFSBACKUP_PLACEHOLDER_TEST") == "1" {
		fsp := &fsProcessor{fs: "tank", dst: &config.DestinationConfig{Receiver: os.Getenv("ZFSBACKUP_TEST_RECEIVER"), SyncPlaceholders: []string{"peer"}}}
		if err := fsp.sendPlaceholders(); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	zfsScript := `#!/bin/sh
printf '%s' '{"datasets":{"tank#z-old-peer":{"properties":{"guid":{"value":"1"},"createtxg":{"value":"10"}}},"tank#a-new-peer":{"properties":{"guid":{"value":"2"},"createtxg":{"value":"20"}}}}}'
`
	if err := os.WriteFile(filepath.Join(dir, "zfs"), []byte(zfsScript), 0o700); err != nil {
		t.Fatal(err)
	}
	receiver := filepath.Join(dir, "receiver")
	if err := os.WriteFile(receiver, []byte("#!/bin/sh\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSendPlaceholdersUsesNewestTXG$", "-test.v")
	cmd.Env = append(os.Environ(), "ZFSBACKUP_PLACEHOLDER_TEST=1", "ZFSBACKUP_TEST_RECEIVER="+receiver, "PATH="+dir+":"+os.Getenv("PATH"))
	// sendPlaceholders logs its response at debug level, so have the receiver
	// assert the actual wire payload before acknowledging success.
	if err := os.WriteFile(receiver, []byte("#!/bin/sh\npayload=$(cat)\ncase \"$payload\" in *'\"guid\":\"2\"'*) exit 0;; *) printf '%s' \"$payload\"; exit 1;; esac\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("placeholder did not select the newest TXG: %v\n%s", err, out)
	}
}

func TestSenderRunRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		job  config.SenderConfig
	}{
		{"empty receiver", config.SenderConfig{Destinations: []config.DestinationConfig{{Receiver: "  "}}}},
		{"invalid regex", config.SenderConfig{SnapshotRegex: "[", Destinations: []config.DestinationConfig{{Receiver: "true"}}}},
		{"shared placeholder", config.SenderConfig{Destinations: []config.DestinationConfig{{Receiver: "one", Placeholders: []string{"peer"}}, {Receiver: "two", Placeholders: []string{"peer"}}}}},
		{"invalid sync suffix", config.SenderConfig{Destinations: []config.DestinationConfig{{Receiver: "one", SyncPlaceholders: []string{"peer-two"}}}}},
		{"invalid compression", config.SenderConfig{Destinations: []config.DestinationConfig{{Receiver: "one", Compression: "typo"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Run(&config.Config{Sender: &tc.job}, 1, ""); err == nil {
				t.Fatal("invalid configuration reported success")
			}
		})
	}
}

func TestReceiverCommandPreservesArguments(t *testing.T) {
	cmd := buildReceiverCmd(context.Background(), "ssh backup@host --", "--dataset=tank/data")
	if got := strings.Join(cmd.Args, " "); got != "ssh backup@host -- --dataset=tank/data" {
		t.Fatal(got)
	}
}

func TestPlaceholderSyncWithoutSnapshots(t *testing.T) {
	if os.Getenv("ZFSBACKUP_SYNC_ONLY_TEST") == "1" {
		fsp := &fsProcessor{fs: "tank", job: &config.SenderConfig{}, dst: &config.DestinationConfig{
			Receiver: os.Getenv("ZFSBACKUP_TEST_RECEIVER"), SyncPlaceholders: []string{"peer"},
		}}
		if err := fsp.sendPlaceholders(); err != nil {
			t.Fatal(err)
		}
		if err := fsp.ProcessFs(); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, suffix := range []string{"", "other", "peer"} {
		t.Run("suffix="+suffix, func(t *testing.T) {
			dir := t.TempDir()
			payload := `{"datasets":{}}`
			if suffix != "" {
				payload = `{"datasets":{"tank#snap-` + suffix + `":{"properties":{"guid":{"value":"7"},"createtxg":{"value":"10"}}}}}`
			}
			if err := os.WriteFile(filepath.Join(dir, "zfs"), []byte("#!/bin/sh\nprintf '%s' '"+payload+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			receiver := filepath.Join(dir, "receiver")
			script := "#!/bin/sh\nexit 1\n"
			if suffix == "peer" {
				script = "#!/bin/sh\ntest \"$1\" = --op=set_placeholders || exit 1\ncat >/dev/null\n"
			}
			if err := os.WriteFile(receiver, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestPlaceholderSyncWithoutSnapshots$")
			cmd.Env = append(os.Environ(), "ZFSBACKUP_SYNC_ONLY_TEST=1", "ZFSBACKUP_TEST_RECEIVER="+receiver, "PATH="+dir+":"+os.Getenv("PATH"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("placeholder-only sync failed: %v\n%s", err, out)
			}
		})
	}
}

func TestSenderPipeline(t *testing.T) {
	if os.Getenv("ZFSBACKUP_PIPELINE_TEST") == "1" {
		dir := os.Getenv("ZFSBACKUP_PIPELINE_DIR")
		for _, dst := range []config.DestinationConfig{
			{Receiver: filepath.Join(dir, "receiver")},
			{Receiver: filepath.Join(dir, "receiver"), Compression: "zstd"},
			{Receiver: filepath.Join(dir, "receiver"), Compression: "zstd", MbufferArgs: []string{"-p 90"}},
		} {
			fsp := &fsProcessor{fs: "tank", dst: &dst}
			for i := 0; i < 25; i++ {
				if err := fsp.send([]string{"send", "tank@snapshot"}, false); err != nil {
					t.Fatal(err)
				}
			}
		}
		fsp := &fsProcessor{fs: "tank", dst: &config.DestinationConfig{Receiver: filepath.Join(dir, "missing-receiver"), Compression: "zstd", MbufferArgs: []string{"-p 90"}}}
		if err := fsp.send([]string{"send", "tank@snapshot"}, false); err == nil {
			t.Fatal("missing receiver unexpectedly started")
		}
		fsp.dst.Compression = "invalid"
		if err := fsp.send([]string{"send", "tank@snapshot"}, false); err == nil {
			t.Fatal("invalid compressor unexpectedly started")
		}
		return
	}
	dir := t.TempDir()
	for name, script := range map[string]string{
		"zfs":      "printf snapshot-stream",
		"mbuffer":  "exec cat",
		"zstd":     "exec cat",
		"receiver": "cat >> \"$ZFSBACKUP_PIPELINE_DIR/output\"",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSenderPipeline$")
	cmd.Env = append(os.Environ(), "ZFSBACKUP_PIPELINE_TEST=1", "ZFSBACKUP_PIPELINE_DIR="+dir, "PATH="+dir+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pipeline test failed: %v\n%s", err, out)
	}
	out, err := os.ReadFile(filepath.Join(dir, "output"))
	if err != nil || string(out) != strings.Repeat("snapshot-stream", 75) {
		t.Fatalf("stream was lost or truncated: %v; got %d bytes", err, len(out))
	}
}
