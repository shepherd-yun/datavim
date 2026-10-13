package theme

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/ui/highlight"
)

func TestBox(t *testing.T) {
	got := strings.Split(ansi.Strip(Box("T", "a\nb\nc", 10, 4, false)), "\n")
	want := []string{
		"┌ T ─────┐",
		"│a       │",
		"│b       │",
		"└────────┘",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s", strings.Join(got, "\n"))
	}
}

func TestBoxLongTitleAndTinySizes(t *testing.T) {
	for _, line := range strings.Split(ansi.Strip(Box("a very long title", "", 8, 3, true)), "\n") {
		if w := ansi.StringWidth(line); w != 8 {
			t.Errorf("line %q width %d, want 8", line, w)
		}
	}
	if Box("t", "x", 1, 5, false) != "" || Box("t", "x", 5, 1, false) != "" {
		t.Error("too small boxes render nothing")
	}
}

func TestFrameDangerIsRed(t *testing.T) {
	red := lipgloss.NewStyle().Foreground(ColorError).Render("┌")
	for _, focused := range []bool{false, true} {
		if got := Frame("t", "x", 10, 3, focused, true); !strings.HasPrefix(got, red) {
			t.Errorf("focused=%v: danger frame is not red: %q", focused, got)
		}
		if got := Box("t", "x", 10, 3, focused); strings.HasPrefix(got, red) {
			t.Errorf("focused=%v: plain box is red", focused)
		}
	}
	if ansi.Strip(Frame("t", "x", 10, 3, false, true)) != ansi.Strip(Box("t", "x", 10, 3, false)) {
		t.Error("only the color differs")
	}
}

func TestHighlightLines(t *testing.T) {
	src := "SELECT 1 -- c\nFROM t"
	spans := []highlight.Span{
		{Start: 0, End: 6, Class: highlight.Keyword},
		{Start: 6, End: 9, Class: highlight.Plain},
		{Start: 9, End: 14, Class: highlight.Comment}, // "-- c\n" crosses the line break
		{Start: 14, End: 18, Class: highlight.Keyword},
		{Start: 18, End: 20, Class: highlight.Plain},
	}
	lines := HighlightLines(src, spans)
	if len(lines) != 2 || ansi.Strip(lines[0]) != "SELECT 1 -- c" || ansi.Strip(lines[1]) != "FROM t" {
		t.Fatalf("lines = %q", lines)
	}
	if !strings.Contains(lines[0], Syntax(highlight.Keyword).Render("SELECT")) ||
		!strings.Contains(lines[1], Syntax(highlight.Keyword).Render("FROM")) {
		t.Fatalf("keywords are styled: %q", lines)
	}
	if got := HighlightLines("", nil); len(got) != 1 || got[0] != "" {
		t.Fatalf("empty: %q", got)
	}
}

func TestKindTag(t *testing.T) {
	for _, k := range []string{"table", "view", "column", "routine", "schema", "database"} {
		if KindTag(k).Render("x") == "x" {
			t.Errorf("%s has no color", k)
		}
	}
	if KindTag("other").Render("x") != "x" {
		t.Error("unknown kinds are plain")
	}
}
