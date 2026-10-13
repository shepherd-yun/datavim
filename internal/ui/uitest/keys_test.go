package uitest

import "testing"

func TestKeyStringRoundTrip(t *testing.T) {
	for _, s := range []string{
		"j", "G", "$", "?", ":", "[", "space", "enter", "esc", "tab",
		"shift+tab", "backspace", "ctrl+c", "ctrl+d", "up", "down", "left", "right",
		"home", "end", "delete", "alt+left", "alt+right", "alt+b", "ctrl+w",
	} {
		if got := Key(s).String(); got != s {
			t.Errorf("Key(%q).String() = %q", s, got)
		}
	}
}
