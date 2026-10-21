package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/uitest"
)

func TestConsoleEditsWithVimKeys(t *testing.T) {
	m, b := newTestModel(t)
	c := openLocalConsole(t, m)
	press(t, m, "i")
	typeKeys(t, m, "select 1;")
	press(t, m, "enter")
	typeKeys(t, m, "select 2;")
	press(t, m, "esc", "g", "g", "d", "d")
	if got := c.editor.Value(); got != "select 2;" {
		t.Fatalf("dd: %q", got)
	}
	press(t, m, "u")
	if got := c.editor.Value(); got != "select 1;\nselect 2;" {
		t.Fatalf("u: %q", got)
	}
	press(t, m, "2", "j") // a count, not "go to tab 2"
	if line, _ := c.editor.Cursor(); line != 1 || m.active != 0 {
		t.Fatalf("2j: line %d, tab %d", line, m.active)
	}
	press(t, m, "enter")
	if got := b.conn.lastQuery(); got != "select 2" {
		t.Fatalf("enter still runs the statement: %q", got)
	}
	press(t, m, "space", "e") // the leader still works
	if m.showTree {
		t.Fatal("<space>e hides the tree")
	}
}

func TestConsoleShowsVimModesAndPendingKeys(t *testing.T) {
	m, _ := newTestModel(t)
	c := openLocalConsole(t, m)
	c.editor.SetValue("select 1")
	press(t, m, "v")
	if s := screen(m); !strings.Contains(s, " VISUAL ") || !strings.Contains(s, "enter runs the selection") {
		t.Fatalf("VISUAL:\n%s", s)
	}
	press(t, m, "V")
	if !strings.Contains(screen(m), " V-LINE ") {
		t.Fatalf("V-LINE:\n%s", screen(m))
	}
	press(t, m, "esc", "\"", "a", "2", "d")
	if !strings.Contains(screen(m), `"a2d`) {
		t.Fatalf("pending keys:\n%s", screen(m))
	}
	press(t, m, "esc")
	if strings.Contains(screen(m), `"a2d`) {
		t.Fatal("esc clears the pending keys")
	}
}

func TestConsoleRunsTheVisualSelection(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.editor.SetValue("select 1;\nupdate t set a = 1 where id = 1;\nselect 3;")
	c.editor.Top()
	for _, k := range []string{"V", "j"} {
		c.TakeKey(uitest.Key(k))
	}
	runTab(t, c, c.Handle(keymap.RunStatement))
	if got := b.conn.execList(); !slices.Contains(got, "update t set a = 1 where id = 1") {
		t.Fatalf("execs = %v", got)
	}
	if strings.Contains(b.conn.lastQuery(), "select 3") {
		t.Fatal("only the selection runs")
	}
	if c.editor.Visual() {
		t.Fatal("running the selection leaves VISUAL mode")
	}
}

func TestSelectedStatementsPointIntoTheText(t *testing.T) {
	text := "select 1;\nselect 2; select 3;"
	from := strings.Index(text, "select 2")
	stmts := selectedStatements(text, from, len(text), 0)
	if len(stmts) != 2 || text[stmts[1].Start:stmts[1].End] != "select 3" {
		t.Fatalf("stmts = %+v", stmts)
	}
}

func TestGtSwitchesTabs(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m) // tab 1: users data
	press(t, m, "ctrl+h", "space", "c", "esc")
	if m.active != 1 {
		t.Fatalf("console is tab 2, active %d", m.active)
	}
	press(t, m, "g", "t") // wraps to the first tab
	if m.active != 0 {
		t.Fatalf("gt: active %d", m.active)
	}
	press(t, m, "2") // from the data tab, 2 still goes to tab 2
	press(t, m, "g", "T")
	if m.active != 0 {
		t.Fatalf("gT from the console: active %d", m.active)
	}
	press(t, m, "2", "1", "g", "t") // in the console, 1gt goes to tab 1
	if m.active != 0 {
		t.Fatalf("1gt: active %d", m.active)
	}
	press(t, m, "2", "9", "g", "t")
	if m.active != 1 {
		t.Fatal("9gt without a tab 9 stays")
	}
}

