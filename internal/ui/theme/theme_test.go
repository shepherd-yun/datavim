package theme

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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
