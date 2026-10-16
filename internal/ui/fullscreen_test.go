package ui

import (
	"strings"
	"testing"
)

func TestFullScreenFollowsTheFocus(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	c := openLocalConsole(t, m)
	normal := c.width
	press(t, m, "+")
	if !m.full || strings.Contains(screen(m), "Connections") {
		t.Fatalf("+ fills the screen with the tabs:\n%s", screen(m))
	}
	if c.width != m.width-2 {
		t.Fatalf("tab width %d, want %d", c.width, m.width-2)
	}
	press(t, m, "g", "T")
	if m.active != 0 || !m.full || strings.Contains(screen(m), "Connections") {
		t.Fatal("switching tabs keeps full screen")
	}
	lines(t, m)
	press(t, m, "ctrl+h")
	lines(t, m)
	s := screen(m)
	if !strings.Contains(s, "Connections") || strings.Contains(s, "[1]") {
		t.Fatalf("full screen follows the focus to the tree:\n%s", s)
	}
	press(t, m, "ctrl+l", "g", "t", "-")
	if m.full || !strings.Contains(screen(m), "Connections") || c.width != normal {
		t.Fatalf("- goes back: width %d, want %d", c.width, normal)
	}
	for _, k := range [][2]string{{"=", "_"}, {"+", "_"}, {"=", "-"}} {
		press(t, m, k[0])
		if !m.full {
			t.Fatalf("%s enters full screen", k[0])
		}
		press(t, m, k[1])
		if m.full {
			t.Fatalf("%s leaves full screen", k[1])
		}
	}
}

func TestFullScreenKeepsTheTreeSetting(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	press(t, m, "space", "e", "+", "-")
	if m.showTree {
		t.Fatal("leaving full screen keeps the tree hidden")
	}
}

func TestFullScreenConsoleShowsTheFocusedHalf(t *testing.T) {
	m, _ := newTestModel(t)
	c := openLocalConsole(t, m)
	normal := c.editorH
	press(t, m, "+") // NORMAL mode: + is not an editor key
	if !m.full {
		t.Fatal("+ in the editor's NORMAL mode enters full screen")
	}
	lines(t, m)
	if c.editorH != c.height-1 || strings.Contains(screen(m), "Results") {
		t.Fatalf("the editor fills the tab: %d of %d\n%s", c.editorH, c.height, screen(m))
	}
	press(t, m, "ctrl+j")
	lines(t, m)
	s := screen(m)
	if !strings.Contains(s, "Results") || !strings.Contains(s, "[1]") || c.resultsH != c.height-1 {
		t.Fatalf("the results fill the tab under the tab bar: %d of %d\n%s", c.resultsH, c.height, s)
	}
	press(t, m, "_")
	if c.editorH != normal || !strings.Contains(screen(m), "Results") {
		t.Fatal("_ splits the console again")
	}
	press(t, m, "ctrl+k", "i", "+", "-")
	if m.full || c.editor.Value() != "+-" {
		t.Fatalf("INSERT mode types + and -: %q", c.editor.Value())
	}
}

// lines asserts the screen fills the terminal exactly.
func lines(t *testing.T, m *Model) {
	t.Helper()
	if n := strings.Count(screen(m), "\n") + 1; n != m.height {
		t.Fatalf("%d lines, want %d:\n%s", n, m.height, screen(m))
	}
}

func TestFullScreenRedisConsole(t *testing.T) {
	m, _ := newTestModel(t)
	c := openRConsole(t, m, 0)
	press(t, m, "+")
	if c.editorH != c.height-1 {
		t.Fatalf("the editor fills the tab: %d of %d", c.editorH, c.height)
	}
	press(t, m, "ctrl+j")
	if c.resultsH != c.height-1 {
		t.Fatalf("the results fill the tab: %d of %d", c.resultsH, c.height)
	}
	lines(t, m)
	press(t, m, "ctrl+k")
	lines(t, m)
}
