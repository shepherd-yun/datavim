package editor

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

func typeText(m *Model, s string) {
	for _, k := range uitest.Type(s) {
		m.Update(k)
	}
}

func TestInsertModeEditsAndNormalModeDoesNot(t *testing.T) {
	m := New()
	m.SetSize(40, 6)
	typeText(m, "x")
	if m.Value() != "" {
		t.Fatal("NORMAL mode ignores typing")
	}
	m.EnterInsert()
	typeText(m, "select 1;")
	m.Update(uitest.Key("enter"))
	typeText(m, "select 2;")
	if got := m.Value(); got != "select 1;\nselect 2;" {
		t.Fatalf("value = %q", got)
	}
	if m.Offset() != len(m.Value()) {
		t.Fatalf("offset = %d", m.Offset())
	}
	m.Update(tea.PasteMsg{Content: " -- x"})
	if !strings.HasSuffix(m.Value(), "select 2; -- x") {
		t.Fatalf("paste: %q", m.Value())
	}
	m.ExitInsert()
	if m.Insert() {
		t.Fatal("ExitInsert")
	}
	typeText(m, "zz")
	if strings.Contains(m.Value(), "zz") {
		t.Fatal("NORMAL mode ignores typing")
	}
}

func TestMovesAndOffset(t *testing.T) {
	m := New()
	m.SetSize(40, 6)
	m.SetValue("select 1;\nselect 2;")
	m.Up(1)
	m.LineStart()
	if m.Offset() != 0 {
		t.Fatalf("start offset = %d", m.Offset())
	}
	m.Right()
	m.Right()
	if m.Offset() != 2 {
		t.Fatalf("right offset = %d", m.Offset())
	}
	m.Left()
	if m.Offset() != 1 {
		t.Fatalf("left offset = %d", m.Offset())
	}
	m.Down(1)
	m.LineEnd()
	if m.Offset() != len(m.Value()) {
		t.Fatalf("end offset = %d", m.Offset())
	}
	m.Top()
	if m.Offset() != 0 {
		t.Fatal("Top")
	}
	m.Bottom()
	if m.Offset() != len(m.Value()) {
		t.Fatal("Bottom")
	}
}

func TestOffsetCountsBytes(t *testing.T) {
	m := New()
	m.SetSize(40, 6)
	m.SetValue("select '李雷';")
	m.Top()
	for range 9 { // past "select '李"
		m.Right()
	}
	if got, want := m.Offset(), len("select '李"); got != want {
		t.Fatalf("offset = %d, want %d", got, want)
	}
}

func TestViewFitsAndShowsText(t *testing.T) {
	m := New()
	m.SetSize(30, 4)
	m.SetValue("select 1")
	v := ansi.Strip(m.View())
	if !strings.Contains(v, "select 1") {
		t.Fatalf("view:\n%s", v)
	}
	for _, l := range strings.Split(v, "\n") {
		if ansi.StringWidth(l) > 30 {
			t.Fatalf("line wider than 30: %q", l)
		}
	}
	m.SetSize(0, 0)
	_ = m.View()
}
