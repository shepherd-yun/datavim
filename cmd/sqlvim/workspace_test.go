package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
)

func TestSelectWorkspaceDefault(t *testing.T) {
	cfg, state := t.TempDir(), t.TempDir()
	ws, err := selectWorkspace(cfg, state, workspaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ws.Name != "default" || ws.ConfigDir != cfg || ws.StateDir != state {
		t.Fatalf("ws = %+v: the default workspace uses the roots as before", ws)
	}
}

func TestSelectWorkspaceFlagBeatsEnv(t *testing.T) {
	cfg, state := t.TempDir(), t.TempDir()
	for _, n := range []string{"work", "personal"} {
		if err := os.MkdirAll(filepath.Join(cfg, "workspaces", n), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := selectWorkspace(cfg, state, workspaceOptions{name: "personal", env: "work"})
	if err != nil || ws.Name != "personal" {
		t.Fatalf("ws = %+v, %v; -w wins over SQLVIM_WORKSPACE", ws, err)
	}
	ws, err = selectWorkspace(cfg, state, workspaceOptions{env: "work"})
	if err != nil || ws.Name != "work" {
		t.Fatalf("ws = %+v, %v; SQLVIM_WORKSPACE is used without -w", ws, err)
	}
}

func TestSelectWorkspaceMissing(t *testing.T) {
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "workspaces", "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := selectWorkspace(cfg, t.TempDir(), workspaceOptions{name: "personal"})
	want := "workspace \"personal\" does not exist (have: default, work)\ncreate it with: sqlvim -w personal --create"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if _, statErr := os.Stat(filepath.Join(cfg, "workspaces", "personal")); statErr == nil {
		t.Fatal("a missing workspace must not be created without --create")
	}
}

func TestSelectWorkspaceCreate(t *testing.T) {
	cfg, state := t.TempDir(), t.TempDir()
	ws, err := selectWorkspace(cfg, state, workspaceOptions{name: "personal", create: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{ws.ConfigDir, ws.StateDir} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Fatalf("%s: %v", d, err)
		}
	}
	if _, err := selectWorkspace(cfg, state, workspaceOptions{name: "personal", create: true}); err != nil {
		t.Fatalf("--create on an existing workspace: %v", err)
	}
}

func TestSelectWorkspaceErrors(t *testing.T) {
	cfg, state := t.TempDir(), t.TempDir()
	for _, tc := range []struct {
		o    workspaceOptions
		want string
	}{
		{workspaceOptions{create: true}, "--create needs a workspace name (-w or SQLVIM_WORKSPACE)"},
		{workspaceOptions{name: "default", create: true}, "--create cannot be used with the default workspace"},
		{workspaceOptions{env: "default", create: true}, "--create cannot be used with the default workspace"},
		{workspaceOptions{name: "Work"}, `invalid workspace name "Work"`},
		{workspaceOptions{env: "../x"}, `invalid workspace name "../x"`},
	} {
		_, err := selectWorkspace(cfg, state, tc.o)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.o, err, tc.want)
		}
	}
}

func TestKeychainPrefix(t *testing.T) {
	if p := keychainPrefix(config.Workspace{Name: "default"}); p != "" {
		t.Fatalf("default prefix = %q", p)
	}
	if p := keychainPrefix(config.Workspace{Name: "personal"}); p != "personal/" {
		t.Fatalf("personal prefix = %q", p)
	}
}
