package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func captureClipboard(t *testing.T) *string {
	t.Helper()
	var got string
	old := copyToClipboard
	copyToClipboard = func(s string) tea.Cmd { got = s; return nil }
	t.Cleanup(func() { copyToClipboard = old })
	return &got
}

func TestCopyAsFromDataTab(t *testing.T) {
	m, _ := newTestModel(t)
	clip := captureClipboard(t)
	openUsers(t, m)
	press(t, m, "space", "y", "c")
	if *clip != "id,name\n1,user1\n2,user2\n" {
		t.Fatalf("clipboard = %q", *clip)
	}
	if !strings.Contains(screen(m), "copied 2 rows as CSV") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	press(t, m, "space", "y", "i")
	if !strings.HasPrefix(*clip, "INSERT INTO `app`.`users` (`id`, `name`) VALUES ('1', 'user1');") {
		t.Fatalf("insert = %q", *clip)
	}
}

func TestYankCellAndRow(t *testing.T) {
	m, _ := newTestModel(t)
	clip := captureClipboard(t)
	openUsers(t, m)
	press(t, m, "l", "y", "l")
	if *clip != "user1" {
		t.Fatalf("cell = %q", *clip)
	}
	press(t, m, "y", "y")
	if *clip != "1\tuser1\n" {
		t.Fatalf("row = %q", *clip)
	}
}

func TestNothingToCopy(t *testing.T) {
	m, _ := newTestModel(t)
	clip := captureClipboard(t)
	press(t, m, "space", "y", "c")
	if *clip != "" || !strings.Contains(screen(m), "nothing to copy") {
		t.Fatalf("clip=%q screen:\n%s", *clip, screen(m))
	}
}

func exportedFile(t *testing.T, pattern string) string {
	t.Helper()
	files, _ := filepath.Glob(pattern)
	if len(files) != 1 {
		t.Fatalf("files matching %s: %v", pattern, files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestExportCurrentPage(t *testing.T) {
	m, _ := newTestModel(t)
	dir := t.TempDir()
	m.settings.ExportDir = filepath.Join(dir, "out")
	openUsers(t, m)
	press(t, m, "space", "x", "c")
	if !strings.Contains(screen(m), "Current page (2 rows)") {
		t.Fatalf("scope choice:\n%s", screen(m))
	}
	press(t, m, "enter")
	if !strings.Contains(screen(m), "Save to:") {
		t.Fatalf("path prompt:\n%s", screen(m))
	}
	press(t, m, "enter")
	if got := exportedFile(t, filepath.Join(dir, "out", "users-*.csv")); got != "id,name\n1,user1\n2,user2\n" {
		t.Fatalf("file = %q", got)
	}
	if !strings.Contains(screen(m), "exported 2 rows") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestExportWholeTableStreams(t *testing.T) {
	m, b := newTestModel(t)
	dir := t.TempDir()
	m.settings.ExportDir = dir
	openUsers(t, m)
	press(t, m, "space", "x", "j", "j", "enter", "enter")
	if got := b.conn.lastQuery(); got != "SELECT * FROM `app`.`users` ORDER BY `id`" {
		t.Fatalf("query = %q", got)
	}
	if got := exportedFile(t, filepath.Join(dir, "users-*.json")); strings.Count(got, `"id"`) != 3 {
		t.Fatalf("file = %s", got)
	}
}

func TestExportFromConsoleUsesStatementTarget(t *testing.T) {
	m, _ := newTestModel(t)
	dir := t.TempDir()
	m.settings.ExportDir = dir
	press(t, m, "l", "j", "space", "c")
	m.activeTab().(*consoleTab).editor.SetValue("select * from users")
	press(t, m, "enter", "space", "x", "s", "enter")
	if got := exportedFile(t, filepath.Join(dir, "users-*.sql")); !strings.HasPrefix(got, "INSERT INTO `users` (`id`, `name`)") {
		t.Fatalf("file = %q", got)
	}
}

func TestExportNeverOverwrites(t *testing.T) {
	m, _ := newTestModel(t)
	dir := t.TempDir()
	m.settings.ExportDir = dir
	openUsers(t, m)
	existing := filepath.Join(dir, "taken.csv")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	press(t, m, "space", "x", "c", "enter", "ctrl+u")
	for _, r := range existing {
		press(t, m, string(r))
	}
	press(t, m, "enter")
	if data, _ := os.ReadFile(existing); string(data) != "keep" {
		t.Fatal("existing file overwritten")
	}
	if !strings.Contains(screen(m), "export failed") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestExpandHome(t *testing.T) {
	for in, want := range map[string]string{"~": "/h", "~/x.csv": "/h/x.csv", "/a/b": "/a/b", "rel": "rel"} {
		if got := expandHome(in, "/h"); got != want {
			t.Errorf("expandHome(%q) = %q", in, got)
		}
	}
}
