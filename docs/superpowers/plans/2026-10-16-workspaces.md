# 工作空间 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `sqlvim -w <name>` (or `SQLVIM_WORKSPACE`) starts sqlvim in a workspace with its own connections, keychain entries and `state.db`, while `config.toml` / `keymap.toml` stay shared and no `-w` behaves exactly as today.

**Architecture:** `internal/config/workspace.go` validates names and resolves a `Workspace{Name, ConfigDir, StateDir}`; a default workspace's dirs are the roots themselves. `config.LoadIn(root, connDir)` reads connections from the workspace and settings/keymap from the root. `secret.Keyring` gains a `Prefix` for keychain accounts. `cmd/sqlvim` picks the workspace (`selectWorkspace`) before anything else and passes its dirs and prefix down; `ui.Model.SetWorkspace` shows the name in the status bar, welcome box and window title.

**Tech Stack:** Go, Bubble Tea v2 (`charm.land/bubbletea/v2`), `github.com/zalando/go-keyring`.

**Spec:** `docs/superpowers/specs/2026-10-16-workspaces-design.md`

## Global Constraints

- Workspace names: only `[a-z0-9_-]`, length 1–32, no case folding; `default` is the default workspace.
- Name resolution order: `-w`/`--workspace` > `SQLVIM_WORKSPACE` > `default`.
- Non-default workspace paths: `<cfg>/workspaces/<name>/connections.toml`, `<state>/workspaces/<name>/state.db`; keychain account `<name>/<conn>`. Default workspace: unchanged paths, unprefixed account.
- Password env var `SQLVIM_PASSWORD_<CONN>` is never prefixed.
- Directories are created with mode 0700.
- Error texts (exact):
  - `workspace "personal" does not exist (have: default, work)` + newline + `create it with: sqlvim -w personal --create`
  - `invalid workspace name "Work": use 1-32 characters from a-z, 0-9, '_' and '-'`
  - `--create needs a workspace name (-w or SQLVIM_WORKSPACE)`
  - `--create cannot be used with the default workspace`
- Safety rules (readonly, prod confirmations, …) are per connection and untouched. Passwords are never logged.
- UI text is in English. Conventional Commits with a scope, in Chinese like recent history. **No `Co-Authored-By` line.**
- `CHANGELOG.md` `[Unreleased]` entry; README changes go to both `README.md` and `README.zh-CN.md`.
- `make test` and `make lint` pass at the end of every task.

## Deviations from the spec (Task 5 updates the spec to match)

- `config.Load(dir)` keeps its signature as a wrapper for `LoadIn(dir, dir)` instead of changing every caller.
- `secret` tests use `keyring.MockInit()` (already used in this repo) instead of a new internal backend interface.
- The welcome box shows the workspace in its title (`sqlvim — personal`) instead of an extra `Workspace:` line, so `welcomeText` stays keymap-only.

## Review Focus

1. **`--config <dir>` with no workspace** must use `<dir>/connections.toml` and `<state>/state.db` exactly as before (existing tests and users rely on it). → test in Task 4.
2. **A connection added in the UI while in workspace `personal`** must be written to `workspaces/personal/connections.toml`, not the root file. → test in Task 4.
3. **The same connection name in two workspaces** must keep separate keychain passwords. → test in Task 2.
4. **`workspaces/personal` is a file, or `workspaces/default` / `workspaces/Bad` directories exist**: `personal` does not exist, `--create` fails with an OS error instead of starting, the list never shows `default` twice or invalid names. → tests in Task 1.
5. **A symlinked workspace directory** (dotfiles setups) must count as existing and appear in `--list-workspaces`. → test in Task 1.

---

## File Structure

