package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

var userCols = []db.ColumnInfo{{Name: "id", DatabaseType: "BIGINT"}, {Name: "name", DatabaseType: "VARCHAR"}}

func userRow(id, name string) []db.Value { return []db.Value{{Text: id}, {Text: name}} }

// usersData fills app.users (source) and app.users_bak (target): 1 is
// alike, 2 is bob in users and robert in users_bak, 3 is only in
// users_bak and 4 only in users.
func usersData(c *fakeConn) {
	c.setData("users", userCols, userRow("1", "alice"), userRow("2", "bob"), userRow("4", "dave"))
	c.setData("users_bak", userCols, userRow("1", "alice"), userRow("2", "robert"), userRow("3", "carol"))
}

// openUsersData compares the data of app.users with app.users_bak.
func openUsersData(t *testing.T, m *Model, b *fakeBackend) *diffTab {
	t.Helper()
	d := openUsersDiff(t, m, b)
	usersData(b.conn)
	press(t, m, "D")
	if d.data == nil {
		t.Fatalf("no data view:\n%s", screen(m))
	}
	return d
}

func queried(c *fakeConn, s string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.ContainsFunc(c.queries, func(q string) bool { return strings.Contains(q, s) })
}

func TestDataViewListsDifferingRows(t *testing.T) {
	m, b := newTestModel(t)
	openUsersData(t, m, b)
	wantScreen(t, m,
		"local · app.users ──▶ local · app.users_bak",
		"key: id", "3 differ (1 + · 1 - · 1 ~)", "1 delete skipped",
		"the structures differ · comparing the 2 columns both have",
		"[✓] ~ 2   robert → bob",
		"[ ] - 3   carol",
		"[✓] + 4   dave",
	)
	if strings.Contains(screen(m), "alice") {
		t.Fatalf("a row that is alike is listed:\n%s", screen(m))
	}
	for _, q := range []string{"SELECT `id`, `name` FROM `app`.`users`", "SELECT `id`, `name` FROM `app`.`users_bak`"} {
		if !queried(b.conn, q) {
			t.Errorf("no query %q", q)
		}
	}
}

func TestDataViewBackToStructure(t *testing.T) {
	m, b := newTestModel(t)
	d := openUsersData(t, m, b)
	press(t, m, "D")
	if d.data != nil {
		t.Fatal("D did not go back")
	}
	wantScreen(t, m, "column email")
}

func TestDataViewSameRows(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	b.conn.setData("users", userCols, userRow("1", "alice"))
	b.conn.setData("users_bak", userCols, userRow("1", "alice"))
	press(t, m, "D")
	wantScreen(t, m, "the rows are the same · 1 row compared")
}

func TestDataViewNeedsAKey(t *testing.T) {
	m, b := newTestModel(t)
	wide(m)
	keyless := func(name string) db.TableSchema {
		return db.TableSchema{Ref: db.TableRef{Namespace: appNS, Name: name}, Columns: []db.Column{{Name: "id", Type: "bigint"}}}
	}
	b.conn.setSchemas(keyless("users"), keyless("users_bak"))
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	d := m.activeTab().(*diffTab)
	press(t, m, "D")
	if d.data != nil {
		t.Fatal("opened a data view without a key")
	}
	wantScreen(t, m, "users has no primary key or unique NOT NULL index")
}

func TestDataViewNamespacePicksTheTableUnderCursor(t *testing.T) {
	m, b := newTestModel(t)
	wide(m)
	staging := b.addConn(config.Connection{Name: "staging", Driver: "mysql", Env: config.EnvDev})
	b.conn.setTables([]db.Table{{Name: "users"}, {Name: "audit"}})
	b.conn.setSchemas(schemaOf("users", idColumn(), db.Column{Name: "name", Type: "varchar(100)"}), schemaOf("audit", idColumn()))
	staging.setTables([]db.Table{{Name: "users"}})
	staging.setSchemas(schemaOf("users", idColumn(), db.Column{Name: "name", Type: "varchar(50)"}))
	src := compareSide{conn: "local", driver: "mysql", ref: db.TableRef{Namespace: appNS}}
	tgt := compareSide{conn: "staging", driver: "mysql", ref: db.TableRef{Namespace: appNS}}
	drain(t, m, m.openDiff(src, tgt))
	d := m.activeTab().(*diffTab)

	press(t, m, "D") // on "+ audit": only the source has it
	if d.data != nil {
		t.Fatal("compared the data of a table the target lacks")
	}
	wantScreen(t, m, "move to a ~ table")

	b.conn.setData("users", userCols, userRow("1", "alice"))
	staging.setData("users", userCols, userRow("1", "alicia"))
	press(t, m, "j", "D") // on "~ users"
	wantScreen(t, m, "local · app.users ──▶ staging · app.users", "[✓] ~ 1   alicia → alice")
}

