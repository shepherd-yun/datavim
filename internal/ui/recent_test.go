package ui

import (
	"strings"
	"testing"
)

func TestRecentListsWhatWasOpened(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	press(t, m, "space", "s")
	press(t, m, "space", "e")
	s := screen(m)
	data, structure := strings.Index(s, "data     local/app/users"), strings.Index(s, "struct   local/app/users")
	if data < 0 || structure < 0 || structure > data {
		t.Fatalf("newest first:\n%s", s)
	}
	press(t, m, "down", "enter")
	if tab, ok := m.activeTab().(*dataTab); !ok || tab.ref.Name != "users" {
		t.Fatalf("active tab = %#v", m.activeTab())
	}
}

func TestRecentConsoleAndMissingConnection(t *testing.T) {
	m, b := newTestModel(t)
	press(t, m, "l", "j", "space", "c", "ctrl+h")
	press(t, m, "space", "e")
	if s := screen(m); !strings.Contains(s, "console  local/app") {
		t.Fatalf("screen:\n%s", s)
	}
	press(t, m, "enter")
	if _, ok := m.activeTab().(*consoleTab); !ok || m.focus != focusTabs {
		t.Fatalf("console reopened: %#v", m.activeTab())
	}
	if err := b.RemoveConnection("local"); err != nil {
		t.Fatal(err)
	}
	press(t, m, "space", "e", "enter")
	if !strings.Contains(m.status, "no longer exists") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestRecentEmpty(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "space", "e")
	if m.overlay != nil || !strings.Contains(m.status, "nothing opened yet") {
		t.Fatalf("overlay %T, status %q", m.overlay, m.status)
	}
}
