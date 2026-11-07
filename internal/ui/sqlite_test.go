package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
)

func sqliteBackend() *fakeBackend {
	b := newFakeBackend()
	b.conns = []config.Connection{{Name: "local", Driver: "sqlite", Database: "/tmp/a.db", Env: config.EnvDev}}
	return b
}

func TestServerStatusHiddenForSQLite(t *testing.T) {
	m := newTestModelWith(t, sqliteBackend())
	m.setFocus(focusTree)
	if slices.ContainsFunc(m.nodeMenu(m.tree.Selected()), func(it menuItem) bool { return strings.Contains(it.desc, "status") }) {
		t.Fatal("the connection menu offers server status")
	}
	press(t, m, "space", "i")
	if _, onServer := m.activeTab().(*sqlServerTab); onServer {
		t.Fatal("server tab opened")
	}
	if !strings.Contains(m.status, "SQLite has no server status") {
		t.Fatalf("status %q", m.status)
	}
}
