package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
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
	press(t, m, "space", "b") // the leader still works
	if m.showTree {
		t.Fatal("<space>b hides the tree")
	}
}

func TestConsoleShowsVimModesAndPendingKeys(t *testing.T) {
	m, _ := newTestModel(t)
	c := openLocalConsole(t, m)
	c.editor.SetValue("select 1")
	press(t, m, "v")
	if s := screen(m); !strings.Contains(s, " VISUAL ") {
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
