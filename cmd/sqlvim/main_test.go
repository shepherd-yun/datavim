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
	mgr := newManager(&config.Config{}, config.Workspace{Name: config.DefaultWorkspace, ConfigDir: dir}, nil)
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
	mgr := newManager(&config.Config{}, config.Workspace{Name: config.DefaultWorkspace, ConfigDir: t.TempDir()}, p)
	if mgr.Tunnels != p {
		t.Fatal("the manager opens ssh connections through the pool")
	}
	if mgr := newManager(&config.Config{}, config.Workspace{Name: config.DefaultWorkspace, ConfigDir: t.TempDir()}, nil); mgr.Tunnels != nil {
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
	mgr := newManager(&config.Config{}, config.Workspace{Name: config.DefaultWorkspace, ConfigDir: dir}, nil)
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

func TestRunRejectsMissingWorkspace(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SQLVIM_WORKSPACE", "")
	dir := t.TempDir()
	err := run([]string{"--config", dir, "-w", "personal"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `workspace "personal" does not exist (have: default)`) ||
		!strings.Contains(err.Error(), "sqlvim -w personal --create") {
		t.Fatalf("err = %v", err)
	}
	err = run([]string{"--config", dir, "--workspace", "Work"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), `invalid workspace name "Work"`) {
		t.Fatalf("long flag: err = %v", err)
	}
}

func TestRunListsWorkspaces(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	for _, n := range []string{"work", "personal"} {
		if err := os.MkdirAll(filepath.Join(dir, "workspaces", n), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := run([]string{"--config", dir, "--list-workspaces"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "default\npersonal\nwork\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestNewManagerPersistsToWorkspace(t *testing.T) {
	keyring.MockInit()
	root := t.TempDir()
	ws, err := config.ResolveWorkspace(root, t.TempDir(), "personal")
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Create(); err != nil {
		t.Fatal(err)
	}
	mgr := newManager(&config.Config{}, ws, nil)
	if err := mgr.AddConnection(config.Connection{Name: "home", Driver: "mysql"}, "pw", true); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadIn(root, ws.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Connections) != 1 || cfg.Connections[0].Name != "home" {
		t.Fatalf("workspace connections = %+v", cfg.Connections)
	}
	if rootCfg, _ := config.Load(root); len(rootCfg.Connections) != 0 {
		t.Fatalf("the default workspace got %+v", rootCfg.Connections)
	}
	if got, err := keyring.Get("sqlvim", "personal/home"); err != nil || got != "pw" {
		t.Fatalf("keychain personal/home = %q, %v", got, err)
	}
}

func TestNewManagerOpensRedis(t *testing.T) {
	mgr := newManager(&config.Config{}, config.Workspace{Name: config.DefaultWorkspace, ConfigDir: t.TempDir()}, nil)
	if mgr.RedisOpen == nil || mgr.RedisSessionOpen == nil {
		t.Fatal("redis connections need an opener")
	}
}

func TestNewManagerOpensKafka(t *testing.T) {
	mgr := newManager(&config.Config{}, config.Workspace{Name: config.DefaultWorkspace, ConfigDir: t.TempDir()}, nil)
	if mgr.KafkaOpen == nil || mgr.KafkaReaderOpen == nil {
		t.Fatal("kafka connections need an opener")
	}
}
