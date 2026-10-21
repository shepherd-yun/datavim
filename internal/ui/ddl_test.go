package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
)

const usersDDL = "CREATE TABLE `users` (\n  `id` bigint\n)"

func TestCopyDDLFromTheTree(t *testing.T) {
	m, _ := newTestModel(t)
	clip := captureClipboard(t)
	press(t, m, "l", "j", "l", "j") // cursor on users
	press(t, m, "space", "y", "d")
	if *clip != usersDDL {
		t.Fatalf("clipboard = %q", *clip)
	}
	if !strings.Contains(screen(m), "copied the DDL of users") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	if len(m.tabs) != 0 || m.overlay != nil {
		t.Fatal("copying opens nothing")
	}
}

func TestYDCopiesDDLFromTheTree(t *testing.T) {
	m, _ := newTestModel(t)
	clip := captureClipboard(t)
	press(t, m, "l", "j", "l", "j", "y", "d") // cursor on users
	if *clip != usersDDL {
		t.Fatalf("clipboard = %q", *clip)
	}
}

func TestCopyDDLFromTabs(t *testing.T) {
	m, _ := newTestModel(t)
	clip := captureClipboard(t)
	openUsers(t, m)
	press(t, m, "space", "y", "d")
	if *clip != usersDDL {
		t.Fatalf("from the data tab: %q", *clip)
	}
	*clip = ""
	press(t, m, "space", "S", "]", "space", "y", "d") // any section of the structure tab
	if _, ok := m.activeTab().(*structureTab); !ok || *clip != usersDDL {
		t.Fatalf("from the structure tab: %q", *clip)
	}
}

func TestCopyDDLNeedsATable(t *testing.T) {
	m, _ := newTestModel(t)
	clip := captureClipboard(t)
	press(t, m, "space", "y", "d")
	if *clip != "" || !strings.Contains(screen(m), "select a table first") {
		t.Fatalf("clip=%q screen:\n%s", *clip, screen(m))
	}
}

func TestCopyDDLOfAMissingTable(t *testing.T) {
	m, _ := newTestModel(t)
	clip := captureClipboard(t)
	drain(t, m, m.openStructure("local", db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "missing"}))
	press(t, m, "space", "y", "d")
	if *clip != "" || m.status != "table missing not found" || !m.statusErr {
		t.Fatalf("clip=%q status=%q", *clip, m.status)
	}
}

func TestOpenFullStructureDirectly(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "l", "j", "l", "j") // cursor on users
	press(t, m, "space", "S")
	if tab, ok := m.activeTab().(*structureTab); !ok || tab.ref.Name != "users" || m.overlay != nil {
		t.Fatalf("active tab = %#v, overlay = %v", m.activeTab(), m.overlay)
	}
	press(t, m, "space", "S") // from the structure tab: same table, no new tab
	if len(m.tabs) != 1 {
		t.Fatalf("tabs = %d", len(m.tabs))
	}
}

func TestOpenFullStructureNeedsATable(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "space", "S")
	if len(m.tabs) != 0 || !strings.Contains(screen(m), "select a table first") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestCopyRoutineDefinition(t *testing.T) {
	m, _ := newTestModel(t)
	clip := captureClipboard(t)
	press(t, m, "l")
	search(t, m, "add_one")
	press(t, m, "enter", "y")
	if *clip != "CREATE FUNCTION add_one() RETURNS int RETURN 1" {
		t.Fatalf("clipboard = %q", *clip)
	}
	if !strings.Contains(screen(m), "copied add_one") || m.overlay == nil {
		t.Fatalf("the popup stays open:\n%s", screen(m))
	}
}
