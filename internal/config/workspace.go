package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// DefaultWorkspace is the workspace datavim uses without -w; its files sit in
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
		if !workspaceNameRune(r) {
			ok = false
			break
		}
	}
	if !ok {
		return fmt.Errorf("invalid workspace name %q: use 1-32 characters from a-z, 0-9, '_' and '-'", name)
	}
	return nil
}

func workspaceNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-'
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
