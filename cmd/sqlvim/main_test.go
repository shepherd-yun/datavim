package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPrintsVersion(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"--version"}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "sqlvim dev") {
		t.Fatalf("output = %q, want it to contain %q", got, "sqlvim dev")
	}
}

func TestRunWithoutConnectionsExplainsWhatToDo(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	err := run([]string{"--config", dir}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "connections.toml") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunReportsConfigErrors(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "connections.toml"), []byte("[[connection]]\nname = \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"--config", dir}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "connections.toml:") {
		t.Fatalf("err = %v", err)
	}
}