| File | Change |
|---|---|
| `internal/config/workspace.go` (new) | `DefaultWorkspace`, `Workspace`, `ValidWorkspaceName`, `ResolveWorkspace`, `ListWorkspaces`, `Exists`, `Create`, `IsDefault` |
| `internal/config/workspace_test.go` (new) | tests for the above |
| `internal/config/config.go` | `LoadIn(root, connDir)`; `Load(dir)` wraps it |
| `internal/config/config_test.go` | `LoadIn` test |
| `internal/secret/secret.go` | `Keyring.Prefix` |
| `internal/secret/secret_test.go` | prefix tests |
| `internal/ui/app.go` | `Model.workspace`, `SetWorkspace`, status bar, welcome title, window title |
| `internal/ui/workspace_test.go` (new) | UI tests |
| `cmd/sqlvim/workspace.go` (new) | `workspaceOptions`, `selectWorkspace`, `keychainPrefix` |
| `cmd/sqlvim/workspace_test.go` (new) | tests for the above |
| `cmd/sqlvim/main.go` | flags, wiring, `newManager(cfg, ws, tunnels)` |
| `cmd/sqlvim/main_test.go` | `newManager` callers, run-level workspace tests |
| `CHANGELOG.md`, `README.md`, `README.zh-CN.md`, `docs/superpowers/specs/2026-10-13-sqlvim-design.md`, `docs/superpowers/specs/2026-10-16-workspaces-design.md` | docs |

---

### Task 1: `config` workspaces and `LoadIn`

**Files:**
- Create: `internal/config/workspace.go`, `internal/config/workspace_test.go`
- Modify: `internal/config/config.go:115-155` (`Load`)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `const DefaultWorkspace = "default"`
  - `type Workspace struct { Name, ConfigDir, StateDir string }`
  - `func ValidWorkspaceName(name string) error`
  - `func ResolveWorkspace(cfgRoot, stateRoot, name string) (Workspace, error)`
  - `func ListWorkspaces(cfgRoot string) ([]string, error)`
  - `func (w Workspace) IsDefault() bool`
  - `func (w Workspace) Exists() (bool, error)`
  - `func (w Workspace) Create() error`
  - `func LoadIn(root, connDir string) (*Config, error)`

- [ ] **Step 1: Write the failing tests**

`internal/config/workspace_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestValidWorkspaceName(t *testing.T) {
	for name, ok := range map[string]bool{
		"work":                              true,
		"my-db_2":                           true,
		"default":                           true,
		strings.Repeat("a", 32):             true,
		"":                                  false,
		strings.Repeat("a", 33):             false,
		"Work":                              false,
		"../x":                              false,
		"a/b":                               false,
		"a b":                               false,
		".":                                 false,
		"café":                              false,
	} {
		err := ValidWorkspaceName(name)
		if (err == nil) != ok {
			t.Errorf("ValidWorkspaceName(%q) = %v, want ok=%v", name, err, ok)
		}
	}
	err := ValidWorkspaceName("Work")
	want := `invalid workspace name "Work": use 1-32 characters from a-z, 0-9, '_' and '-'`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestResolveWorkspace(t *testing.T) {
	ws, err := ResolveWorkspace("/cfg", "/state", DefaultWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if ws != (Workspace{Name: "default", ConfigDir: "/cfg", StateDir: "/state"}) || !ws.IsDefault() {
		t.Fatalf("default = %+v", ws)
	}
	ws, err = ResolveWorkspace("/cfg", "/state", "personal")
	if err != nil {
		t.Fatal(err)
	}
	want := Workspace{
		Name:      "personal",
		ConfigDir: filepath.Join("/cfg", "workspaces", "personal"),
		StateDir:  filepath.Join("/state", "workspaces", "personal"),
	}
	if ws != want || ws.IsDefault() {
		t.Fatalf("personal = %+v, want %+v", ws, want)
	}
	if _, err := ResolveWorkspace("/cfg", "/state", "../etc"); err == nil {
		t.Fatal("an invalid name must not resolve to a path")
	}
}

func TestListWorkspaces(t *testing.T) {
	root := t.TempDir()
	if got, err := ListWorkspaces(root); err != nil || !slices.Equal(got, []string{"default"}) {
		t.Fatalf("no workspaces dir: %v, %v", got, err)
	}
	wsDir := filepath.Join(root, "workspaces")
	for _, d := range []string{"work", "personal", "default", "Bad"} {
		if err := os.MkdirAll(filepath.Join(wsDir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, wsDir, "notes", "a file, not a workspace")
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(wsDir, "linked")); err != nil {
		t.Fatal(err)
	}
	got, err := ListWorkspaces(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"default", "linked", "personal", "work"}; !slices.Equal(got, want) {
		t.Fatalf("ListWorkspaces = %v, want %v", got, want)
	}
}

func TestWorkspaceExistsAndCreate(t *testing.T) {
	cfg, state := t.TempDir(), t.TempDir()
	def, _ := ResolveWorkspace(cfg, state, DefaultWorkspace)
	if ok, err := def.Exists(); !ok || err != nil {
		t.Fatalf("default always exists: %v, %v", ok, err)
	}
	ws, _ := ResolveWorkspace(cfg, state, "personal")
	if ok, err := ws.Exists(); ok || err != nil {
		t.Fatalf("Exists before Create = %v, %v", ok, err)
	}
	if err := ws.Create(); err != nil {
		t.Fatal(err)
	}
	if err := ws.Create(); err != nil {
		t.Fatalf("Create on an existing workspace: %v", err)
	}
	if ok, err := ws.Exists(); !ok || err != nil {
		t.Fatalf("Exists after Create = %v, %v", ok, err)
	}
	for _, d := range []string{ws.ConfigDir, ws.StateDir} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v, want 0700", d, fi.Mode().Perm())
		}
	}
}

func TestWorkspaceThatIsAFile(t *testing.T) {
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "workspaces"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cfg, "workspaces"), "personal", "")
	ws, _ := ResolveWorkspace(cfg, t.TempDir(), "personal")
	if ok, err := ws.Exists(); ok || err != nil {
		t.Fatalf("a file is not a workspace: %v, %v", ok, err)
	}
	if err := ws.Create(); err == nil {
		t.Fatal("Create over a file must fail")
	}
}

func TestWorkspaceSymlinkExists(t *testing.T) {
	cfg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg, "workspaces"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(cfg, "workspaces", "linked")); err != nil {
		t.Fatal(err)
	}
	ws, _ := ResolveWorkspace(cfg, t.TempDir(), "linked")
	if ok, err := ws.Exists(); !ok || err != nil {
		t.Fatalf("a symlinked workspace exists: %v, %v", ok, err)
	}
}
```

