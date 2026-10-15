package tree

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

func fixture() *Model {
	m := New([]*Node{
		{Kind: KindConn, Name: "local", Conn: "local"},
		{Kind: KindConn, Name: "prod", Conn: "prod", Prod: true},
	})
	m.SetHeight(10)
	return m
}

func names(m *Model) []string {
	var out []string
	for _, r := range m.visible() {
		out = append(out, r.node.Name)
	}
	return out
}

func TestExpandLoadAndCollapse(t *testing.T) {
	m := fixture()
	if !m.Expand() {
		t.Fatal("first expand of an unloaded node needs a load")
	}
	root := m.Selected()
	if !root.Loading || !root.Expanded {
		t.Fatalf("root = %+v", root)
	}
	m.SetChildren(root, []*Node{{Kind: KindDatabase, Name: "app", Conn: "local"}}, nil)
	if got := strings.Join(names(m), ","); got != "local,app,prod" {
		t.Fatalf("visible = %s", got)
	}
	m.Move(1)
	if m.Selected().Name != "app" || m.Selected().Parent() != root {
		t.Fatalf("selected = %+v", m.Selected())
	}
	m.Collapse() // app is collapsed already: jump to parent
	if m.Selected() != root {
		t.Fatal("h on a collapsed node selects its parent")
	}
	m.Collapse()
	if root.Expanded {
		t.Fatal("h on an expanded node collapses it")
	}
	if m.Expand() {
		t.Fatal("re-expanding a loaded node needs no load")
	}
}

func TestCursorFollowsNodeAcrossAsyncLoads(t *testing.T) {
	m := fixture()
	m.Expand()
	root := m.Selected()
	m.Move(1) // select "prod" while "local" is still loading
	m.SetChildren(root, []*Node{{Name: "a"}, {Name: "b"}}, nil)
	if m.Selected().Name != "prod" {
		t.Fatalf("selected = %s, want prod", m.Selected().Name)
	}
}

func TestLoadErrorAllowsRetry(t *testing.T) {
	m := fixture()
	m.Expand()
	root := m.Selected()
	m.SetChildren(root, nil, errors.New("connection refused"))
	if root.Err != "connection refused" || root.Expanded || root.Loading {
		t.Fatalf("root = %+v", root)
	}
	if !strings.Contains(ansi.Strip(m.View(60, true)), "connection refused") {
		t.Fatal("error must be visible")
	}
	if !m.Expand() || root.Err != "" {
		t.Fatal("expand after an error retries and clears it")
	}
}

func TestCancelLoadAndResetConn(t *testing.T) {
	m := fixture()
	m.Expand()
	root := m.Selected()
	m.CancelLoad(root)
	if root.Loading || root.Expanded {
		t.Fatalf("cancel: %+v", root)
	}
	m.Expand()
	m.SetChildren(root, []*Node{{Name: "app"}}, nil)
	m.Move(1)
	m.ResetConn("local")
	if root.Loaded || root.Expanded || len(root.Children) != 0 {
		t.Fatalf("reset: %+v", root)
	}
	if m.Selected() != root {
		t.Fatal("cursor falls back to the nearest visible ancestor")
	}
}

func TestMovementClampsAndScrolls(t *testing.T) {
	var roots []*Node
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		roots = append(roots, &Node{Kind: KindConn, Name: n})
	}
	m := New(roots)
	m.SetHeight(3)
	m.Move(-5)
	if m.Selected().Name != "a" {
		t.Fatal("clamp at top")
	}
	m.Bottom()
	if m.Selected().Name != "f" {
		t.Fatal("bottom")
	}
	view := ansi.Strip(m.View(20, true))
	if !strings.Contains(view, "f") || strings.Contains(view, "a") {
		t.Fatalf("view must scroll to keep the cursor visible:\n%s", view)
	}
	m.Top()
	m.HalfPage(1)
	if m.Selected().Name != "b" {
		t.Fatalf("half page of height 3 moves 1, got %s", m.Selected().Name)
	}
}

func TestViewMarksProdAndHandlesEmpty(t *testing.T) {
	m := fixture()
	m.SetHeight(10)
	if !strings.Contains(ansi.Strip(m.View(40, false)), "[PROD]") {
		t.Fatal("prod marker")
	}
	empty := New(nil)
	empty.SetHeight(5)
	if empty.Selected() != nil || empty.View(10, true) != "" || empty.Expand() {
		t.Fatal("empty tree")
	}
}

