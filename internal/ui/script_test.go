package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func scriptConsole(t *testing.T, m *Model) *consoleTab {
	t.Helper()
	c, ok := m.activeTab().(*consoleTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	return c
}

func TestWriteScriptOpensConsoleOnTarget(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	press(t, m, "space", "w")
	c := scriptConsole(t, m)
	if c.conn != "local" || c.ns != appNS || c.Title() != "sync:app" {
		t.Fatalf("console %s on %s %+v", c.Title(), c.conn, c.ns)
	}
	text := c.editor.Value()
	for _, want := range []string{
		"-- source: local · app.users\n",
		"-- target: local · app.users_bak\n",
		"ALTER TABLE `app`.`users_bak` AddColumn email;",
		"ALTER TABLE `app`.`users_bak` AlterColumn name;",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "DropColumn legacy") {
		t.Fatalf("the unpicked drop is in the script:\n%s", text)
	}
	if c.Key() == consoleTabKey("local", appNS) {
		t.Fatal("the script console took the connection's usual console key")
	}
}

func TestWriteScriptOpensANewConsoleEachTime(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	press(t, m, "space", "w")
	m.gotoTab(0)
	press(t, m, "space", "w")
	if len(m.tabs) != 3 {
		t.Fatalf("tabs = %d, want the diff and two consoles", len(m.tabs))
	}
	if m.tabs[1].Key() == m.tabs[2].Key() {
		t.Fatal("both script consoles have one key")
	}
}

func TestWriteScriptNothingPicked(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	press(t, m, "x", "x") // all, then none
	press(t, m, "space", "w")
	if len(m.tabs) != 1 {
		t.Fatalf("tabs = %d", len(m.tabs))
	}
	wantScreen(t, m, "nothing picked")
}

func TestManualChangesStayInTheScriptAsComments(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setSchemas(
		schemaOf("users", idColumn(), db.Column{Name: "email", Type: "text", Nullable: true},
			db.Column{Name: "total", Type: "int", Nullable: true, Generated: "id * 2"}),
		schemaOf("users_bak", idColumn(), db.Column{Name: "total", Type: "int", Nullable: true}),
	)
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	wantScreen(t, m, "~ column total  not generated → AS (id * 2)  ⚠ manual")
	press(t, m, "j", "j", "x") // on total
	wantScreen(t, m, "this change cannot be scripted")
	press(t, m, "space", "w")
	text := scriptConsole(t, m).editor.Value()
	for _, want := range []string{
		"AddColumn email;",
		"-- Not scripted, change by hand:\n--   users_bak: ~ column total  not generated → AS (id * 2)\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
}

func TestScriptConsoleKeepsNoDraft(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	press(t, m, "space", "w")
	c := scriptConsole(t, m)
	drain(t, m, m.closeTab(m.active))
	if d := m.store.(*fakeStore).draft(c.Key()); d != "" {
		t.Fatalf("draft saved: %q", d)
	}
}
