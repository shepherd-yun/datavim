package editor

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

func BenchmarkViewLargeText(b *testing.B) {
	m := New()
	m.SetSize(120, 40)
	m.SetValue(strings.Repeat("SELECT id, name, 'x' FROM users WHERE id = 1;\n", 6000))
	m.SetCursor(3000, 10)
	m.EnterInsert()
	x := uitest.Key("x")
	for b.Loop() {
		m.Update(x) // the text changes every frame, as when typing
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
