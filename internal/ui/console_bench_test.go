package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

func BenchmarkConsoleTypingInLargeText(b *testing.B) {
	c := newConsoleTab(1, newFakeBackend(), newFakeStore(), readyCatalogs(), "local",
		db.Namespace{Database: "app"}, config.Settings{MaxRows: 2, MaxColumnWidth: 40}, timings{})
	c.SetSize(120, 40)
	c.editor.SetValue(strings.Repeat("-- q\nSELECT id, name, 'x' FROM users WHERE id = 1;\n", 3000))
	c.Handle(keymap.InsertMode)
	c.View(true)
	for b.Loop() {
		c.Input(uitest.Key("a"))
		c.View(true)
	}
}

func BenchmarkConsoleBackspaceInLargeText(b *testing.B) {
	c := newConsoleTab(1, newFakeBackend(), newFakeStore(), readyCatalogs(), "local",
		db.Namespace{Database: "app"}, config.Settings{MaxRows: 2, MaxColumnWidth: 40}, timings{})
	c.SetSize(120, 40)
	c.editor.SetValue(strings.Repeat("-- q\nSELECT id, name, 'x' FROM users WHERE id = 1;\n", 3000))
	c.Handle(keymap.InsertMode)
	c.View(true)
	for b.Loop() {
		c.Input(uitest.Key("a"))
		c.Input(uitest.Key("backspace"))
		c.View(true)
	}
}