func TestTabCyclingFromEveryTab(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)                            // tab 1: users data
	press(t, m, "ctrl+h", "j", "enter")        // tab 2: view "active"
	press(t, m, "ctrl+h", "space", "c", "esc") // tab 3: console
	if len(m.tabs) != 3 || m.active != 2 {
		t.Fatalf("tabs=%d active=%d", len(m.tabs), m.active)
	}
	steps := []struct {
		keys []string
		want int
	}{
		{[]string{"L"}, 0}, // from the console, wraps forward
		{[]string{"H"}, 2}, // from a data tab, wraps backward
		{[]string{"H"}, 1},
		{[]string{"1"}, 0},
		{[]string{"g", "T"}, 2}, // gT from a data tab wraps
		{[]string{"L"}, 0},
		{[]string{"L"}, 1},
		{[]string{"g", "t"}, 2}, // gt from a data tab
	}
	for i, s := range steps {
		press(t, m, s.keys...)
		if m.active != s.want {
			t.Fatalf("step %d %v: active %d, want %d", i, s.keys, m.active, s.want)
		}
	}
	press(t, m, "ctrl+h", "L") // from the tree too, wrapping past the console
	if m.active != 0 || m.focus != focusTabs {
		t.Fatalf("L from the tree goes to the next tab: active %d", m.active)
	}
}

func TestConsoleClipboardRegister(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.editor.SetValue("select 1")
	c.editor.Top()
	clip := captureClipboard(t)
	for _, k := range []string{"\"", "+", "y", "y"} {
		cmd, used := c.TakeKey(uitest.Key(k))
		if !used {
			t.Fatalf("%s not used", k)
		}
		runTab(t, c, cmd)
	}
	if *clip != "select 1\n" {
		t.Fatalf("\"+yy copies the line: %q", *clip)
	}
	fakeClipboard(t, "-- x\n")
	var cmd tea.Cmd
	for _, k := range []string{"\"", "+", "p"} {
		cmd, _ = c.TakeKey(uitest.Key(k))
	}
	if c.clipPaste == nil {
		t.Fatal("\"+p waits for the clipboard")
	}
	runTab(t, c, cmd)
	if got := c.editor.Value(); got != "select 1\n-- x" {
		t.Fatalf("pasted: %q", got)
	}
	c.Update(clipboardTextMsg{tabID: c.id, text: "late"})
	if strings.Contains(c.editor.Value(), "late") {
		t.Fatal("an answer nobody waits for is dropped")
	}
}

func TestClipboardAnswerGoesToTheActiveConsole(t *testing.T) {
	m, _ := newTestModel(t)
	c := openLocalConsole(t, m)
	c.editor.SetValue("x")
	press(t, m, "\"", "+", "P")
	m.Update(tea.ClipboardMsg{Content: "y"})
	if got := c.editor.Value(); got != "yx" {
		t.Fatalf("OSC 52 answer pasted: %q", got)
	}
}

func TestExternalEditIsUndoable(t *testing.T) {
	c := newConsole(t, newFakeBackend(), "local")
	c.editor.SetValue("select 1")
	runTab(t, c, c.Update(editTextMsg{tabID: c.id, text: "select 2\n"}))
	c.TakeKey(uitest.Key("u"))
	if got := c.editor.Value(); got != "select 1" {
		t.Fatalf("u after $EDITOR: %q", got)
	}
}

func TestHelpListsVimKeysInTheConsole(t *testing.T) {
	m, _ := newTestModel(t)
	openLocalConsole(t, m)
	press(t, m, "?")
	s := screen(m)
	for _, want := range []string{"Run statement under cursor", "Undo, redo", "gt gT"} {
		if !strings.Contains(s, want) {
			t.Errorf("help missing %q:\n%s", want, s)
		}
	}
}
