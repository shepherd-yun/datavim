package editor

import (
	"strings"
	"testing"
)

func BenchmarkViewLargeText(b *testing.B) {
	m := New()
	m.SetSize(120, 40)
	m.SetValue(strings.Repeat("SELECT id, name, 'x' FROM users WHERE id = 1;\n", 6000))
	i := 0
	for b.Loop() {
		i++
		m.InsertString("x") // the text changes every frame, as when typing
		_ = m.View()
	}
}

func BenchmarkSetCursorInLargeText(b *testing.B) {
	m := New()
	m.SetSize(120, 40)
	m.SetValue(strings.Repeat("SELECT id, name, 'x' FROM users WHERE id = 1;\n", 3000))
	for b.Loop() {
		m.SetCursor(2999, 3)
	}
}
