package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunContinuesAfterFilesystemDiscoveryFailure(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configFile, []byte(`{"include":["tank"],"snapshot":{"name_pattern":"snap"},"monitor":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{
		"zfs": "exit 1", "zpool": `printf '%s' '{"pools":{}}'`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := cliCommand(t, "run", "--config", configFile, "--dry-run")
	cmd.Env = append(cmd.Env, "PATH="+dir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "snapshot failed") || !strings.Contains(string(out), "MonitorSuccess 0") {
		t.Fatalf("run should report failure after publishing monitor status: err=%v, output=%s", err, out)
	}
}