func TestFindVisibleNodes(t *testing.T) {
	m := fixture()
	m.Expand()
	m.SetChildren(m.Selected(), []*Node{
		{Kind: KindDatabase, Name: "app", Conn: "local"},
		{Kind: KindDatabase, Name: "Archive", Conn: "local"},
	}, nil)
	if !m.Find("ar", true) || m.Selected().Name != "Archive" {
		t.Fatalf("selected %q", m.Selected().Name)
	}
	if !m.Find("o", true) || m.Selected().Name != "prod" {
		t.Fatalf("selected %q", m.Selected().Name)
	}
	if !m.Find("o", true) || m.Selected().Name != "local" {
		t.Fatalf("wraps around, selected %q", m.Selected().Name)
	}
	if !m.Find("app", false) || m.Selected().Name != "app" {
		t.Fatalf("backwards wraps, selected %q", m.Selected().Name)
	}
	if m.Find("zzz", true) || m.Find("", true) {
		t.Fatal("no match")
	}
}

func TestBrokenConnection(t *testing.T) {
	m := fixture()
	m.SetBroken("local", true)
	if v := ansi.Strip(m.View(40, true)); !strings.Contains(v, "disconnected") {
		t.Fatalf("view:\n%s", v)
	}
	m.ResetConn("local")
	if v := ansi.Strip(m.View(40, true)); strings.Contains(v, "disconnected") {
		t.Fatal("ResetConn clears the mark")
	}
}

func TestAddRootAndEmpty(t *testing.T) {
	m := New(nil)
	m.SetHeight(5)
	if !m.Empty() || m.Selected() != nil {
		t.Fatal("empty tree")
	}
	m.AddRoot(&Node{Kind: KindConn, Name: "new", Conn: "new"})
	if m.Empty() || m.Selected().Name != "new" {
		t.Fatal("AddRoot selects the new connection")
	}
}

func TestReplaceAndRemoveRoot(t *testing.T) {
	m := fixture()
	m.Expand()
	m.SetChildren(m.Selected(), []*Node{{Kind: KindDatabase, Name: "app", Conn: "local"}}, nil)
	m.Move(1) // app, under local
	m.ReplaceRoot("local", &Node{Kind: KindConn, Name: "laptop", Conn: "laptop"})
	if got := strings.Join(names(m), ","); got != "laptop,prod" {
		t.Fatalf("visible = %s", got)
	}
	if m.Selected().Name != "laptop" {
		t.Fatalf("selected = %s: the replacement keeps the cursor on that connection", m.Selected().Name)
	}
	m.RemoveRoot("laptop")
	if got := strings.Join(names(m), ","); got != "prod" || m.Selected().Name != "prod" {
		t.Fatalf("visible = %s selected = %v", got, m.Selected())
	}
	m.RemoveRoot("prod")
	if !m.Empty() || m.Selected() != nil {
		t.Fatal("empty tree")
	}
}

func TestReveal(t *testing.T) {
	m := fixture()
	local := m.roots[0]
	path := []string{"app", "public"}
	n, res := m.Reveal("local", path)
	if res != NeedsLoad || n != local || !local.Loading {
		t.Fatalf("first step: %v %v", n, res)
	}
	if _, res := m.Reveal("local", path); res != Waiting {
		t.Fatalf("while loading: %v", res)
	}
	m.SetChildren(local, []*Node{{Kind: KindDatabase, Name: "app", Conn: "local", NS: db.Namespace{Database: "app"}}}, nil)
	app := local.Children[0]
	if n, res := m.Reveal("local", path); res != NeedsLoad || n != app || m.Selected() != app {
		t.Fatalf("second step: %v %v", n, res)
	}
	m.SetChildren(app, []*Node{{Kind: KindSchema, Name: "public", Conn: "local", NS: db.Namespace{Database: "app", Schema: "public"}}}, nil)
	if _, res := m.Reveal("local", path); res != Revealed || m.Selected().Name != "public" {
		t.Fatalf("done: %v, selected %q", res, m.Selected().Name)
	}
	if _, res := m.Reveal("local", []string{"nope"}); res != NotFound {
		t.Fatal("unknown database")
	}
	if _, res := m.Reveal("nope", nil); res != NotFound {
		t.Fatal("unknown connection")
	}
}

