package vimedit

import (
	"strings"
	"testing"
)

func BenchmarkTypingInLargeText(b *testing.B) {
	e := New()
	e.SetText(strings.Repeat("SELECT id, name, 'x' FROM users WHERE id = 1;\n", 6000))
	e.SetCursor(Pos{3000, 10})
	e.Feed(Key{Name: "i"})
	for b.Loop() {
		e.Feed(Key{Name: "a", Text: "a"})
		_ = e.Text()
		_ = e.Offset()
	}
}

func BenchmarkDeleteLineInLargeText(b *testing.B) {
	e := New()
	e.SetText(strings.Repeat("SELECT id, name, 'x' FROM users WHERE id = 1;\n", 6000))
	e.SetCursor(Pos{3000, 0})
	for b.Loop() {
		e.Feed(Key{Name: "y"})
		e.Feed(Key{Name: "y"})
		e.Feed(Key{Name: "p"})
		e.Feed(Key{Name: "d"})
		e.Feed(Key{Name: "d"})
	}
}
