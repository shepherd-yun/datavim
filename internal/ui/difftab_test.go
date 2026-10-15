package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

var appNS = db.Namespace{Database: "app"}

func idColumn() db.Column { return db.Column{Name: "id", Type: "bigint", PrimaryKey: true} }

func schemaOf(name string, cols ...db.Column) db.TableSchema {
	return db.TableSchema{
		Ref:     db.TableRef{Namespace: appNS, Name: name},
		Columns: cols,
		Indexes: []db.Index{{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true}},
	}
}

func tableSide(conn, table string) compareSide {
	return compareSide{conn: conn, driver: "mysql", ref: db.TableRef{Namespace: appNS, Name: table}}
}

// usersSchemas are app.users (source) and app.users_bak (target): users_bak
// lacks email, has a narrower name and an extra column legacy.
func usersSchemas() []db.TableSchema {
	return []db.TableSchema{
		schemaOf("users", idColumn(), db.Column{Name: "name", Type: "varchar(100)"},
			db.Column{Name: "email", Type: "varchar(255)", Nullable: true}),
		schemaOf("users_bak", idColumn(), db.Column{Name: "name", Type: "varchar(50)"},
			db.Column{Name: "legacy", Type: "int", Nullable: true}),
	}
}

// openUsersDiff compares local app.users (source) with app.users_bak (target).
func openUsersDiff(t *testing.T, m *Model, b *fakeBackend) *diffTab {
	t.Helper()
	wide(m)
	b.conn.setSchemas(usersSchemas()...)
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	d, ok := m.activeTab().(*diffTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	return d
}

// wide makes the window wide enough for a diff tab's header line.
func wide(m *Model) { m.Update(tea.WindowSizeMsg{Width: 160, Height: 30}) }

func wantScreen(t *testing.T, m *Model, want ...string) {
	t.Helper()
	s := screen(m)
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Fatalf("missing %q:\n%s", w, s)
		}
	}
}

func TestDiffTabListsChangesWithDefaults(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	wantScreen(t, m,
		"local · app.users ──▶ local · app.users_bak",
		"1 table differs", "1 destructive skipped",
		"▾ [-] ~ users_bak",
		"[ ] - column legacy  int NULL  ⚠ destructive",
		"[✓] + column email  varchar(255) NULL",
		"[✓] ~ column name  varchar(50) → varchar(100)",
	)
	if !strings.Contains(screen(m), "[1]diff:users_bak") {
		t.Errorf("tab title:\n%s", screen(m))
	}
}

func TestDiffTabToggle(t *testing.T) {
	m, b := newTestModel(t)
	d := openUsersDiff(t, m, b)
	press(t, m, "x") // on the table: everything
	wantScreen(t, m, "[✓] - column legacy", "▾ [✓] ~ users_bak")
	press(t, m, "x") // again: nothing
	wantScreen(t, m, "[ ] - column legacy", "[ ] + column email", "▾ [ ] ~ users_bak")
	press(t, m, "j", "j", "x") // email alone
	wantScreen(t, m, "[✓] + column email", "▾ [-] ~ users_bak")
	if !d.picked[pick{0, 1}] || d.picked[pick{0, 0}] {
		t.Errorf("picked = %v", d.picked)
	}
}

func TestDiffTabFoldAndJump(t *testing.T) {
	m, b := newTestModel(t)
	d := openUsersDiff(t, m, b)
	press(t, m, "h")
	if strings.Contains(screen(m), "column email") {
		t.Fatalf("collapsed table still lists its changes:\n%s", screen(m))
	}
	wantScreen(t, m, "▸ [-] ~ users_bak")
	press(t, m, "l")
	wantScreen(t, m, "column email")

	press(t, m, "]", "c")
	if d.cursor != 1 {
		t.Fatalf("]c: cursor = %d, want 1", d.cursor)
	}
	press(t, m, "]", "c", "]", "c", "]", "c")
	if d.cursor != 3 || !strings.Contains(screen(m), "no more changes") {
		t.Fatalf("cursor = %d:\n%s", d.cursor, screen(m))
	}
	press(t, m, "[", "c")
	if d.cursor != 2 {
		t.Fatalf("[c: cursor = %d, want 2", d.cursor)
	}
	press(t, m, "h") // on a change: folds its table and goes up to it
	if d.cursor != 0 || !d.collapsed[0] {
		t.Fatalf("cursor = %d, collapsed = %v", d.cursor, d.collapsed)
	}
}

func TestDiffTabSwap(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	press(t, m, "~")
	wantScreen(t, m,
		"local · app.users_bak ──▶ local · app.users",
		"[ ] - column email", "[✓] + column legacy",
		"~ column name  varchar(100) → varchar(50)  ⚠ lossy",
	)
}

func TestDiffTabIdentical(t *testing.T) {
	m, b := newTestModel(t)
	same := schemaOf("users", idColumn())
	twin := schemaOf("users_bak", idColumn())
	b.conn.setSchemas(same, twin)
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	wantScreen(t, m, "the tables have the same structure")
}

func TestDiffTabShowsWhichSideFailed(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setSchemas(usersSchemas()[1]) // users is missing
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	wantScreen(t, m, "✗ source local · app.users: users: not found")
	if strings.Contains(screen(m), "✗ target") {
		t.Fatalf("the target loaded fine:\n%s", screen(m))
	}
	b.conn.setSchemas(usersSchemas()...)
	press(t, m, "space", "r")
	wantScreen(t, m, "column email")
}

func TestDiffTabLoadCanBeCancelled(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setSchemas(usersSchemas()...)
	load := m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak"))
	wantScreen(t, m, "loading both sides")
	press(t, m, "ctrl+c")
	drain(t, m, load) // the reads see their context cancelled
	wantScreen(t, m, "✗ source local · app.users: canceled")
}

func TestDiffTabNamespaces(t *testing.T) {
	m, b := newTestModel(t)
	wide(m)
	staging := b.addConn(config.Connection{Name: "staging", Driver: "mysql", Env: config.EnvDev})
	b.conn.setTables([]db.Table{{Name: "users"}, {Name: "audit"}, {Name: "active", Kind: db.TableKindView}})
	b.conn.setSchemas(schemaOf("users", idColumn()), schemaOf("audit", idColumn()))
	staging.setTables([]db.Table{{Name: "users"}, {Name: "tmp"}})
	staging.setSchemas(schemaOf("users", idColumn()), schemaOf("tmp", idColumn()))
	src := compareSide{conn: "local", driver: "mysql", ref: db.TableRef{Namespace: appNS}}
	tgt := compareSide{conn: "staging", driver: "mysql", ref: db.TableRef{Namespace: appNS}}
	drain(t, m, m.openDiff(src, tgt))
	wantScreen(t, m,
		"local · app ──▶ staging · app",
		"2 tables differ · 1 identical · 1 destructive skipped",
		"[✓] + audit  (new table, 1 column)",
		"[ ] - tmp  ⚠ destructive",
	)
}

func TestDiffTabAsksForPassword(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setSchemas(usersSchemas()...)
	b.mu.Lock()
	b.needPassword = true
	b.mu.Unlock()
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	if _, ok := m.overlay.(*overlay.Password); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	b.mu.Lock()
	b.needPassword = false
	b.mu.Unlock()
	typeText(t, m, "secret")
	press(t, m, "enter")
	wantScreen(t, m, "column email")
}
