package ui

import (
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
	"strings"
	"testing"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"

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
	press(t, m, "l", "j", "space", "c", "esc", "ctrl+h")
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

func TestRecentReopensAFile(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "select 1;")
	ex(t, m, "e "+p)
	ex(t, m, "q")
	press(t, m, "space", "f")
	if s := screen(m); !strings.Contains(s, "file") || !strings.Contains(s, "a.sql") || !strings.Contains(s, "local") {
		t.Fatalf("screen:\n%s", s)
	}
	press(t, m, "enter")
	if c := fileConsole(t, m); c.editor.Value() != "select 1;" || c.conn != "local" {
		t.Fatalf("conn %s text %q", c.conn, c.editor.Value())
	}
}

func TestRecentFileLabel(t *testing.T) {
	r := history.Recent{Kind: history.RecentFile, Conn: "local", Database: "app", Name: "/tmp/x/a.sql"}
	if got := recentLabel(r); !strings.HasPrefix(got, "file     /tmp/x/a.sql  local/app") {
		t.Fatalf("label %q", got)
	}
}

func TestSwitchingToAFileConsoleRecordsTheFile(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "")
	ex(t, m, "e "+p)
	openLocalConsole(t, m)
	press(t, m, "H") // back to the file console
	st := m.store.(*fakeStore)
	st.mu.Lock()
	last := st.recents[len(st.recents)-1]
	st.mu.Unlock()
	if last.Kind != history.RecentFile || last.Name != p {
		t.Fatalf("last recent %+v", last)
	}
}

func TestRecentKafkaTopic(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 1000)
	tt := openMessages(t, m, "orders")
	st := m.store.(*fakeStore)
	if last := st.recents[len(st.recents)-1]; last.Kind != history.RecentKafkaTopic || last.Name != "orders" || last.Conn != "events" {
		t.Fatalf("recent = %+v", last)
	}
	seek(t, m, "p0:10")
	m.Update(uitest.Key("T")) // not drained: live tail polls for ever
	if tt.msgs.tail == nil {
		t.Fatal("tail on")
	}
	pressAll(t, m, ":")
	typeText(t, m, "q")
	pressAll(t, m, "enter")
	pressAll(t, m, "space", "f")
	pressAll(t, m, "enter")
	re, ok := m.activeTab().(*topicTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	if got := k.lastReader().rangeList(); len(got) != 1 || got[0][0] != (kafka.Range{From: 800, To: 1000}) || re.msgs.tail != nil || re.msgs.from != "" {
		t.Fatalf("reopened at %v, tail %v", got, re.msgs.tail)
	}
	if !strings.HasPrefix(recentLabel(history.Recent{Kind: history.RecentKafkaGroup, Conn: "events", Name: "g"}), "group") {
		t.Fatal("group label")
	}
}
