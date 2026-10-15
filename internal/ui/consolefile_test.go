package ui

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

// ex runs an ex command as if typed on the command line.
func ex(t *testing.T, m *Model, text string) {
	t.Helper()
	drain(t, m, func() tea.Msg { return overlay.CommandMsg{Text: text} })
}

// writeSQL writes text to name in a new temp dir and returns its path.
func writeSQL(t *testing.T, name, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func fileConsole(t *testing.T, m *Model) *consoleTab {
	t.Helper()
	c, ok := m.activeTab().(*consoleTab)
	if !ok || c.file == nil {
		t.Fatalf("no file console: %T\n%s", m.activeTab(), screen(m))
	}
	return c
}

func TestEditOpensAFileConsoleOnTheTreeConnection(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "select 1;\n")
	ex(t, m, "e "+p)
	c := fileConsole(t, m)
	if c.conn != "local" || c.editor.Value() != "select 1;\n" || c.Title() != "a.sql" {
		t.Fatalf("conn %s title %q text %q", c.conn, c.Title(), c.editor.Value())
	}
	if !strings.Contains(statusLine(m), "a.sql") {
		t.Fatalf("status bar shows the path: %s", statusLine(m))
	}
}

func TestEditInheritsTheTabNamespace(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	ex(t, m, "e "+writeSQL(t, "a.sql", ""))
	if c := fileConsole(t, m); c.conn != "local" || c.ns != appNS {
		t.Fatalf("on %s %+v", c.conn, c.ns)
	}
}

func TestEditNeedsAConnectionAndAName(t *testing.T) {
	b := newFakeBackend()
	b.conns = nil
	m := New(b, newFakeStore(), config.Settings{PageSize: 2, MaxColumnWidth: 40, MaxRows: 10})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	ex(t, m, "e "+writeSQL(t, "a.sql", ""))
	if !strings.Contains(m.status, "open a console or select a connection first") {
		t.Fatalf("status = %q", m.status)
	}
	m2, _ := newTestModel(t)
	ex(t, m2, "e")
	if !strings.Contains(m2.status, ":e needs a file name") {
		t.Fatalf("status = %q", m2.status)
	}
}

func TestEditSameFileSwitchesToItsTab(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "select 1;")
	t.Chdir(filepath.Dir(p))
	for _, spelling := range []string{p, "a.sql", "./a.sql"} {
		ex(t, m, "e "+spelling)
	}
	if len(m.tabs) != 1 {
		t.Fatalf("tabs = %d, want 1", len(m.tabs))
	}
}

func TestEditSameFileOnAnotherConnection(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "")
	ex(t, m, "e "+p)
	press(t, m, "ctrl+h", "g", "g", "j") // tree: prod
	ex(t, m, "e "+p)
	if len(m.tabs) != 2 || fileConsole(t, m).conn != "prod" {
		t.Fatalf("tabs = %d", len(m.tabs))
	}
}

func TestEditMissingFileIsNew(t *testing.T) {
	m, _ := newTestModel(t)
	ex(t, m, "e "+filepath.Join(t.TempDir(), "b.sql"))
	if c := fileConsole(t, m); c.Title() != "b.sql [new]" || c.editor.Value() != "" {
		t.Fatalf("title %q", c.Title())
	}
}

func TestEditPathWithSpaces(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "my file.sql", "select 1;")
	ex(t, m, "e "+p)
	if c := fileConsole(t, m); c.editor.Value() != "select 1;" {
		t.Fatalf("text %q", c.editor.Value())
	}
}

