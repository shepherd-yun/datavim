package theme

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/shepherd-yun/datavim/internal/ui/highlight"
)

func TestApplyOverridesColors(t *testing.T) {
	t.Cleanup(func() { _ = Apply(nil) })
	if err := Apply(map[string]string{"focus": "#ff8800", "string": "120", "visual_bg": "#abc"}); err != nil {
		t.Fatal(err)
	}
	if ColorFocus != lipgloss.Color("#ff8800") {
		t.Fatalf("ColorFocus = %v", ColorFocus)
	}
	if got := Syntax(highlight.Keyword).GetForeground(); got != lipgloss.Color("#ff8800") {
		t.Fatalf("keyword color = %v, want the focus color", got)
	}
	if got := Syntax(highlight.String).GetForeground(); got != lipgloss.Color("120") {
		t.Fatalf("string color = %v", got)
	}
	if got := Visual.GetBackground(); got != lipgloss.Color("#abc") {
		t.Fatalf("visual bg = %v", got)
	}
	if got := Muted.GetForeground(); got != lipgloss.Color("8") {
		t.Fatalf("untouched slot changed: %v", got)
	}
}

func TestApplyNilRestoresDefaults(t *testing.T) {
	if err := Apply(map[string]string{"accent": "1"}); err != nil {
		t.Fatal(err)
	}
	if err := Apply(nil); err != nil {
		t.Fatal(err)
	}
	if ActiveTab.GetForeground() != lipgloss.Color("14") {
		t.Fatalf("ActiveTab = %v, want default 14", ActiveTab.GetForeground())
	}
}

func TestApplyRejectsBadColors(t *testing.T) {
	t.Cleanup(func() { _ = Apply(nil) })
	for _, tc := range []struct{ slot, value, want string }{
		{"focus", "bleu", `theme.focus: invalid color "bleu"`},
		{"focus", "#12345", `theme.focus: invalid color "#12345"`},
		{"error", "256", `theme.error: invalid color "256"`},
		{"error", "-1", `theme.error: invalid color "-1"`},
		{"focsu", "1", `theme.focsu: unknown key (want one of accent, border,`},
	} {
		err := Apply(map[string]string{"accent": "1", tc.slot: tc.value})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Apply(%s=%q) = %v, want %q", tc.slot, tc.value, err, tc.want)
		}
		if ColorAccent != lipgloss.Color("14") {
			t.Errorf("a rejected theme must not be half applied: accent = %v", ColorAccent)
		}
	}
}
