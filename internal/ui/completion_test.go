package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/meta"
	"github.com/shepherd-yun/sqlvim/internal/ui/highlight"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/complete"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

// readyCatalogs is a cache with fakeCatalog loaded for local.
func readyCatalogs() *meta.Cache {
	c := meta.NewCache()
	gen, _ := c.Start(localKey)
	c.Finish(localKey, gen, meta.NewSnapshot(localKey, fakeCatalog(), time.Now()), nil)
	return c
}

// insertConsole is a local console on app with the catalog loaded, in INSERT mode.
func insertConsole(t *testing.T) *consoleTab {
	t.Helper()
	c := newConsoleTab(1, newFakeBackend(), newFakeStore(), readyCatalogs(), "local",
		db.Namespace{Database: "app"}, config.Settings{MaxRows: 2, MaxColumnWidth: 40}, timings{})
	c.SetSize(80, 24)
	c.Handle(keymap.InsertMode)
	return c
}

func typeIn(t *testing.T, c *consoleTab, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if len([]rune(k)) == 1 || strings.Contains(k, "+") || k == "enter" || k == "esc" || k == "tab" || k == "left" || k == "up" || k == "down" {
			runTab(t, c, c.Input(uitest.Key(k)))
			continue
		}
		for _, kp := range uitest.Type(k) {
			runTab(t, c, c.Input(kp))
		}
	}
}

func TestConsoleCompletesTablesAndColumns(t *testing.T) {
	c := insertConsole(t)
	typeIn(t, c, "select * from us")
	if c.popup == nil || c.popup.Chosen().Label != "users" {
		t.Fatalf("popup = %+v", c.popup)
	}
	if !strings.Contains(consoleView(c), "table users") {
		t.Fatalf("popup drawn:\n%s", consoleView(c))
	}
	typeIn(t, c, "enter")
	if c.editor.Value() != "select * from users" || c.popup != nil {
		t.Fatalf("accepted: %q, popup %v", c.editor.Value(), c.popup)
	}
	typeIn(t, c, " where na")
	if c.popup == nil || c.popup.Chosen().Label != "name" {
		t.Fatalf("column popup = %+v", c.popup)
	}
	typeIn(t, c, "enter")
	if c.editor.Value() != "select * from users where name" {
		t.Fatalf("value = %q", c.editor.Value())
	}
}

func TestConsoleCompletesAliasColumns(t *testing.T) {
	c := insertConsole(t)
	c.editor.SetValue(" from users u")
	c.editor.Top()
	typeIn(t, c, "select u.")
	if c.popup == nil {
		t.Fatal("a popup after alias.")
	}
	got := c.popup.Chosen()
	if got.Label != "id" || got.Kind != complete.Column {
		t.Fatalf("first column = %+v", got)
	}
}

func TestConsoleCompletionQuotesIdentifiersAndKeywords(t *testing.T) {
	c := insertConsole(t)
	typeIn(t, c, "select * from or", "ctrl+y")
	if c.editor.Value() != "select * from `order`" {
		t.Fatalf("quoted: %q", c.editor.Value())
	}
	c2 := insertConsole(t)
	typeIn(t, c2, "sel", "ctrl+y")
	if c2.editor.Value() != "select" {
		t.Fatalf("lower-case keyword: %q", c2.editor.Value())
	}
	c3 := insertConsole(t)
	typeIn(t, c3, "SEL", "ctrl+y")
	if c3.editor.Value() != "SELECT" {
		t.Fatalf("upper-case keyword: %q", c3.editor.Value())
	}
}

func TestConsoleCompletionKeys(t *testing.T) {
	c := insertConsole(t)
	typeIn(t, c, "select * from ", "ctrl+n")
	if c.popup == nil || c.popup.Len() != 4 {
		t.Fatalf("ctrl+n opens the popup with every table and namespace: %+v", c.popup)
	}
	typeIn(t, c, "down", "down", "up", "enter")
	if c.editor.Value() != "select * from `order`" {
		t.Fatalf("down / up select, enter accepts: %q", c.editor.Value())
	}

	c = insertConsole(t)
	typeIn(t, c, "select * from ", "ctrl+n", "tab", "tab", "shift+tab", "ctrl+y")
	if c.editor.Value() != "select * from `order`" {
		t.Fatalf("tab / shift+tab select, ctrl+y accepts: %q", c.editor.Value())
	}

	c = insertConsole(t)
	typeIn(t, c, "select * from us", "esc")
	if c.popup != nil || !c.CapturesInput() {
		t.Fatal("esc closes the popup and stays in INSERT mode")
	}
	typeIn(t, c, "esc")
	if c.CapturesInput() {
		t.Fatal("a second esc leaves INSERT mode")
	}

	c = insertConsole(t)
	typeIn(t, c, "select 1", "enter")
	if c.popup != nil || c.editor.Value() != "select 1\n" {
		t.Fatalf("without a popup enter inserts a newline: %q", c.editor.Value())
	}

	c = insertConsole(t)
	typeIn(t, c, "select * from us", "left")
	if c.popup != nil {
		t.Fatal("moving the cursor closes the popup")
	}

	c = insertConsole(t)
	typeIn(t, c, "select 'us")
	if c.popup != nil {
		t.Fatal("no completion inside strings")
	}
}

func TestConsoleWithoutCatalogOffersKeywords(t *testing.T) {
	c := newConsoleTab(1, newFakeBackend(), newFakeStore(), meta.NewCache(), "local",
		db.Namespace{Database: "app"}, config.Settings{MaxRows: 2, MaxColumnWidth: 40}, timings{})
	c.SetSize(80, 24)
	c.Handle(keymap.InsertMode)
	typeIn(t, c, "select * from us")
	if c.popup != nil {
		t.Fatalf("no tables known yet: %+v", c.popup)
	}
	typeIn(t, c, " wher")
	if c.popup == nil || c.popup.Chosen().Label != "where" {
		t.Fatalf("keywords still work: %+v", c.popup)
	}
}

func TestConsoleEditorIsHighlighted(t *testing.T) {
	c := insertConsole(t)
	c.editor.SetValue("SELECT 1")
	if v := c.View(true); !strings.Contains(v, theme.Syntax(highlight.Keyword).Render("SELECT")) {
		t.Fatalf("view: %q", v)
	}
}