func TestDataViewRowLimit(t *testing.T) {
	m, b := newTestModel(t)
	d := openUsersDiff(t, m, b)
	usersData(b.conn)
	d.maxRows = 2
	press(t, m, "D")
	wantScreen(t, m, "✗ source local · app.users: more than 2 rows (compare_max_rows)", "narrows the rows compared")

	d.maxRows = 3 // exactly the rows each side has
	press(t, m, "space", "r")
	wantScreen(t, m, "3 differ")
}

func TestCompareMaxRowsComesFromSettings(t *testing.T) {
	b := newFakeBackend()
	m := New(b, newFakeStore(), config.Settings{PageSize: 2, MaxColumnWidth: 40, MaxRows: 10, CompareMaxRows: 7})
	b.conn.setSchemas(usersSchemas()...)
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	if d := m.activeTab().(*diffTab); d.maxRows != 7 {
		t.Fatalf("maxRows = %d", d.maxRows)
	}
	m2, b2 := newTestModel(t) // settings without compare_max_rows
	if d := openUsersDiff(t, m2, b2); d.maxRows != config.DefaultSettings().CompareMaxRows {
		t.Fatalf("maxRows = %d", d.maxRows)
	}
}

func TestDataViewLoadCanBeCancelled(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	usersData(b.conn)
	b.conn.setBlockData(true)
	_, load := m.Update(uitest.Key("D"))
	wantScreen(t, m, "reading both sides")
	press(t, m, "ctrl+c")
	drain(t, m, load)
	wantScreen(t, m, "✗ source local · app.users: canceled")
}

func TestDataViewReloads(t *testing.T) {
	m, b := newTestModel(t)
	openUsersData(t, m, b)
	b.conn.setData("users_bak", userCols, userRow("1", "alice"), userRow("2", "bob"), userRow("4", "dave"))
	press(t, m, "space", "r")
	wantScreen(t, m, "the rows are the same")
}

// The source's stream breaks after two rows; withConn reconnects and reads
// it again, which must start the side over (not report a key read twice).
func TestDataViewStartsASideOverAfterReconnect(t *testing.T) {
	m, b := newTestModel(t)
	d := openUsersDiff(t, m, b)
	usersData(b.conn)
	b.conn.setStreamDrop(2)
	press(t, m, "D")
	wantScreen(t, m, "3 differ (1 + · 1 - · 1 ~)")
	if d.data.srcErr != "" || d.data.tgtErr != "" {
		t.Fatalf("errors: %q %q", d.data.srcErr, d.data.tgtErr)
	}
}

func TestDataViewScrollsColumns(t *testing.T) {
	m, b := newTestModel(t)
	wide(m)
	three := func(name string) db.TableSchema {
		return schemaOf(name, idColumn(), db.Column{Name: "name", Type: "text"}, db.Column{Name: "note", Type: "text"})
	}
	b.conn.setSchemas(three("users"), three("users_bak"))
	cols := append(slices.Clone(userCols), db.ColumnInfo{Name: "note", DatabaseType: "TEXT"})
	b.conn.setData("users", cols, []db.Value{{Text: "1"}, {Text: "bob"}, {Text: "hi"}})
	b.conn.setData("users_bak", cols, []db.Value{{Text: "1"}, {Text: "bob"}, {Text: "ho"}})
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	press(t, m, "D")
	wantScreen(t, m, "bob", "ho → hi")
	press(t, m, "l")
	if strings.Contains(screen(m), "bob") {
		t.Fatalf("l did not scroll past name:\n%s", screen(m))
	}
	wantScreen(t, m, "1   ho → hi") // the key stays
	press(t, m, "l", "h")
	wantScreen(t, m, "bob")
}