func TestEditRejectsWhatTheEditorCannotHold(t *testing.T) {
	for name, c := range map[string]struct {
		content []byte
		dir     bool
		want    string
	}{
		"big.sql": {content: []byte(strings.Repeat("x", 2<<20+1)), want: "big.sql is 2.0 MB: too large for the editor"},
		"bin.sql": {content: []byte{0xff, 0xfe, 'a'}, want: "bin.sql is not a UTF-8 text file"},
		"nul.sql": {content: []byte("a\x00b"), want: "nul.sql is not a UTF-8 text file"},
		"dir.sql": {dir: true, want: "dir.sql is a directory"},
	} {
		t.Run(name, func(t *testing.T) {
			m, _ := newTestModel(t)
			p := filepath.Join(t.TempDir(), name)
			var err error
			if c.dir {
				err = os.Mkdir(p, 0o755)
			} else {
				err = os.WriteFile(p, c.content, 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			ex(t, m, "e "+p)
			if len(m.tabs) != 0 || !strings.Contains(m.status, c.want) {
				t.Fatalf("tabs %d status %q", len(m.tabs), m.status)
			}
		})
	}
}

func TestEditDropsTheBOM(t *testing.T) {
	m, _ := newTestModel(t)
	ex(t, m, "e "+writeSQL(t, "a.sql", "\ufeffselect 1;"))
	if c := fileConsole(t, m); c.editor.Value() != "select 1;" || c.dirty() {
		t.Fatalf("text %q dirty %v", c.editor.Value(), c.dirty())
	}
}

func TestFileConsoleMarksChangesAndKeepsNoDraft(t *testing.T) {
	m, _ := newTestModel(t)
	ex(t, m, "e "+writeSQL(t, "a.sql", "select 1;"))
	c := fileConsole(t, m)
	press(t, m, "A", "x", "esc")
	if c.Title() != "a.sql [+]" {
		t.Fatalf("title %q", c.Title())
	}
	st := m.store.(*fakeStore)
	st.mu.Lock()
	defer st.mu.Unlock()
	for k := range st.drafts {
		if strings.HasPrefix(k, "file:") {
			t.Fatalf("draft saved for %s", k)
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWriteSavesTheFileKeepingItsMode(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "select 1;")
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	ex(t, m, "e "+p)
	c := fileConsole(t, m)
	c.editor.SetValue("select 2;\n")
	ex(t, m, "w")
	if got := readFile(t, p); got != "select 2;\n" {
		t.Fatalf("disk %q", got)
	}
	if info, _ := os.Stat(p); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if c.Title() != "a.sql" || !strings.Contains(c.status, "written, 1 line") {
		t.Fatalf("title %q status %q", c.Title(), c.status)
	}
}

func TestWriteInAFileConsoleLeavesStagedEdits(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "")
	ex(t, m, "e "+p)
	c := fileConsole(t, m)
	stageName(t, c)
	ex(t, m, "w")
	if c.changes() != 1 || c.res.pendingSQL != nil {
		t.Fatalf("changes %d, preview %v: :w only saves the file", c.changes(), c.res.pendingSQL)
	}
	if readFile(t, p) != c.editor.Value() {
		t.Fatal("file not written")
	}
}

func TestWriteInAPlainConsoleIsUnchanged(t *testing.T) {
	m, _ := newTestModel(t)
	openLocalConsole(t, m)
	ex(t, m, "w")
	if !strings.Contains(m.status, "nothing to write") {
		t.Fatalf("status %q", m.status)
	}
	ex(t, m, "w!")
	if !strings.Contains(m.status, "nothing to write") {
		t.Fatalf(":w! without a file: status %q", m.status)
	}
}

// touch rewrites p with text and a later mtime.
func touch(t *testing.T, p, text string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
}

func TestWriteAsksWhenTheFileChangedOnDisk(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "select 1;")
	ex(t, m, "e "+p)
	c := fileConsole(t, m)
	c.editor.SetValue("mine")
	touch(t, p, "theirs")
	ex(t, m, "w")
	if !strings.Contains(screen(m), "changed on disk") || readFile(t, p) != "theirs" {
		t.Fatalf("no confirm:\n%s", screen(m))
	}
	press(t, m, "n")
	if readFile(t, p) != "theirs" {
		t.Fatal("n keeps the disk file")
	}
	ex(t, m, "w")
	press(t, m, "y")
	if readFile(t, p) != "mine" {
		t.Fatal("y overwrites")
	}
	touch(t, p, "theirs again")
	ex(t, m, "w!")
	if readFile(t, p) != "mine" {
		t.Fatal(":w! writes without asking")
	}
}

func TestWriteAsTurnsAPlainConsoleIntoAFileConsole(t *testing.T) {
	m, _ := newTestModel(t)
	c := openLocalConsole(t, m)
	c.editor.SetValue("select 1;")
	p := filepath.Join(t.TempDir(), "b.sql")
	ex(t, m, "w "+p)
	if readFile(t, p) != "select 1;" || c.file == nil || c.file.path != p || c.Title() != "b.sql" {
		t.Fatalf("file %+v title %q", c.file, c.Title())
	}
}

func TestWriteAsAPathOpenElsewhereFails(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "")
	ex(t, m, "e "+p)
	openLocalConsole(t, m)
	ex(t, m, "w "+p)
	if !strings.Contains(m.status, "is open in another tab") {
		t.Fatalf("status %q", m.status)
	}
}

func TestWriteAsAnExistingFileAsks(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "b.sql", "keep")
	c := openLocalConsole(t, m)
	c.editor.SetValue("new")
	ex(t, m, "w "+p)
	if !strings.Contains(screen(m), "overwrite") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	press(t, m, "n")
	if readFile(t, p) != "keep" || c.file != nil {
		t.Fatal("n keeps the file and the console plain")
	}
}

func TestWriteCreatesANewFileButNotItsDirectory(t *testing.T) {
	m, _ := newTestModel(t)
	p := filepath.Join(t.TempDir(), "b.sql")
	ex(t, m, "e "+p)
	c := fileConsole(t, m)
	c.editor.SetValue("x")
	ex(t, m, "w")
	if readFile(t, p) != "x" || c.Title() != "b.sql" {
		t.Fatalf("title %q", c.Title())
	}
	ex(t, m, "w "+filepath.Join(t.TempDir(), "missing", "c.sql"))
	if !strings.Contains(c.status, "write c.sql") {
		t.Fatalf("status %q", c.status)
	}
}

func TestWriteQuitClosesOnceWritten(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "")
	ex(t, m, "e "+p)
	fileConsole(t, m).editor.SetValue("x")
	ex(t, m, "wq")
	if len(m.tabs) != 0 || readFile(t, p) != "x" {
		t.Fatalf("tabs %d", len(m.tabs))
	}
	openLocalConsole(t, m)
	ex(t, m, "wq")
	if len(m.tabs) != 1 || !strings.Contains(m.status, ":wq needs a file") {
		t.Fatalf("plain console: tabs %d status %q", len(m.tabs), m.status)
	}
}

func TestWriteFollowsSymlinks(t *testing.T) {
	m, _ := newTestModel(t)
	target := writeSQL(t, "real.sql", "a")
	link := filepath.Join(t.TempDir(), "link.sql")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	ex(t, m, "e "+link)
	fileConsole(t, m).editor.SetValue("b")
	ex(t, m, "w")
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 || readFile(t, target) != "b" {
		t.Fatalf("link %v err %v real %q", info.Mode(), err, readFile(t, target))
	}
}

func TestEditsDuringASaveStayMarked(t *testing.T) {
	m, _ := newTestModel(t)
	ex(t, m, "e "+writeSQL(t, "a.sql", ""))
	c := fileConsole(t, m)
	c.editor.SetValue("first")
	cmd := c.save("", false, false)
	c.editor.SetValue("second")
	drain(t, m, cmd)
	if c.Title() != "a.sql [+]" {
		t.Fatalf("title %q", c.Title())
	}
}
