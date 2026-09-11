package receiver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikispag/zfsbackup/internal/config"
)

func TestIncrementalSuggestionDistinguishesAbsenceAndErrors(t *testing.T) {
	if os.Getenv("ZFSBACKUP_RECEIVER_TEST") == "1" {
		incrementalSuggestion("backup", "tank", &config.ReceiverConfig{})
		return
	}
	for _, tc := range []struct {
		name      string
		script    string
		wantError bool
		want      string
	}{
		{"unavailable pool", "exit 1", true, ""},
		{"missing destination", `case "$*" in *receive_resume_token*) exit 1;; *) printf '%s' '{"datasets":{"backup":{}}}';; esac`, false, `"send_full":true`},
		{"destination property failure", `case "$*" in *receive_resume_token*) exit 1;; *) printf '%s' '{"datasets":{"backup":{},"backup/tank":{}}}';; esac`, true, ""},
		{"snapshot query failure", `case "$*" in *receive_resume_token*) printf '%s' '{"datasets":{"backup/tank":{"properties":{"receive_resume_token":{"value":"-"}}}}}';; *) exit 1;; esac`, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "zfs"), []byte("#!/bin/sh\n"+tc.script+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestIncrementalSuggestionDistinguishesAbsenceAndErrors$")
			cmd.Env = append(os.Environ(), "ZFSBACKUP_RECEIVER_TEST=1", "PATH="+dir+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError || !strings.Contains(string(out), tc.want) {
				t.Fatalf("error=%v output=%s; want error=%v containing %q", err, out, tc.wantError, tc.want)
			}
		})
	}
}

func TestReceiveHonorsDisableMount(t *testing.T) {
	if mode := os.Getenv("ZFSBACKUP_RECEIVE_MOUNT_TEST"); mode != "" {
		cfg := &config.ReceiverConfig{}
		if mode == "allow" {
			allow := false
			cfg.DisableMount = &allow
		}
		if err := receive("backup", "tank", "none", cfg, true); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, mode := range []string{"default", "allow"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ZFSBACKUP_RECEIVE_ARGS\"\n"
			if err := os.WriteFile(filepath.Join(dir, "zfs"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			argsPath := filepath.Join(dir, "args")
			cmd := exec.Command(os.Args[0], "-test.run=^TestReceiveHonorsDisableMount$")
			cmd.Env = append(os.Environ(), "ZFSBACKUP_RECEIVE_MOUNT_TEST="+mode, "ZFSBACKUP_RECEIVE_ARGS="+argsPath, "PATH="+dir+":"+os.Getenv("PATH"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("receive failed: %v\n%s", err, out)
			}
			args, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, flag := range []string{"\n-u\n", "\ncanmount=off\n"} {
				if strings.Contains(string(args), flag) != (mode == "default") {
					t.Fatalf("unexpected mount flags: %s", args)
				}
			}
		})
	}
}

func TestReceiverCommandLine(t *testing.T) {
	if mode := os.Getenv("ZFSBACKUP_RECEIVER_CLI_TEST"); mode != "" {
		os.Args = []string{"zfsbackup", "receiver", "--base_dataset=backup"}
		if mode == "local" || mode == "local positional" {
			os.Args = append(os.Args, "--", "--op=incremental_suggestions", "--dataset=tank")
		}
		if mode == "local positional" {
			os.Args = append(os.Args, "unexpected", "--fast")
		}
		Main()
		return
	}
	for _, tc := range []struct {
		mode      string
		ssh       string
		wantError bool
	}{
		{"local", "", false},
		{"local positional", "", true},
		{"ssh", "--op=incremental_suggestions --dataset=tank", false},
		{"ssh positional", "--op=incremental_suggestions --dataset=tank unexpected --fast", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			dir := t.TempDir()
			script := `#!/bin/sh
case "$*" in
  *receive_resume_token*) printf '%s' '{"datasets":{"backup/tank":{"properties":{"receive_resume_token":{"value":"-"}}}}}';;
  *snapshot*) printf '%s' '{"datasets":{"backup/tank@snap":{"properties":{"guid":{"value":"7"},"createtxg":{"value":"10"}}}}}';;
  *) printf '%s' '{"datasets":{"backup":{}}}';;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "zfs"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestReceiverCommandLine$")
			cmd.Env = append(os.Environ(), "ZFSBACKUP_RECEIVER_CLI_TEST="+tc.mode, "SSH_ORIGINAL_COMMAND="+tc.ssh, "PATH="+dir+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError || (!tc.wantError && !strings.Contains(string(out), `"guid":"7"`)) {
				t.Fatalf("receiver CLI error=%v output=%s; want error=%v", err, out, tc.wantError)
			}
		})
	}
}
