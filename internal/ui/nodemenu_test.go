package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
)

func TestActionMenuOnTable(t *testing.T) {
	m, _ := newTestModel(t)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "a")
	s := screen(m)
	for _, want := range []string{"users", "Open data", "Copy DDL", "Rename table", "Truncate table", "Drop table", "dd"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
	// picking an entry runs it: Truncate asks for the table's name
	at := slices.IndexFunc(m.nodeMenu(m.tree.Selected()), func(it menuItem) bool { return it.action == keymap.TruncateTable })
	for range at {
		press(t, m, "j")
	}
	press(t, m, "enter")
	if !strings.Contains(screen(m), "Truncate table users") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestActionMenuOnView(t *testing.T) {
	m, _ := newTestModel(t)
	selectNode(t, m, "local", "app", "active")
	press(t, m, "a")
	s := screen(m)
	if !strings.Contains(s, "Drop view") || !strings.Contains(s, "Rename view") || strings.Contains(s, "Truncate") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestActionMenuOnConnection(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "a")
	s := screen(m)
	if !strings.Contains(s, "Edit connection") || !strings.Contains(s, "Delete connection") || strings.Contains(s, "Drop") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestActionMenuShowsRemappedKeys(t *testing.T) {
	m, _ := newTestModel(t)
	k, err := keymap.Custom("", map[keymap.Context]map[string]string{
		keymap.Tree: {"dd": "", "D": "node.delete", "T": "table.truncate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.SetKeymap(k)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "a")
	for _, it := range m.nodeMenu(m.tree.Selected()) {
		switch it.action {
		case keymap.DeleteNode:
			if it.keys != "D" {
				t.Fatalf("drop shows %q", it.keys)
			}
		case keymap.TruncateTable:
			if it.keys != "T" {
				t.Fatalf("truncate shows %q", it.keys)
			}
		}
	}
}
