package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

var usersRef = db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"}

func loadedDataTab(t *testing.T, b *fakeBackend) *dataTab {
	t.Helper()
	tab := newDataTab(1, b, newFakeStore(), "local", usersRef, config.Settings{PageSize: 2, MaxColumnWidth: 40})
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
	tab := newStructureTab(2, b, newFakeStore(), "local", usersRef)
	tab.SetSize(80, 20)
	tab.Update(run(tab.Init()))
	if v := ansi.Strip(tab.View(true)); !strings.Contains(v, "[Columns]") || !strings.Contains(v, "bigint") {
		t.Fatalf("columns:\n%s", v)
	}
	tab.Handle(keymap.NextSection)
	if v := ansi.Strip(tab.View(true)); !strings.Contains(v, "PRIMARY") {
		t.Fatalf("indexes:\n%s", v)
	}

	missing := newStructureTab(3, b, newFakeStore(), "local", db.TableRef{Namespace: usersRef.Namespace, Name: "missing"})
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

func TestDataTabSortCycles(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	tab.Handle(keymap.Right) // name
	for _, want := range []string{
		"SELECT * FROM `app`.`users` ORDER BY `name`, `id` LIMIT 3 OFFSET 0",
		"SELECT * FROM `app`.`users` ORDER BY `name` DESC, `id` LIMIT 3 OFFSET 0",
		"SELECT * FROM `app`.`users` ORDER BY `id` LIMIT 3 OFFSET 0",
	} {
		tab.Update(run(tab.Handle(keymap.SortColumn)))
		if got := b.conn.lastQuery(); got != want {
			t.Fatalf("query = %q, want %q", got, want)
		}
		if col, _ := tab.grid.Column(); col.Name != "name" {
			t.Fatalf("the cursor stays on the sorted column: %s", col.Name)
		}
		if strings.Contains(want, "`name`, ") {
			if v := ansi.Strip(tab.View(true)); !strings.Contains(v, "name ▲") || !strings.Contains(v, "sorted by name asc") {
				t.Fatalf("view:\n%s", v)
			}
		}
	}
}

func TestDataTabFilterByValue(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	tab.Handle(keymap.Right) // name of user1
	tab.Update(run(tab.Handle(keymap.FilterEqual)))
	if got, want := b.conn.lastQuery(), "SELECT * FROM `app`.`users` WHERE (`name` = 'user1'\n) ORDER BY `id` LIMIT 3 OFFSET 0"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	tab.Handle(keymap.Left)
	tab.Update(run(tab.Handle(keymap.FilterEqual)))
	if got := b.conn.lastQuery(); !strings.Contains(got, "WHERE ((`name` = 'user1') AND `id` = '1'\n)") {
		t.Fatalf("filters add up: %q", got)
	}
	if v := ansi.Strip(tab.View(true)); !strings.Contains(v, "where (`name` = 'user1') AND `id` = '1'") {
		t.Fatalf("status shows the filter:\n%s", v)
	}
}

func TestDataTabFilterPrompt(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	msg, ok := run(tab.Handle(keymap.FilterPrompt)).(openOverlayMsg)
	if _, isPrompt := msg.overlay.(*overlay.Prompt); !ok || !isPrompt {
		t.Fatalf("F opens a prompt: %#v", msg)
	}
	tab.Update(run(tab.Update(filterMsg{tabID: 1, text: " id > 1; "})))
	if got := b.conn.lastQuery(); !strings.Contains(got, "WHERE (id > 1\n) ORDER BY `id`") {
		t.Fatalf("query = %q", got)
	}
	tab.Update(run(tab.Update(filterMsg{tabID: 1, text: ""})))
	if got := b.conn.lastQuery(); strings.Contains(got, "WHERE") {
		t.Fatalf("an empty filter shows every row: %q", got)
	}
}

func TestDataTabFilterIsConfined(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	for _, bad := range []string{"1=1; DELETE FROM users", "1) ; DROP TABLE users; --"} {
		if cmd := tab.Update(filterMsg{tabID: 1, text: bad}); cmd != nil {
			t.Fatalf("%q must be rejected", bad)
		}
		if v := ansi.Strip(tab.View(true)); !strings.Contains(v, "the filter must be a condition") {
			t.Fatalf("view:\n%s", v)
		}
	}
	tab.Update(run(tab.Update(filterMsg{tabID: 1, text: "id = 1) -- "})))
	if got := b.conn.lastQuery(); !strings.HasSuffix(got, "\n) ORDER BY `id` LIMIT 3 OFFSET 0") {
		t.Fatalf("a comment cannot swallow ORDER BY and LIMIT: %q", got)
	}
}

func TestDataTabBadFilterKeepsThePage(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	q := "SELECT * FROM `app`.`users` WHERE (nope\n) ORDER BY `id` LIMIT 3 OFFSET 0"
	b.conn.setFail(q, errors.New("unknown column nope"))
	tab.Update(run(tab.Update(filterMsg{tabID: 1, text: "nope"})))
	v := ansi.Strip(tab.View(true))
	if !strings.Contains(v, "unknown column nope") || !strings.Contains(v, "user1") || tab.query.where != "" {
		t.Fatalf("a failing filter leaves the shown rows and the old filter: %q\n%s", tab.query.where, v)
	}
}
