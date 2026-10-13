package vimedit

import (
	"strings"
	"testing"
)

// parse splits s at "|" into the text and the cursor before that point.
func parse(t *testing.T, s string) (string, Pos) {
	t.Helper()
	i := strings.Index(s, "|")
	if i < 0 {
		t.Fatalf("no cursor in %q", s)
	}
	text := s[:i] + s[i+1:]
	b := newBuffer(text)
	return text, b.pos(i)
}
