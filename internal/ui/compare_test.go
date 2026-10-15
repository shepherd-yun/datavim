package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

// withBakTable makes the fake app database list users, users_bak and the view active.
func withBakTable(b *fakeBackend) {
	b.conn.setTables([]db.Table{{Name: "users"}, {Name: "users_bak"}, {Name: "active", Kind: db.TableKindView}})
	b.conn.setSchemas(usersSchemas()...)
}

func TestMarkThenCompareOpensDiff(t *testing.T) {
	m, b := newTestModel(t)
	withBakTable(b)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "m")
	wantScreen(t, m, "◆ users")
	if !strings.Contains(statusLine(m), "compare source: local · app.users") {
		t.Fatalf("status: %s", statusLine(m))
	}
	selectNode(t, m, "local", "app", "users_bak")
	press(t, m, "C")
	d, ok := m.activeTab().(*diffTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	if d.src.ref.Name != "users" || d.tgt.ref.Name != "users_bak" {
		t.Fatalf("src %+v, tgt %+v", d.src, d.tgt)
	}
	if strings.Contains(screen(m), "◆") || m.compareMark != nil {
		t.Fatal("the mark stays after comparing")
	}
	wantScreen(t, m, "column email")
}

func TestMarkTwiceClears(t *testing.T) {
	m, b := newTestModel(t)
	withBakTable(b)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "m", "m")
	if strings.Contains(screen(m), "◆") || m.compareMark != nil {
		t.Fatalf("mark kept:\n%s", screen(m))
	}
	if !strings.Contains(statusLine(m), "compare mark cleared") {
		t.Fatalf("status: %s", statusLine(m))
	}
}

func TestCompareRejects(t *testing.T) {
	m, b := newTestModel(t)
	withBakTable(b)
	selectNode(t, m, "local", "app", "active")
	press(t, m, "m")
	if !strings.Contains(statusLine(m), "views are not compared") {
		t.Fatalf("view: %s", statusLine(m))
	}
	selectNode(t, m, "local", "app", "users")
	press(t, m, "m", "C")
	if !strings.Contains(statusLine(m), "the source and the target are the same") {
		t.Fatalf("same: %s", statusLine(m))
	}
	selectNode(t, m, "local", "app")
	press(t, m, "C")
	if !strings.Contains(statusLine(m), "compare a table with a table") {
		t.Fatalf("kinds: %s", statusLine(m))
	}
	if len(m.tabs) != 0 {
		t.Fatalf("tabs = %d", len(m.tabs))
	}
	selectNode(t, m, "prod", "app")
	press(t, m, "m")
	if !strings.Contains(statusLine(m), "select a schema") {
		t.Fatalf("PostgreSQL database: %s", statusLine(m))
	}
}

func TestCompareProblemEngines(t *testing.T) {
	my := compareSide{conn: "local", driver: "mysql", ref: db.TableRef{Namespace: appNS, Name: "users"}}
	pg := compareSide{conn: "prod", driver: "postgres", ref: db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "users"}}
	if p := compareProblem(my, pg); p != "cannot compare mysql with postgres" {
		t.Fatalf("problem = %q", p)
	}
	other := my
	other.conn = "staging"
	if p := compareProblem(my, other); p != "" {
		t.Fatalf("same table on two connections: %q", p)
	}
}

func TestComparePicksSourceWithoutMark(t *testing.T) {
	m, b := newTestModel(t)
	withBakTable(b)
	drain(t, m, m.ensureMeta("local", "app")) // the catalog lists active (view), order and users
	selectNode(t, m, "local", "app", "users_bak")
	press(t, m, "C")
	if _, ok := m.overlay.(*overlay.Picker); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	s := screen(m)
	if !strings.Contains(s, "local · app.order") || !strings.Contains(s, "local · app.users") || strings.Contains(s, "app.active") {
		t.Fatalf("picker:\n%s", s)
	}
	typeText(t, m, "app.users")
	press(t, m, "enter")
	d, ok := m.activeTab().(*diffTab)
	if !ok || d.src.ref.Name != "users" || d.tgt.ref.Name != "users_bak" {
		t.Fatalf("active tab = %+v", m.activeTab())
	}
}

func TestCompareWithoutCandidates(t *testing.T) {
	m, b := newTestModel(t)
	withBakTable(b)
	selectNode(t, m, "local", "app", "users_bak")
	m.catalogs.Forget("local") // revealing the node loaded it
	press(t, m, "C")
	if m.overlay != nil || !strings.Contains(statusLine(m), "nothing to compare with") {
		t.Fatalf("overlay %T, status %s", m.overlay, statusLine(m))
	}
}
