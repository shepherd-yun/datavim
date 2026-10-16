package ui

import (
	"errors"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/meta"
)

var localKey = meta.Key{Conn: "local"}

func TestCatalogLoadsWhenAConnectionIsFirstUsed(t *testing.T) {
	m, b := newTestModel(t)
	if b.conn.catalogCount() != 0 {
		t.Fatal("nothing loads before a connection is used")
	}
	press(t, m, "l")
	snap, st := m.catalogs.Get(localKey)
	if st != meta.Ready || len(snap.Tables(db.Namespace{Database: "app"})) != 3 {
		t.Fatalf("state %v, snapshot %+v", st, snap)
	}
	press(t, m, "j", "l", "j", "l") // expand app and open users
	if n := b.conn.catalogCount(); n != 1 {
		t.Fatalf("a MySQL catalog covers the server; loaded %d times", n)
	}
}

func TestPostgresCatalogsArePerDatabase(t *testing.T) {
	m, b := newTestModel(t)
	b.mu.Lock()
	b.conns[1].Database = "app" // prod is postgres
	b.mu.Unlock()
	press(t, m, "j", "l")
	if _, st := m.catalogs.Get(meta.Key{Conn: "prod", Database: "app"}); st != meta.Ready {
		t.Fatalf("default database: %v", st)
	}
	drain(t, m, m.ensureMeta("prod", "other"))
	if _, st := m.catalogs.Get(meta.Key{Conn: "prod", Database: "other"}); st != meta.Ready {
		t.Fatalf("other database: %v", st)
	}
	if n := b.conn.catalogCount(); n != 2 {
		t.Fatalf("loads = %d", n)
	}
}

func TestRefreshReloadsCatalog(t *testing.T) {
	m, b := newTestModel(t)
	press(t, m, "l")
	press(t, m, "space", "r")
	if n := b.conn.catalogCount(); n != 2 {
		t.Fatalf("loads = %d", n)
	}
}

func TestFailedCatalogWaitsForRefresh(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.mu.Lock()
	b.conn.catalogErr = errors.New("permission denied")
	b.conn.mu.Unlock()
	press(t, m, "l")
	if _, st := m.catalogs.Get(localKey); st != meta.Failed {
		t.Fatalf("state = %v", st)
	}
	press(t, m, "j", "l")
	if n := b.conn.catalogCount(); n != 1 {
		t.Fatalf("a failed load is not retried by itself; loads = %d", n)
	}
	for _, r := range m.tree.Roots() {
		if r.Broken {
			t.Fatal("a failed catalog does not mark the connection broken")
		}
	}
	b.conn.mu.Lock()
	b.conn.catalogErr = nil
	b.conn.mu.Unlock()
	press(t, m, "space", "r")
	if _, st := m.catalogs.Get(localKey); st != meta.Ready {
		t.Fatalf("after <space>r: %v", st)
	}
}

func TestConsoleDDLReloadsCatalog(t *testing.T) {
	m, b := newTestModel(t)
	press(t, m, "l", "j", "space", "c", "esc")
	c := m.activeTab().(*consoleTab)
	c.editor.SetValue("create table t2 (id int)")
	press(t, m, "enter")
	if n := b.conn.catalogCount(); n != 2 {
		t.Fatalf("DDL reloads the catalog; loads = %d", n)
	}
	c.editor.SetValue("select 1")
	press(t, m, "enter")
	if n := b.conn.catalogCount(); n != 2 {
		t.Fatalf("queries do not; loads = %d", n)
	}
}

func TestEditedOrRemovedConnectionForgetsItsCatalog(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "l")
	m.Update(connUpdatedMsg{old: "local", conn: config.Connection{Name: "local", Driver: "mysql"}})
	if _, st := m.catalogs.Get(localKey); st != meta.Missing {
		t.Fatalf("edited: %v", st)
	}
	drain(t, m, m.ensureMeta("local", ""))
	if _, st := m.catalogs.Get(localKey); st != meta.Ready {
		t.Fatalf("reloaded: %v", st)
	}
	m.Update(connRemovedMsg{name: "local"})
	if _, st := m.catalogs.Get(localKey); st != meta.Missing {
		t.Fatalf("removed: %v", st)
	}
}

func TestCatalogLoadsAfterThePasswordIsEntered(t *testing.T) {
	m, b := newTestModel(t)
	b.needPassword = true
	press(t, m, "space", "space", "esc") // the catalog load fails: no password yet
	press(t, m, "l", "s", "e", "c", "enter")
	if _, st := m.catalogs.Get(localKey); st != meta.Ready {
		t.Fatalf("state after the password = %v", st)
	}
}
