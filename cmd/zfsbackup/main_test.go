package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIProcess(t *testing.T) {
	if os.Getenv("ZFSBACKUP_TEST_CLI") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"zfsbackup"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	t.Fatal("missing argument separator")
}

func cliCommand(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, append([]string{"-test.run=^TestCLIProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "ZFSBACKUP_TEST_CLI=1")
	return cmd
}

func TestCLIConfigNamedHelpOrVersion(t *testing.T) {
	for _, name := range []string{"help", "version"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := cliCommand(t, "run", "--config", name)
			cmd.Dir = dir
			output, err := cmd.CombinedOutput()
			if err != nil || len(output) != 0 {
				t.Fatalf("run with config %q: err=%v, output=%s", name, err, output)
			}
		})
	}
}

func TestRunDryRunSkipsSender(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configFile, []byte(`{"sender":{"destinations":[{"receiver":"missing-receiver"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := cliCommand(t, "run", "--config", configFile, "--dry-run").CombinedOutput()
	if err != nil || !strings.Contains(string(output), "skipping sender module") {
		t.Fatalf("dry-run should skip sender: err=%v, output=%s", err, output)
	}
}

func TestRunRejectsInvalidParallelism(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"0", "-1"} {
		output, err := cliCommand(t, "run", "--config", configFile, "--parallelism", value).CombinedOutput()
		if err == nil || !strings.Contains(string(output), "parallelism must be positive") {
			t.Errorf("parallelism %s: err=%v, output=%s", value, err, output)
		}
	}
}

func TestCLIRejectsUnexpectedArguments(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configFile, []byte(`{"monitor":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"run", "monitor", "snapshot", "deleter", "sender"} {
		output, err := cliCommand(t, command, "--config", configFile, "unexpected", "--debug").CombinedOutput()
		if err == nil || !strings.Contains(string(output), "unexpected arguments") {
			t.Errorf("%s: err=%v, output=%s", command, err, output)
		}
	}
}

func TestCLIRejectsTrailingConfigData(t *testing.T) {
	for _, contents := range []string{"{} {}", "{} trailing", "{} null"} {
		configFile := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(configFile, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		for _, command := range []string{"run", "receiver"} {
			output, err := cliCommand(t, command, "--config", configFile).CombinedOutput()
			if err == nil || !strings.Contains(string(output), "exactly one JSON value") {
				t.Errorf("%s accepted config %q: err=%v, output=%s", command, contents, err, output)
			}
		}
	}
}
