package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/history"
)

func TestRecentListsWhatWasOpened(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	press(t, m, "space", "s", "ctrl+o")
	press(t, m, "space", "f")
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
	press(t, m, "space", "f")
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
	press(t, m, "space", "f", "enter")
	if !strings.Contains(m.status, "no longer exists") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestRecentEmpty(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "space", "f")
	if m.overlay != nil || !strings.Contains(m.status, "nothing opened yet") {
		t.Fatalf("overlay %T, status %q", m.overlay, m.status)
	}
}

func TestRecentAsksForThePassword(t *testing.T) {
	m, b := newTestModel(t)
	if err := m.store.AddRecent(history.Recent{Kind: history.RecentData, Conn: "local", Database: "app", Name: "users", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	b.needPassword = true
	press(t, m, "space", "f", "enter")
	if !strings.Contains(screen(m), "Password for local") {
		t.Fatalf("prompt not shown:\n%s", screen(m))
	}
	press(t, m, "s", "e", "c", "enter")
	tab, ok := m.activeTab().(*dataTab)
	if !ok || tab.err != "" {
		t.Fatalf("tab = %#v", m.activeTab())
	}
	if _, _, ok := tab.grid.Cell(); !ok {
		t.Fatal("the tab loads once the password is entered")
	}
}

func TestSwitchingTabsUpdatesRecent(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	press(t, m, "space", "s", "ctrl+o") // the structure tab is now the newest
	press(t, m, "1")                    // back to the data tab
	got, _ := m.store.Recent(1)
	if len(got) != 1 || got[0].Kind != history.RecentData {
		t.Fatalf("newest = %+v", got)
	}
}