Append to `internal/config/config_test.go`:

```go
func TestLoadInReadsConnectionsFromWorkspace(t *testing.T) {
	root := t.TempDir()
	connDir := filepath.Join(root, "workspaces", "personal")
	if err := os.MkdirAll(connDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "connections.toml", "[[connection]]\nname = \"work-db\"\ndriver = \"mysql\"\n")
	writeFile(t, connDir, "connections.toml", "[[connection]]\nname = \"home-db\"\ndriver = \"postgres\"\n")
	writeFile(t, root, "config.toml", "page_size = 7\n")
	writeFile(t, root, "keymap.toml", "leader = \",\"\n")

	cfg, err := LoadIn(root, connDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Connections) != 1 || cfg.Connections[0].Name != "home-db" {
		t.Fatalf("connections = %+v, want only home-db", cfg.Connections)
	}
	if cfg.Settings.PageSize != 7 || cfg.Keymap.Leader != "," {
		t.Fatalf("settings/keymap come from the root: %+v, %+v", cfg.Settings, cfg.Keymap)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/config`
Expected: build failure, `undefined: ValidWorkspaceName` / `LoadIn`.

- [ ] **Step 3: Implement `workspace.go`**

`internal/config/workspace.go`:

```go
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// DefaultWorkspace is the workspace sqlvim uses without -w; its files sit in
// the config and state roots, as before workspaces existed.
const DefaultWorkspace = "default"

const workspacesDir = "workspaces"

// Workspace is one set of connections with its own local state.
type Workspace struct {
	Name      string
	ConfigDir string // holds connections.toml
	StateDir  string // holds state.db
}

// ValidWorkspaceName reports whether name can be used as a directory and
// keychain prefix: 1-32 characters from a-z, 0-9, '_' and '-'.
func ValidWorkspaceName(name string) error {
	ok := len(name) >= 1 && len(name) <= 32
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			ok = false
			break
		}
	}
	if !ok {
		return fmt.Errorf("invalid workspace name %q: use 1-32 characters from a-z, 0-9, '_' and '-'", name)
	}
	return nil
}

// ResolveWorkspace validates name and returns its directories without
// touching the file system.
func ResolveWorkspace(cfgRoot, stateRoot, name string) (Workspace, error) {
	if name == DefaultWorkspace {
		return Workspace{Name: name, ConfigDir: cfgRoot, StateDir: stateRoot}, nil
	}
	if err := ValidWorkspaceName(name); err != nil {
		return Workspace{}, err
	}
	return Workspace{
		Name:      name,
		ConfigDir: filepath.Join(cfgRoot, workspacesDir, name),
		StateDir:  filepath.Join(stateRoot, workspacesDir, name),
	}, nil
}

// ListWorkspaces returns "default" followed by the sorted names of the
// directories (or symlinks to directories) under cfgRoot/workspaces that are
// valid workspace names.
func ListWorkspaces(cfgRoot string) ([]string, error) {
	dir := filepath.Join(cfgRoot, workspacesDir)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	names := []string{DefaultWorkspace}
	for _, e := range entries {
		n := e.Name()
		if n == DefaultWorkspace || ValidWorkspaceName(n) != nil {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, n)); err == nil && fi.IsDir() {
			names = append(names, n)
		}
	}
	return names, nil
}

// IsDefault reports whether w is the default workspace.
func (w Workspace) IsDefault() bool { return w.Name == DefaultWorkspace }

// Exists reports whether the workspace's config directory exists; the
// default workspace always exists.
func (w Workspace) Exists() (bool, error) {
	if w.IsDefault() {
		return true, nil
	}
	fi, err := os.Stat(w.ConfigDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	}
	return fi.IsDir(), nil
}

// Create makes the workspace's config and state directories.
func (w Workspace) Create() error {
	for _, d := range []string{w.ConfigDir, w.StateDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Implement `LoadIn`**

In `internal/config/config.go`, replace the head of `Load` (line 115-120):

```go
// Load reads connections.toml, config.toml and keymap.toml from dir. Missing files are not an error.
func Load(dir string) (*Config, error) { return LoadIn(dir, dir) }

