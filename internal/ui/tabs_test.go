package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
)

var usersRef = db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"}

func loadedDataTab(t *testing.T, b *fakeBackend) *dataTab {
	t.Helper()
	tab := newDataTab(1, b, "local", usersRef, config.Settings{PageSize: 2, MaxColumnWidth: 40})
	tab.SetSize(60, 10)
	tab.Update(run(tab.Init()))
	return tab
}

func TestDataTabFirstPage(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	if got, want := b.conn.lastQuery(), "SELECT * FROM `app`.`users` ORDER BY `id` LIMIT 3 OFFSET 0"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	v := ansi.Strip(tab.View(true))
	if !strings.Contains(v, "user2") || strings.Contains(v, "user3") {
		t.Fatalf("the extra row only signals another page:\n%s", v)
	}
	if !strings.Contains(v, "rows 1–2 · page 1 · ] next") {
		t.Fatalf("status:\n%s", v)
	}
}

func TestDataTabPaging(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	tab.Update(run(tab.Handle(keymap.NextPage)))
	if !strings.HasSuffix(b.conn.lastQuery(), "LIMIT 3 OFFSET 2") {
		t.Fatalf("query = %q", b.conn.lastQuery())
	}
	if v := ansi.Strip(tab.View(true)); !strings.Contains(v, "rows 3–4 · page 2") {
		t.Fatalf("status:\n%s", v)
	}
	tab.Update(run(tab.Handle(keymap.PrevPage)))
	if !strings.HasSuffix(b.conn.lastQuery(), "OFFSET 0") {
		t.Fatalf("query = %q", b.conn.lastQuery())
	}

	b.conn.rows = 1
	tab.Update(run(tab.Handle(keymap.Refresh)))
	if tab.Handle(keymap.NextPage) != nil {
		t.Fatal("no next page when the server returned no extra row")
	}
}

func TestDataTabCellDetail(t *testing.T) {
	tab := loadedDataTab(t, newFakeBackend())
	tab.Handle(keymap.Right)
	msg, ok := run(tab.Handle(keymap.CellDetail)).(openOverlayMsg)
	if !ok {
		t.Fatal("K must open an overlay")
	}
	if v := ansi.Strip(msg.overlay.View(60, 20)); !strings.Contains(v, "user1") || !strings.Contains(v, "name") {
		t.Fatalf("detail:\n%s", v)
	}
}

func TestDataTabKeepsDataOnError(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	b.setConnErr(errors.New("connection reset"))
	tab.Update(run(tab.Handle(keymap.NextPage)))
	v := ansi.Strip(tab.View(true))
	if !strings.Contains(v, "✗ connection reset") || !strings.Contains(v, "user1") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestStructureTab(t *testing.T) {
	b := newFakeBackend()
	tab := newStructureTab(2, b, "local", usersRef)
	tab.SetSize(80, 20)
	tab.Update(run(tab.Init()))
	if v := ansi.Strip(tab.View(true)); !strings.Contains(v, "[Columns]") || !strings.Contains(v, "bigint") {
		t.Fatalf("columns:\n%s", v)
	}
	tab.Handle(keymap.NextSection)
	if v := ansi.Strip(tab.View(true)); !strings.Contains(v, "PRIMARY") {
		t.Fatalf("indexes:\n%s", v)
	}

	missing := newStructureTab(3, b, "local", db.TableRef{Namespace: usersRef.Namespace, Name: "missing"})
	missing.SetSize(80, 20)
	missing.Update(run(missing.Init()))
	if v := ansi.Strip(missing.View(true)); !strings.Contains(v, "not found") {
		t.Fatalf("missing:\n%s", v)
	}
}

func TestTabKeys(t *testing.T) {
	if dataTabKey("local", usersRef) == structureTabKey("local", usersRef) {
		t.Fatal("data and structure tabs of one table are distinct")
	}
	other := usersRef
	other.Database = "app2"
	if dataTabKey("local", usersRef) == dataTabKey("local", other) {
		t.Fatal("tables in different databases are distinct")
	}
}