func TestDataViewToggle(t *testing.T) {
	m, b := newTestModel(t)
	d := openUsersData(t, m, b)
	press(t, m, "x") // the ~ row
	wantScreen(t, m, "[ ] ~ 2")
	press(t, m, "j", "x") // the - row
	wantScreen(t, m, "[✓] - 3")
	if strings.Contains(screen(m), "delete skipped") {
		t.Fatalf("the delete is picked:\n%s", screen(m))
	}
	if d.data.picked[0] || !d.data.picked[1] || !d.data.picked[2] {
		t.Fatalf("picked = %v", d.data.picked)
	}
}

func TestDataViewJump(t *testing.T) {
	m, b := newTestModel(t)
	d := openUsersData(t, m, b)
	press(t, m, "]", "c", "]", "c")
	if d.data.cursor != 2 {
		t.Fatalf("cursor = %d", d.data.cursor)
	}
	press(t, m, "]", "c")
	wantScreen(t, m, "no more changes")
	press(t, m, "[", "c")
	if d.data.cursor != 1 {
		t.Fatalf("cursor = %d", d.data.cursor)
	}
}

func TestDataViewSwap(t *testing.T) {
	m, b := newTestModel(t)
	openUsersData(t, m, b)
	press(t, m, "~")
	wantScreen(t, m,
		"local · app.users_bak ──▶ local · app.users",
		"[✓] ~ 2   bob → robert",
		"[✓] + 3   carol",
		"[ ] - 4   dave",
	)
}

func TestDataViewFilter(t *testing.T) {
	m, b := newTestModel(t)
	openUsersData(t, m, b)
	press(t, m, "F")
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	typeText(t, m, "< 3") // after the key column the prompt starts with
	press(t, m, "enter")
	for _, q := range []string{
		"SELECT `id`, `name` FROM `app`.`users` WHERE (id < 3\n)",
		"SELECT `id`, `name` FROM `app`.`users_bak` WHERE (id < 3\n)",
	} {
		if !queried(b.conn, q) {
			t.Errorf("no query %q", q)
		}
	}
	wantScreen(t, m, "WHERE id < 3")
}

func TestDataViewFilterRejectsASecondStatement(t *testing.T) {
	m, b := newTestModel(t)
	openUsersData(t, m, b)
	press(t, m, "F", "ctrl+u")
	typeText(t, m, "1=1; DELETE FROM users")
	press(t, m, "enter")
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatalf("the prompt closed: overlay = %T", m.overlay)
	}
	wantScreen(t, m, "the filter must be a condition")
	if queried(b.conn, "DELETE") {
		t.Fatal("the bad filter reached the server")
	}
}

func TestDataScriptOpensConsoleOnTarget(t *testing.T) {
	m, b := newTestModel(t)
	openUsersData(t, m, b)
	press(t, m, "space", "w")
	c := scriptConsole(t, m)
	if c.conn != "local" || c.ns != appNS {
		t.Fatalf("console on %s %+v", c.conn, c.ns)
	}
	text := c.editor.Value()
	for _, want := range []string{
		"-- sqlvim data sync\n",
		"-- source: local · app.users\n",
		"-- target: local · app.users_bak\n",
		"-- key: id\n",
		"UPDATE `app`.`users_bak` SET `name` = 'bob' WHERE `id` = 2;",
		"INSERT INTO `app`.`users_bak` (`id`, `name`) VALUES (4, 'dave');",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "DELETE") {
		t.Fatalf("the unpicked delete is in the script:\n%s", text)
	}
}

func TestDataScriptNothingPicked(t *testing.T) {
	m, b := newTestModel(t)
	openUsersData(t, m, b)
	press(t, m, "x", "j", "j", "x") // unpick the ~ and the + row
	press(t, m, "space", "w")
	if _, ok := m.activeTab().(*diffTab); !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	wantScreen(t, m, "nothing picked")
}
