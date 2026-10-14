package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db/tunnel"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
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

func TestNewManagerPersistsAddedConnections(t *testing.T) {
	dir := t.TempDir()
	mgr := newManager(&config.Config{}, dir, nil)
	if err := mgr.AddConnection(config.Connection{Name: "local", Driver: "mysql"}, "", false); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Connections) != 1 || cfg.Connections[0].Name != "local" {
		t.Fatalf("connections = %+v", cfg.Connections)
	}
}

func TestNewManagerUsesTunnels(t *testing.T) {
	p := tunnel.NewPool(tunnel.Options{})
	defer p.Close()
	mgr := newManager(&config.Config{}, t.TempDir(), p)
	if mgr.Tunnels != p {
		t.Fatal("the manager opens ssh connections through the pool")
	}
	if mgr := newManager(&config.Config{}, t.TempDir(), nil); mgr.Tunnels != nil {
		t.Fatal("nil stays nil")
	}
}

func TestLocalUser(t *testing.T) {
	if localUser() == "" {
		t.Fatal("the default ssh user is the local user")
	}
}

func TestNewManagerPersistsEditsAndRemovals(t *testing.T) {
	keyring.MockInit() // never touch the real keychain
	dir := t.TempDir()
	mgr := newManager(&config.Config{}, dir, nil)
	if err := mgr.AddConnection(config.Connection{Name: "sqlvim-test-a", Driver: "mysql"}, "", false); err != nil {
		t.Fatal(err)
	}
	if err := mgr.UpdateConnection("sqlvim-test-a", config.Connection{Name: "sqlvim-test-b", Driver: "postgres"}, "", false); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Connections) != 1 || cfg.Connections[0].Name != "sqlvim-test-b" || cfg.Connections[0].Driver != "postgres" {
		t.Fatalf("connections = %+v", cfg.Connections)
	}
	if err := mgr.RemoveConnection("sqlvim-test-b"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.Load(dir); len(cfg.Connections) != 0 {
		t.Fatalf("connections = %+v", cfg.Connections)
	}
}

func TestRunReportsKeymapErrors(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for body, want := range map[string]string{
		"[grid]\n\"f\" = \"sort.column\"\n": "keymap.toml: keymap: f (sort.column) is a prefix of",
		"[grid]\n\"S\" = \"sort\"\n":        `keymap.toml: [grid] "S": unknown action "sort"`,
		"leader = \"ab\"\n":                 `keymap.toml: leader "ab"`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "keymap.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		err := run([]string{"--config", dir}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("keymap %q: err = %v, want %q", body, err, want)
		}
	}
}

func TestRunReportsThemeErrors(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[theme]\nfocus = \"bleu\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"--config", dir}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `config.toml: theme.focus: invalid color "bleu"`) {
		t.Fatalf("err = %v", err)
	}
}

// The shipped examples must load: a typo there is the first thing a new user hits.
func TestExamplesLoad(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"connections.toml", "config.toml", "keymap.toml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "docs", "examples", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildKeymap(dir, cfg.Keymap); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = theme.Apply(nil) })
	if err := theme.Apply(cfg.Theme); err != nil {
		t.Fatal(err)
	}
}
