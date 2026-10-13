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
	"github.com/shepherd-yun/sqlvim/internal/ui/vimedit"
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
	m.Escape()
	if m.Insert() {
		t.Fatal("ExitInsert")
	}
	typeText(m, "zz")
	if strings.Contains(m.Value(), "zz") {
		t.Fatal("NORMAL mode ignores typing")
	}
}

func TestVimKeysMoveAndOffset(t *testing.T) {
	m := New()
	m.SetSize(40, 6)
	m.SetValue("select 1;\nselect 2;")
	for _, k := range []string{"k", "0"} {
		m.Update(uitest.Key(k))
	}
	if m.Offset() != 0 {
		t.Fatalf("start offset = %d", m.Offset())
	}
	if r := m.Update(uitest.Key("w")); !r.Handled || m.Offset() != len("select ") {
		t.Fatalf("w: offset %d", m.Offset())
	}
	m.Update(uitest.Key("G"))
	m.Update(uitest.Key("$"))
	if m.Offset() != len(m.Value())-1 {
		t.Fatalf("G$ offset = %d", m.Offset())
	}
	if r := m.Update(uitest.Key("enter")); r.Handled {
		t.Fatal("enter is the console's (run)")
	}
	m.Top()
	if m.Offset() != 0 {
		t.Fatal("Top")
	}
	m.Bottom()
	if m.Offset() != len(m.Value())-1 {
		t.Fatal("Bottom stays on the last rune in NORMAL mode")
	}
}

func TestKeyOf(t *testing.T) {
	for _, c := range []struct {
		key  tea.KeyPressMsg
		want vimedit.Key
	}{
		{uitest.Key("G"), vimedit.Key{Name: "G", Text: "G"}},
		{uitest.Key("space"), vimedit.Key{Name: "space", Text: " "}},
		{uitest.Key("ctrl+r"), vimedit.Key{Name: "ctrl+r"}},
		{uitest.Key("esc"), vimedit.Key{Name: "esc"}},
	} {
		if got := KeyOf(c.key); got != c.want {
			t.Errorf("KeyOf(%s) = %+v, want %+v", c.key, got, c.want)
		}
	}
}

func TestViewShowsTheSelection(t *testing.T) {
	m := New()
	m.SetSize(30, 2)
	m.SetValue("select id")
	m.Top()
	for _, k := range []string{"v", "e"} {
		m.Update(uitest.Key(k))
	}
	v := m.View()
	if !strings.Contains(v, theme.Visual.Render("s")) || !strings.Contains(v, theme.Visual.Render("l")) {
		t.Fatalf("selected runes are drawn as selected: %q", v)
	}
	if strings.Contains(v, theme.Visual.Render("i")) {
		t.Fatalf("only the selection: %q", v)
	}
	m.Escape()
	if strings.Contains(m.View(), theme.Visual.Render("s")) {
		t.Fatal("esc clears the selection")
	}
}

func TestOffsetCountsBytes(t *testing.T) {
	m := New()
	m.SetSize(40, 6)
	m.SetValue("select '李雷';")
	m.Top()
	m.Update(uitest.Key("9"))
	m.Update(uitest.Key("l")) // past "select '李"
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
	if x, y := m.CursorPos(); x != 9 || y != 1 {
		t.Fatalf("cursor on the last rune at %d,%d; want 9,1", x, y)
	}
	m.SetValue("abcdefghi") // in INSERT mode the cursor can sit after a full row: it goes to the next one
	m.EnterInsert()
	m.Bottom()
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
	m.Update(uitest.Key("l"))
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
	if line, col := m.Cursor(); line != 2 || col != 0 {
		t.Fatalf("clamped to the last rune in NORMAL mode = %d,%d", line, col)
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