func TestNamespaceFindsTheNodeListingTables(t *testing.T) {
	m := New([]*Node{
		{Kind: KindConn, Name: "my", Conn: "my"},
		{Kind: KindConn, Name: "pg", Conn: "pg"},
	})
	my, pg := m.Roots()[0], m.Roots()[1]
	m.SetChildren(my, []*Node{{Kind: KindDatabase, Name: "app", Conn: "my", NS: db.Namespace{Database: "app"}}}, nil)
	m.SetChildren(my.Children[0], []*Node{{Kind: KindTable, Name: "users", Conn: "my", NS: db.Namespace{Database: "app"}}}, nil)
	m.SetChildren(pg, []*Node{{Kind: KindDatabase, Name: "app", Conn: "pg", NS: db.Namespace{Database: "app"}}}, nil)
	pub := &Node{Kind: KindSchema, Name: "public", Conn: "pg", NS: db.Namespace{Database: "app", Schema: "public"}}
	m.SetChildren(pg.Children[0], []*Node{pub}, nil)

	if n := m.Namespace("my", db.Namespace{Database: "app"}); n != my.Children[0] {
		t.Fatalf("mysql: %+v", n)
	}
	if n := m.Namespace("pg", db.Namespace{Database: "app", Schema: "public"}); n != nil {
		t.Fatalf("schema not loaded yet, got %+v", n)
	}
	m.SetChildren(pub, nil, nil)
	if n := m.Namespace("pg", db.Namespace{Database: "app", Schema: "public"}); n != pub {
		t.Fatalf("postgres: %+v", n)
	}
	if n := m.Namespace("my", db.Namespace{Database: "other"}); n != nil {
		t.Fatalf("missing: %+v", n)
	}
}

// tableFixture is local → app → users → Columns(id, name) + Keys(PRIMARY),
// with prod unloaded. Columns is expanded, Keys collapsed.
func tableFixture() (m *Model, users, cols, keys *Node) {
	m = fixture()
	root := m.Roots()[0]
	ns := db.Namespace{Database: "app"}
	m.SetChildren(root, []*Node{{Kind: KindDatabase, Name: "app", Conn: "local", NS: ns}}, nil)
	app := root.Children[0]
	users = &Node{Kind: KindTable, Name: "users", Conn: "local", NS: ns}
	m.SetChildren(app, []*Node{users}, nil)
	cols = &Node{Kind: KindGroup, Group: GroupColumns, Name: "Columns", Conn: "local", NS: ns, Expanded: true}
	Adopt(cols, []*Node{
		{Kind: KindColumn, Name: "id", Detail: "bigint PK", Conn: "local", NS: ns},
		{Kind: KindColumn, Name: "name", Detail: "text", Conn: "local", NS: ns},
	})
	keys = &Node{Kind: KindGroup, Group: GroupKeys, Name: "Keys", Conn: "local", NS: ns}
	Adopt(keys, []*Node{{Kind: KindKey, Name: "PRIMARY", Detail: "(id)", Conn: "local", NS: ns}})
	m.SetChildren(users, []*Node{cols, keys}, nil)
	m.SetHeight(20)
	return m, users, cols, keys
}