// LoadIn reads config.toml and keymap.toml from root and connections.toml
// from connDir, a workspace's directory. Missing files are not an error.
func LoadIn(root, connDir string) (*Config, error) {
	cfg := &Config{Settings: DefaultSettings()}

	connPath := filepath.Join(connDir, "connections.toml")
```

and in the rest of the function body replace `filepath.Join(dir, "config.toml")` with `filepath.Join(root, "config.toml")` and `filepath.Join(dir, "keymap.toml")` with `filepath.Join(root, "keymap.toml")`. Keep the original doc comment text for `Load` (check the existing line 115 wording and keep it).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -race ./internal/config && make lint`
Expected: PASS, no lint findings.

- [ ] **Step 6: Commit**

```bash
git add internal/config
git commit -m "feat(config): 工作空间的名字校验、路径与列表"
```

---

### Task 2: keychain prefix

**Files:**
- Modify: `internal/secret/secret.go:25-58`
- Test: `internal/secret/secret_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `secret.Keyring{Prefix string}`; account is `Prefix + name`; `EnvVar(name)` unchanged and unprefixed.

- [ ] **Step 1: Write the failing tests**

Append to `internal/secret/secret_test.go`:

```go
func TestKeyringPrefixKeepsWorkspacesApart(t *testing.T) {
	keyring.MockInit()
	noEnv := func(string) string { return "" }
	work := Keyring{Getenv: noEnv}
	personal := Keyring{Getenv: noEnv, Prefix: "personal/"}

	if err := work.Set("local", "work-pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := personal.Get("local"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("personal sees the default workspace's password: %v", err)
	}
	if err := personal.Set("local", "home-pw"); err != nil {
		t.Fatal(err)
	}
	if got, _ := work.Get("local"); got != "work-pw" {
		t.Fatalf("default Get = %q, want work-pw", got)
	}
	if got, _ := personal.Get("local"); got != "home-pw" {
		t.Fatalf("personal Get = %q, want home-pw", got)
	}
	if got, err := keyring.Get("sqlvim", "personal/local"); err != nil || got != "home-pw" {
		t.Fatalf("keychain account personal/local = %q, %v", got, err)
	}
	if err := personal.Delete("local"); err != nil {
		t.Fatal(err)
	}
	if got, _ := work.Get("local"); got != "work-pw" {
		t.Fatalf("deleting in personal removed the default password: %q", got)
	}
}

func TestEnvOverrideIgnoresPrefix(t *testing.T) {
	keyring.MockInit()
	k := Keyring{Prefix: "personal/", Getenv: func(name string) string {
		if name == "SQLVIM_PASSWORD_LOCAL" {
			return "from-env"
		}
		return ""
	}}
	if got, _ := k.Get("local"); got != "from-env" {
		t.Fatalf("Get = %q, want from-env", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/secret`
Expected: build failure, `unknown field Prefix in struct literal`.

- [ ] **Step 3: Implement**

In `internal/secret/secret.go`:

```go
// Keyring is a Store backed by the macOS Keychain (or the platform equivalent).
// The environment variable EnvVar(name) takes precedence, which is handy on CI.
type Keyring struct {
	// Getenv defaults to os.Getenv.
	Getenv func(string) string
	// Prefix goes before the connection name in the keychain account, so
	// workspaces keep same-named connections apart ("personal/").
	Prefix string
}
```

- `Get`: `pw, err := keyring.Get(service, k.Prefix+name)`
- `Set`: change the receiver to `(k Keyring)` and call `keyring.Set(service, k.Prefix+name, password)`
- `Delete`: change the receiver to `(k Keyring)` and call `keyring.Delete(service, k.Prefix+name)`

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/secret && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/secret
git commit -m "feat(secret): 钥匙串账户支持工作空间前缀"
```

---

### Task 3: show the workspace in the UI

**Files:**
- Modify: `internal/ui/app.go` (`Model` struct ~line 72, `New` neighbourhood ~line 150, `View` line 155, `renderTabs` line 1270, `renderStatus` line 1298)
- Test: `internal/ui/workspace_test.go` (new)

**Interfaces:**
- Consumes: `config.DefaultWorkspace` (Task 1).
- Produces: `func (m *Model) SetWorkspace(name string)`; `""` or `"default"` shows nothing.

- [ ] **Step 1: Write the failing tests**

`internal/ui/workspace_test.go`:

```go
package ui

import (
	"strings"
	"testing"
)

func TestWorkspaceIsShown(t *testing.T) {
	m, _ := newTestModel(t)
	m.SetWorkspace("personal")
	s := screen(m)
	lines := strings.Split(s, "\n")
	if status := lines[len(lines)-1]; !strings.Contains(status, "[personal]") {
		t.Fatalf("status bar must name the workspace:\n%s", status)
	}
	if !strings.Contains(s, "sqlvim — personal") {
		t.Fatalf("welcome box title must name the workspace:\n%s", s)
	}
	if got := m.View().WindowTitle; got != "sqlvim — personal" {
		t.Fatalf("WindowTitle = %q", got)
	}
}

func TestDefaultWorkspaceIsNotShown(t *testing.T) {
	for _, name := range []string{"", "default"} {
		m, _ := newTestModel(t)
		m.SetWorkspace(name)
		s := screen(m)
		if strings.Contains(s, "[default]") || strings.Contains(s, "sqlvim — ") {
			t.Fatalf("workspace %q must not be shown:\n%s", name, s)
		}
		if got := m.View().WindowTitle; got != "" {
			t.Fatalf("workspace %q: WindowTitle = %q", name, got)
		}
	}
}
```

If the status bar is not the last line of `screen(m)`, assert on `ansi.Strip(m.renderStatus())` instead (import `github.com/charmbracelet/x/ansi` as `app_test.go` does).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/ui -run Workspace`
Expected: build failure, `m.SetWorkspace undefined`.

- [ ] **Step 3: Implement**

In the `Model` struct, after `keys *keymap.Keymap`:

```go
	workspace string // shown when not the default workspace
```

After `SetKeymap`:

```go
// SetWorkspace names the workspace sqlvim runs in; the default workspace
// ("" or "default") is not shown.
func (m *Model) SetWorkspace(name string) {
	if name == config.DefaultWorkspace {
		name = ""
	}
	m.workspace = name
}

// title is "sqlvim", followed by the workspace when it is not the default.
func (m *Model) title() string {
	if m.workspace == "" {
		return "sqlvim"
	}
	return "sqlvim — " + m.workspace
}
```

`View`:

```go
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	if m.workspace != "" {
		v.WindowTitle = m.title()
	}
	return v
}
```

`renderTabs`: `return theme.Box(m.title(), welcomeText(m.keys), width, height, false)`.

`renderStatus`, right after `parts := []string{mode.Render(" " + m.mode() + " ")}`:

```go
	if m.workspace != "" {
		parts = append(parts, theme.Accent.Render("["+textutil.Sanitize(m.workspace)+"]"))
	}
```

(Names are already validated to `[a-z0-9_-]`; `Sanitize` keeps the status bar safe if `SetWorkspace` is ever called with something else.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/ui && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ui
git commit -m "feat(ui): 状态栏、欢迎页与窗口标题显示工作空间"
```

---

### Task 4: command line and wiring

**Files:**
- Create: `cmd/sqlvim/workspace.go`, `cmd/sqlvim/workspace_test.go`
- Modify: `cmd/sqlvim/main.go:37-104` (`run`), `:136-150` (`newManager`)
- Test: `cmd/sqlvim/main_test.go`

**Interfaces:**
- Consumes: Task 1 (`config.ResolveWorkspace`, `ListWorkspaces`, `Workspace.Exists/Create/IsDefault`, `LoadIn`), Task 2 (`secret.Keyring.Prefix`), Task 3 (`Model.SetWorkspace`).
- Produces:
  - `type workspaceOptions struct { name, env string; create bool }`
  - `func selectWorkspace(cfgRoot, stateRoot string, o workspaceOptions) (config.Workspace, error)`
  - `func keychainPrefix(ws config.Workspace) string`
  - `func newManager(cfg *config.Config, ws config.Workspace, tunnels session.Tunneler) *session.Manager`

- [ ] **Step 1: Write the failing tests for `selectWorkspace`**

`cmd/sqlvim/workspace_test.go`:

```go
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
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -race ./cmd/sqlvim -run 'SelectWorkspace|KeychainPrefix'`
Expected: build failure, `undefined: selectWorkspace`.

- [ ] **Step 3: Implement `cmd/sqlvim/workspace.go`**

```go
package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/config"
)

// workspaceOptions are the command line's say on the workspace.
type workspaceOptions struct {
	name   string // -w / --workspace
	env    string // SQLVIM_WORKSPACE
	create bool   // --create
}

// selectWorkspace picks the workspace: -w, else SQLVIM_WORKSPACE, else the
// default. A missing workspace is an error unless --create asks for it, so a
// typo never opens an empty workspace.
func selectWorkspace(cfgRoot, stateRoot string, o workspaceOptions) (config.Workspace, error) {
	name := o.name
	if name == "" {
		name = o.env
	}
	if name == "" {
		if o.create {
			return config.Workspace{}, errors.New("--create needs a workspace name (-w or SQLVIM_WORKSPACE)")
		}
		name = config.DefaultWorkspace
	}
	ws, err := config.ResolveWorkspace(cfgRoot, stateRoot, name)
	if err != nil {
		return ws, err
	}
	if ws.IsDefault() {
		if o.create {
			return ws, errors.New("--create cannot be used with the default workspace")
		}
		return ws, nil
	}
	ok, err := ws.Exists()
	switch {
	case err != nil:
		return ws, err
	case ok:
		return ws, nil
	case o.create:
		return ws, ws.Create()
	}
	have, err := config.ListWorkspaces(cfgRoot)
	if err != nil {
		return ws, err
	}
	return ws, fmt.Errorf("workspace %q does not exist (have: %s)\ncreate it with: sqlvim -w %s --create",
		name, strings.Join(have, ", "), name)
}

// keychainPrefix keeps the passwords of same-named connections in different
// workspaces apart; the default workspace keeps its accounts unprefixed.
func keychainPrefix(ws config.Workspace) string {
	if ws.IsDefault() {
		return ""
	}
	return ws.Name + "/"
}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `go test -race ./cmd/sqlvim -run 'SelectWorkspace|KeychainPrefix'`
Expected: PASS.

- [ ] **Step 5: Write the failing run-level and manager tests**

In `cmd/sqlvim/main_test.go`, change every `newManager(&config.Config{}, dir, X)` call to `newManager(&config.Config{}, config.Workspace{Name: config.DefaultWorkspace, ConfigDir: dir}, X)` (and `t.TempDir()` likewise), then append:

```go
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
```

`Manager.AddConnection(c, password, save)` persists `c` through `Persist` and, with a non-empty password and `save`, stores it through the manager's `secret.Store` (`internal/session/manager.go:92-114`).

- [ ] **Step 6: Run them to verify they fail**

Run: `go test -race ./cmd/sqlvim`
Expected: build failure (`newManager` takes a `string`), then after Step 7 all pass.

- [ ] **Step 7: Wire `run` and `newManager`**

In `run`, after `configDir := ...`:

```go
	var wsName string
	fs.StringVar(&wsName, "workspace", "", "workspace to open (default $SQLVIM_WORKSPACE, else default)")
	fs.StringVar(&wsName, "w", "", "shorthand for --workspace")
	create := fs.Bool("create", false, "create the workspace if it does not exist")
	listWorkspaces := fs.Bool("list-workspaces", false, "print the workspaces and exit")
```

Replace the block from `cfg, err := config.Load(dir)` with:

```go
	if *listWorkspaces {
		names, err := config.ListWorkspaces(dir)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, strings.Join(names, "\n"))
		return err
	}
	stateRoot, err := config.StateDir()
	if err != nil {
		return err
	}
	ws, err := selectWorkspace(dir, stateRoot, workspaceOptions{
		name: wsName, env: os.Getenv("SQLVIM_WORKSPACE"), create: *create,
	})
	if err != nil {
		return err
	}
	cfg, err := config.LoadIn(dir, ws.ConfigDir)
	if err != nil {
		return err
	}
```

Replace `st, err := openStore(filepath.Dir(logPath))` with:

```go
	if err := os.MkdirAll(ws.StateDir, 0o700); err != nil {
		return err
	}
	st, err := openStore(ws.StateDir)
```

Replace `mgr := newManager(cfg, dir, tunnels)` with `mgr := newManager(cfg, ws, tunnels)`, the start log with `slog.Info("starting", "version", version, "workspace", ws.Name, "connections", len(cfg.Connections))`, and add `model.SetWorkspace(ws.Name)` after `model.SetKeymap(keys)`. Add `"strings"` to the imports.

`newManager`:

```go
// newManager builds the session manager for workspace ws; connections added,
// edited or deleted in the UI are saved to its connections.toml, passwords
// go to its keychain accounts, and ssh connections use tunnels.
func newManager(cfg *config.Config, ws config.Workspace, tunnels session.Tunneler) *session.Manager {
	dir := ws.ConfigDir
	mgr := session.NewManager(cfg.Connections, secret.Keyring{Prefix: keychainPrefix(ws)}, mysql.Driver{}, postgres.Driver{})
	// … the Persist / PersistUpdate / PersistRemove / Tunnels lines stay as they are
```

- [ ] **Step 8: Run all tests and lint**

Run: `make test && make lint`
Expected: PASS. Then a manual smoke check:

```sh
make build
./bin/sqlvim --config "$(mktemp -d)" --list-workspaces          # default
./bin/sqlvim --config /tmp/x -w personal; echo $?               # error + hint, 1
```

- [ ] **Step 9: Commit**

```bash
git add cmd/sqlvim
git commit -m "feat: -w 选择工作空间，--create 与 --list-workspaces"
```

---

### Task 5: docs

**Files:**
- Modify: `CHANGELOG.md` (`[Unreleased]` → `### Added`), `README.md` (new `## Workspaces` before `## Keys`), `README.zh-CN.md` (new `## 工作空间` before `## 按键`), `docs/superpowers/specs/2026-10-13-sqlvim-design.md` (§5.1, §7), `docs/superpowers/specs/2026-10-16-workspaces-design.md` (§5 deviations, 状态)

- [ ] **Step 1: CHANGELOG**

Add at the top of `### Added` under `[Unreleased]`:

```markdown
- Workspaces keep sets of connections apart, like DataGrip's: `sqlvim -w personal` (or
  `SQLVIM_WORKSPACE=personal`) opens a workspace with its own connections, keychain
  passwords, history and drafts, while `config.toml` and `keymap.toml` stay shared. Create
  one with `--create`, list them with `--list-workspaces`. Without `-w` nothing changes.
```

- [ ] **Step 2: README.md** — insert before `## Keys`:

````markdown
## Workspaces

A workspace is its own set of connections, say one for work and one for personal databases.
Each has its own `connections.toml`, keychain passwords, history and console drafts; settings,
theme and keys are shared.

```sh
sqlvim -w personal --create   # create it once
sqlvim -w personal            # or: SQLVIM_WORKSPACE=personal sqlvim
sqlvim --list-workspaces
```

Without `-w` sqlvim opens the `default` workspace, which is the files it has always used.
The workspace name shows in the status bar and the window title.

| | default | `personal` |
|---|---|---|
| connections | `~/.config/sqlvim/connections.toml` | `~/.config/sqlvim/workspaces/personal/connections.toml` |
| history, drafts | `~/.local/state/sqlvim/state.db` | `~/.local/state/sqlvim/workspaces/personal/state.db` |
| keychain account | `<connection>` | `personal/<connection>` |

To move connections into a workspace, create it, cut their `[[connection]]` blocks into its
`connections.toml`, and enter each password once more on first connect. Their history stays
in the default workspace.

With [direnv](https://direnv.net), `export SQLVIM_WORKSPACE=work` in a project's `.envrc`
picks the workspace by directory.
````

- [ ] **Step 3: README.zh-CN.md** — insert before `## 按键`:

````markdown
## 工作空间

工作空间是一组独立的连接，比如一个放工作数据库，一个放个人数据库。每个空间有自己的
`connections.toml`、钥匙串密码、历史和控制台草稿；设置、主题和按键在所有空间共享。

```sh
sqlvim -w personal --create   # 第一次先创建
sqlvim -w personal            # 或：SQLVIM_WORKSPACE=personal sqlvim
sqlvim --list-workspaces
```

不加 `-w` 时进入 `default` 空间，也就是一直以来用的那些文件。当前空间名会显示在状态栏和窗口标题里。

| | default | `personal` |
|---|---|---|
| 连接 | `~/.config/sqlvim/connections.toml` | `~/.config/sqlvim/workspaces/personal/connections.toml` |
| 历史、草稿 | `~/.local/state/sqlvim/state.db` | `~/.local/state/sqlvim/workspaces/personal/state.db` |
| 钥匙串账户 | `<连接名>` | `personal/<连接名>` |

要把连接挪到某个空间：先创建空间，把对应的 `[[connection]]` 段落剪切到它的
`connections.toml`，首次连接时重新输入一次密码。这些连接的历史留在默认空间。

配合 [direnv](https://direnv.net)，在项目的 `.envrc` 里写 `export SQLVIM_WORKSPACE=work`，就能按目录自动选空间。
````

- [ ] **Step 4: Specs**

- Main spec §5.1, after the toml block's bullets, add: `- 连接可以按工作空间分组，见 2026-10-16-workspaces-design.md。`
- Main spec §7, after the table, add: `非默认工作空间的 connections.toml 和 state.db 位于 workspaces/<name>/ 下，见 2026-10-16-workspaces-design.md。`
- Workspaces spec: `状态：已实现`; in §5.1 replace "`Load(dir)` 改为 `Load(root, connDir string)`" with "新增 `LoadIn(root, connDir string)`，`Load(dir)` 保留为 `LoadIn(dir, dir)`"; in §5.2 replace the "收进一个包内的小接口" paragraph with "测试用 `keyring.MockInit()`"; in §5.4 replace the welcome line with "欢迎页框的标题为 `sqlvim — personal`"; in §5.3 `newManager` takes `config.Workspace`.

- [ ] **Step 5: Verify and commit**

Run: `make test && make lint`
Expected: PASS.

```bash
git add CHANGELOG.md README.md README.zh-CN.md docs/superpowers/specs
git commit -m "docs: 工作空间"
```
