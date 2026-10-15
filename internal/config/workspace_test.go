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
		"work":                  true,
		"my-db_2":               true,
		"default":               true,
		strings.Repeat("a", 32): true,
		"":                      false,
		strings.Repeat("a", 33): false,
		"Work":                  false,
		"../x":                  false,
		"a/b":                   false,
		"a b":                   false,
		".":                     false,
		"café":                  false,
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