func TestTableChildren(t *testing.T) {
	m, users, cols, keys := tableFixture()
	if got := strings.Join(names(m), ","); got != "local,app,users,Columns,id,name,Keys,prod" {
		t.Fatalf("visible = %s", got)
	}
	id := cols.Children[0]
	if !users.Expandable() || !cols.Expandable() || id.Expandable() {
		t.Fatal("tables and groups expand, leaves do not")
	}
	if id.Table() != users || cols.Table() != users || users.Table() != users || users.Parent().Table() != nil {
		t.Fatal("Table() finds the table ancestor")
	}
	if id.Parent() != cols || keys.Children[0].Parent() != keys {
		t.Fatal("Adopt links parents")
	}
	view := ansi.Strip(m.View(60, true))
	for _, want := range []string{"▾ • users", "▾ Columns", "· id  bigint PK", "▸ Keys"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	if m.FindTable("local", users.TableRef()) != users || m.FindTable("prod", users.TableRef()) != nil {
		t.Fatal("FindTable")
	}
	if m.Find("bigint", true) {
		t.Fatal("search matches names only, not details")
	}
}

func TestViewIconAndDetailSanitized(t *testing.T) {
	m, users, cols, _ := tableFixture()
	users.Kind = KindView
	users.Expanded = false
	cols.Children[0].Detail = "int\x07"
	view := ansi.Strip(m.View(60, true))
	if !strings.Contains(view, "▸ ◦ users") {
		t.Fatalf("view icon:\n%s", view)
	}
	users.Expanded = true
	if view = m.View(60, true); strings.Contains(view, "\x07") {
		t.Fatal("details are sanitized")
	}
}

func TestReloadKeepsGroupExpansion(t *testing.T) {
	m, users, cols, keys := tableFixture()
	cols.Expanded, keys.Expanded = false, true // the user flipped both
	ns := users.NS
	newCols := &Node{Kind: KindGroup, Group: GroupColumns, Name: "Columns", Conn: "local", NS: ns, Expanded: true}
	Adopt(newCols, []*Node{{Kind: KindColumn, Name: "id", Conn: "local", NS: ns}})
	newKeys := &Node{Kind: KindGroup, Group: GroupKeys, Name: "Keys", Conn: "local", NS: ns}
	Adopt(newKeys, nil)
	m.StartLoad(users)
	m.SetChildren(users, []*Node{newCols, newKeys}, nil)
	if newCols.Expanded || !newKeys.Expanded {
		t.Fatalf("columns expanded=%v keys expanded=%v, want false true", newCols.Expanded, newKeys.Expanded)
	}
}

func TestForget(t *testing.T) {
	m, users, _, _ := tableFixture()
	m.Forget(users)
	if users.Loaded || users.Expanded || users.Children != nil {
		t.Fatalf("users = %+v", users)
	}
	m.selected = users
	if !m.Expand() {
		t.Fatal("expanding a forgotten table loads it again")
	}
}

func TestNerdIcons(t *testing.T) {
	m, users, cols, keys := tableFixture()
	m.SetIcons(IconsNerd)
	m.Roots()[0].Driver = "mysql"
	m.Roots()[1].Driver = "postgres"
	users.Parent().Expanded = true
	keys.Children = append(keys.Children, &Node{Kind: KindForeignKey, Name: "fk"}, &Node{Kind: KindIndex, Name: "ix"})
	keys.Expanded = true
	view := ansi.Strip(m.View(80, true))
	for _, want := range []string{
		"▾ \ue704 local", "▸ \ue76e prod", // the driver's logo
		"▾ \U000f01bc app", "▾ \U000f04eb users",
		"▾ \U000f0770 Columns", "▾ \U000f0770 Keys", // an open folder
		"\U000f0835 id  bigint PK", "\U000f0306 PRIMARY", "\U000f119f fk", "\U000f04bc ix",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	cols.Expanded = false
	users.Kind = KindView
	view = ansi.Strip(m.View(80, true))
	for _, want := range []string{"▸ \U000f024b Columns", "▾ \U000f1094 users"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	schema := &Node{Kind: KindSchema, Name: "public"}
	if got := icon(schema, IconsNerd); got != "▸ \uf0e8" {
		t.Fatalf("schema icon %q", got)
	}
	if got := icon(&Node{Kind: KindConn}, IconsNerd); got != "▸ \U000f01bc" {
		t.Fatalf("a connection of an unknown driver gets the database icon, got %q", got)
	}
}

func TestPlainIconsAreTheDefault(t *testing.T) {
	m, _, _, _ := tableFixture()
	view := ansi.Strip(m.View(60, true))
	if !strings.Contains(view, "▾ • users") || !strings.Contains(view, "· id") {
		t.Fatalf("plain icons:\n%s", view)
	}
}

func TestNerdConnectionIconIsAccented(t *testing.T) {
	m := fixture()
	m.SetIcons(IconsNerd)
	m.Roots()[1].Driver = "postgres"
	if view := m.View(60, true); !strings.Contains(view, theme.Accent.Render("")) {
		t.Fatalf("an unselected connection's logo is drawn in the accent color:\n%q", view)
	}
}
