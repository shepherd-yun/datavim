package uitest

import "testing"

func TestKeyStringRoundTrip(t *testing.T) {
	for _, s := range []string{
		"j", "G", "$", "?", ":", "[", "space", "enter", "esc", "tab",
		"shift+tab", "backspace", "ctrl+c", "ctrl+d", "up", "down", "left", "right",
	} {
		if got := Key(s).String(); got != s {
			t.Errorf("Key(%q).String() = %q", s, got)
		}
	}
}
