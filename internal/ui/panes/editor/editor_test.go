package editor

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/ui/highlight"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
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

func lines(m *Model) []string { return strings.Split(ansi.Strip(m.View()), "\n") }

func TestViewWrapsAndTracksCursor(t *testing.T) {
	m := New()
	m.SetSize(12, 4) // gutter " 1 " (3 cells), 9 cells of text
	m.SetValue("abcdefghijklmnop")
	got := lines(m)
	want := []string{" 1 abcdefghi", "   jklmnop  ", strings.Repeat(" ", 12), strings.Repeat(" ", 12)}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("view (-want +got):\n%s", diff)
	}
	if x, y := m.CursorPos(); x != 10 || y != 1 {
		t.Fatalf("cursor at %d,%d; want 10,1", x, y)
	}
	m.SetValue("abcdefghi") // the cursor sits after a full row: it goes to the next one
	_ = m.View()
	if x, y := m.CursorPos(); x != 3 || y != 1 {
		t.Fatalf("cursor after full row at %d,%d; want 3,1", x, y)
	}
}

func TestViewWrapsWideRunesAndTracksCursor(t *testing.T) {
	m := New()
	m.SetSize(12, 3)
	m.SetValue("abcdefgh李")
	got := lines(m)
	if got[0] != " 1 abcdefgh " || !strings.HasPrefix(got[1], "   李") {
		t.Fatalf("a wide rune that does not fit moves down:\n%q", got)
	}
	m.SetValue("李雷abc")
	m.Top()
	m.Right()
	_ = m.View()
	if x, y := m.CursorPos(); x != 5 || y != 0 {
		t.Fatalf("cursor after one wide rune at %d,%d; want 5,0", x, y)
	}
	for _, l := range lines(m) {
		if w := ansi.StringWidth(l); w != 12 {
			t.Fatalf("every line is exactly 12 wide, got %d: %q", w, l)
		}
	}
}

func TestViewScrollsToTheCursor(t *testing.T) {
	m := New()
	m.SetSize(20, 3)
	var ls []string
	for i := 1; i <= 10; i++ {
		ls = append(ls, fmt.Sprintf("l%d", i))
	}
	m.SetValue(strings.Join(ls, "\n"))
	v := ansi.Strip(m.View())
	if !strings.Contains(v, "10 l10") || strings.Contains(v, " 1 l1\n") {
		t.Fatalf("bottom visible:\n%s", v)
	}
	if _, y := m.CursorPos(); y != 2 {
		t.Fatalf("cursor row = %d", y)
	}
	m.Top()
	if v := ansi.Strip(m.View()); !strings.HasPrefix(v, " 1 l1") {
		t.Fatalf("top visible:\n%s", v)
	}
}

func TestViewHighlightsAndMemoizes(t *testing.T) {
	m := New()
	m.SetSize(30, 2)
	calls := 0
	m.SetHighlighter(func(s string) []highlight.Span {
		calls++
		return []highlight.Span{{Start: 0, End: 6, Class: highlight.Keyword}, {Start: 6, End: len(s), Class: highlight.Plain}}
	})
	m.SetValue("select 1")
	v := m.View()
	if !strings.Contains(v, theme.Syntax(highlight.Keyword).Render("select")) {
		t.Fatalf("keyword not styled: %q", v)
	}
	_ = m.View()
	if calls != 1 {
		t.Fatalf("highlighter ran %d times for unchanged text", calls)
	}
}

func TestPlaceholderAndCells(t *testing.T) {
	m := New()
	m.SetSize(40, 2)
	if v := ansi.Strip(m.View()); !strings.Contains(v, "i to write SQL") {
		t.Fatalf("placeholder:\n%s", v)
	}
	for r, want := range map[rune]cell{
		'\t':   {text: "    ", width: 4},
		'\x1b': {text: "�", width: 1},
		'李':    {text: "李", width: 2},
		'a':    {text: "a", width: 1},
	} {
		if got := cellOf(r, highlight.Plain); got != want {
			t.Errorf("cellOf(%q) = %+v, want %+v", r, got, want)
		}
	}
}

func TestCursorAPI(t *testing.T) {
	m := New()
	m.SetSize(40, 6)
	m.SetValue("ab\ncdé\nf")
	m.SetCursorOffset(len("ab\ncd"))
	if line, col := m.Cursor(); line != 1 || col != 2 || m.Offset() != 5 {
		t.Fatalf("cursor = %d,%d offset %d", line, col, m.Offset())
	}
	m.SetCursor(9, 9)
	if line, col := m.Cursor(); line != 2 || col != 1 {
		t.Fatalf("clamped cursor = %d,%d", line, col)
	}
	m.SetValue("select us from t")
	m.SetCursorOffset(len("select us"))
	m.ReplaceBeforeCursor(len("select "), "users")
	if m.Value() != "select users from t" || m.Offset() != len("select users") {
		t.Fatalf("replace: %q at %d", m.Value(), m.Offset())
	}
}

func TestTypingEditsKeepTheCursor(t *testing.T) {
	m := New()
	m.SetSize(40, 5)
	m.EnterInsert()
	typeText(m, "ab")
	m.Update(uitest.Key("enter"))
	typeText(m, "李c")
	if m.Value() != "ab\n李c" || m.Offset() != len("ab\n李c") {
		t.Fatalf("typed %q at %d", m.Value(), m.Offset())
	}
	m.Update(uitest.Key("backspace"))
	m.Update(uitest.Key("backspace"))
	if m.Value() != "ab\n" || m.Offset() != 3 {
		t.Fatalf("backspace over a wide rune: %q at %d", m.Value(), m.Offset())
	}
	m.Update(uitest.Key("backspace")) // joins the lines
	if m.Value() != "ab" || m.Offset() != 2 {
		t.Fatalf("backspace at a line start: %q at %d", m.Value(), m.Offset())
	}
	m.Top()
	m.Update(uitest.Key("backspace"))
	if m.Value() != "ab" || m.Offset() != 0 {
		t.Fatalf("backspace at the start does nothing: %q", m.Value())
	}
	m.Update(tea.PasteMsg{Content: "x\ny"})
	if m.Value() != "x\nyab" || m.Offset() != 3 {
		t.Fatalf("paste: %q at %d", m.Value(), m.Offset())
	}
}
