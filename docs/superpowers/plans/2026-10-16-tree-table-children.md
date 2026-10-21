# 树展开表结构与 z 折叠键 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tables and views in the tree expand into Columns / Keys / Foreign Keys / Indexes groups whose nodes jump to or edit the structure, and the tree gets vim fold keys (`zo zc za zO zC zM zR zx`).

**Architecture:** `panes/tree` gains group and leaf node kinds plus pure fold operations. `ui/loaders.go` loads a table's children with one `MetaReader.TableSchema` call (views: `Columns`) and a pure builder turns it into nodes. The tree keys split into `tree.expand` (`l`, `→`) and a new `tree.open` (`Enter`). Leaf actions reuse `openStructure` / `modifyTable` through a pending "structure focus" on the structure tab, so no new SQL execution path exists.

**Tech Stack:** Go, Bubble Tea v2 (`charm.land/bubbletea/v2`), Lip Gloss v2.

**Spec:** `docs/superpowers/specs/2026-10-16-tree-table-children-design.md`

## Global Constraints

- Dependencies only point downward: `ui` → `ui/panes/*` / `db`. `panes/tree` must not import `ui`.
- Never hardcode a key name in UI text; use `keyHint` / `KeysFor` so `keymap.toml` remaps show up.
- UI text is in English.
- Safety rules stay: read-only connections, prod confirmation, no structure change during an open transaction. Drops from the tree are only *staged* in Modify Table and run through its `:w` preview.
- Conventional Commits with a scope, in Chinese like the recent history (`feat(ui): …`). No `Co-Authored-By` line.
- Every user-visible change gets a line under `[Unreleased]` in `CHANGELOG.md`; README changes go to both `README.md` and `README.zh-CN.md`.
- Tests: `make test` (`go test -race ./...`) and `make lint` must pass at the end of every task.

## Review Focus

1. **A column or index that was renamed or dropped elsewhere** (tree is stale): `Enter` / `dd` on it must not stage a drop of a different row; the structure tab says "<name> is not in <table> any more". → test in Task 5.
2. **`dd` on a column of a read-only connection** must change nothing and show the read-only hint, and must not leave a pending drop that a later `e` would apply. → test in Task 5.
3. **Refreshing a table from one of its leaves** must keep the user's group expansion (a collapsed Columns stays collapsed) and keep the cursor inside the table. → tests in Task 1 and Task 4.
4. **`zR` / `zO` with an unloaded or failed node in the subtree** must not start a load or expand an error node. → test in Task 2.
5. **Control characters in a column type or index predicate** (`Detail`) must be sanitized like names. → test in Task 1.

---

## File Structure

| File | Change |
|---|---|
| `internal/ui/panes/tree/tree.go` | New kinds, `Group`, `Detail`, `Ref`, `Table()`, `Adopt`, `FindTable`, `Forget`, group-state-preserving `SetChildren`, icons and detail rendering |
| `internal/ui/panes/tree/fold.go` (new) | `Fold`, `OpenLoaded`, `CloseAll`, `OpenTree`, `CloseTree`, `FocusPath` |
| `internal/ui/panes/tree/fold_test.go` (new) | fold tests |
| `internal/ui/treenodes.go` (new) | `tableChildren` builder: `db.TableSchema` → group / leaf nodes |
| `internal/ui/treenodes_test.go` (new) | builder tests |
| `internal/ui/loaders.go` | `fetchChildren` gets the node name and loads tables / views |
| `internal/ui/keymap/keymap.go`, `defaults.go` | `Open` and the `Fold*` actions, bindings |
| `internal/ui/treekeys.go` (new) | `expandNode`, `openNode`, `editNode`, `deleteNode`, `refreshNode`, `reloadTreeTable`, `leafFocus`, `openStructureAt`, `modifyTableAt` |
| `internal/ui/treekeys_test.go` (new) | key / leaf action tests |
| `internal/ui/app.go` | `treeAction` delegates to `treekeys.go` |
| `internal/ui/panes/structure/structure.go` | `Find` |
| `internal/ui/structfocus.go` (new) | `structFocus`, `target`, `applyFocus` |
| `internal/ui/tabs.go`, `structedit.go` | `focus` field; apply / clear it on loads |
| `internal/ui/copy.go`, `ddl.go`, `nodemenu.go`, `modify.go` | leaves in yank, context table, menu, reload after Modify Table |
| `README.md`, `README.zh-CN.md`, `CHANGELOG.md`, `docs/superpowers/specs/2026-10-13-datavim-design.md` | docs |

---

### Task 1: Tree node kinds, rendering and group state

**Files:**
- Modify: `internal/ui/panes/tree/tree.go`
- Test: `internal/ui/panes/tree/tree_test.go`

**Interfaces:**
- Produces:
  - `const KindGroup, KindColumn, KindKey, KindForeignKey, KindIndex Kind` (after `KindView`, in that order)
  - `type Group int`; `const GroupColumns, GroupKeys, GroupForeignKeys, GroupIndexes Group`
  - `Node` fields `Group Group`, `Detail string`, `Ref db.TableRef`
  - `func (n *Node) Table() *Node` — n or its nearest table / view ancestor, else nil
  - `func Adopt(n *Node, children []*Node)` — sets parents, `Children`, `Loaded=true`
  - `func (m *Model) FindTable(conn string, ref db.TableRef) *Node`
  - `func (m *Model) Forget(n *Node)` — drops loaded children so the next expand reloads
  - `Expandable()` is true for conn, database, schema, table, view, group

- [ ] **Step 1: Write the failing tests** — append to `internal/ui/panes/tree/tree_test.go`:

```go
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
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/ui/panes/tree/`
Expected: compile errors (`KindGroup`, `Adopt`, `FindTable`, `Forget` undefined).

- [ ] **Step 3: Implement** in `internal/ui/panes/tree/tree.go`:

Replace the `Kind` constants and add `Group`:

```go
const (
	KindConn Kind = iota
	KindDatabase
	KindSchema
	KindTable
	KindView
	KindGroup      // Columns, Keys, Foreign Keys or Indexes under a table
	KindColumn     // the leaves under a group
	KindKey        // a primary or unique key
	KindForeignKey // Ref is the referenced table
	KindIndex
)

// Group tells the groups under a table apart.
type Group int

const (
	GroupColumns Group = iota
	GroupKeys
	GroupForeignKeys
	GroupIndexes
)
```

Add to `Node` (after `NS`):

```go
	Group  Group        // a KindGroup node's group
	Detail string       // shown muted after the name: a column's type, an index's columns
	Ref    db.TableRef  // a foreign key's referenced table
```

Replace `Expandable` and add `Table` and `Adopt`:

```go
func (n *Node) Expandable() bool { return n.Kind <= KindView || n.Kind == KindGroup }

// Table returns n when it is a table or view, else its nearest table or
// view ancestor, else nil.
func (n *Node) Table() *Node {
	for p := n; p != nil; p = p.parent {
		if p.IsTable() {
			return p
		}
	}
	return nil
}

// Adopt makes children the loaded children of n, for nodes built before
// they join the tree (the groups under a table).
func Adopt(n *Node, children []*Node) {
	for _, c := range children {
		c.parent = n
	}
	n.Children, n.Loaded = children, true
}
```

Replace `SetChildren` so loaded children (groups) keep the expansion of the node they replace:

```go
// SetChildren records the result of a load. A loaded child (a group under
// a table) keeps the expansion of the child it replaces.
func (m *Model) SetChildren(n *Node, children []*Node, err error) {
	n.Loading = false
	if err != nil {
		n.Err, n.Expanded, n.Loaded = err.Error(), false, false
		return
	}
	type key struct {
		kind  Kind
		group Group
		name  string
	}
	was := make(map[key]bool, len(n.Children))
	for _, c := range n.Children {
		was[key{c.Kind, c.Group, c.Name}] = c.Expanded
	}
	for _, c := range children {
		c.parent = n
		if exp, ok := was[key{c.Kind, c.Group, c.Name}]; ok && c.Loaded {
			c.Expanded = exp
		}
	}
	n.Children, n.Loaded, n.Expanded, n.Err = children, true, true, ""
}
```

Add after `ResetConn`:

```go
// Forget drops n's children, so that expanding it loads them again.
func (m *Model) Forget(n *Node) {
	n.Children, n.Loaded, n.Expanded, n.Err = nil, false, false, ""
}

// FindTable returns the loaded node of the table or view ref on conn, or nil.
func (m *Model) FindTable(conn string, ref db.TableRef) *Node {
	var walk func(nodes []*Node) *Node
	walk = func(nodes []*Node) *Node {
		for _, n := range nodes {
			switch {
			case n.Conn != conn:
			case n.IsTable():
				if n.TableRef() == ref {
					return n
				}
			case n.Kind < KindTable:
				if found := walk(n.Children); found != nil {
					return found
				}
			}
		}
		return nil
	}
	return walk(m.roots)
}
```

In `View`, right after the `if i == cur { … }` styling block, add:

```go
		if n.Detail != "" {
			label += "  " + theme.Muted.Render(textutil.Sanitize(n.Detail))
		}
```

Replace `icon`:

```go
func icon(n *Node) string {
	arrow := "▸"
	if n.Expanded {
		arrow = "▾"
	}
	switch n.Kind {
	case KindTable:
		return arrow + " •"
	case KindView:
		return arrow + " ◦"
	case KindColumn, KindKey, KindForeignKey, KindIndex:
		return "·"
	}
	return arrow
}
```

- [ ] **Step 4: Run the tree tests**

Run: `go test -race ./internal/ui/panes/tree/`
Expected: PASS.

- [ ] **Step 5: Run the whole suite** — the table icon changed from `• users` to `▸ • users`.

Run: `go test -race ./... 2>&1 | grep -v '^ok'`
Expected: no failures. If a UI test matched `"• users"` / `"◦ active"` literally, update the expected string to `"▸ • users"` / `"▸ ◦ active"`.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/panes/tree
git commit -m "feat(ui): 树节点支持表下的分组与列、键、索引"
```

---

### Task 2: Fold operations in the tree model

**Files:**
- Create: `internal/ui/panes/tree/fold.go`
- Test: `internal/ui/panes/tree/fold_test.go`

**Interfaces:**
- Consumes: `tableFixture()` (Task 1, same package), `Node.Expandable`, `Model.Selected`, `Model.cursor`
- Produces (all on `*Model`): `Fold()`, `OpenLoaded(n *Node)`, `CloseAll(n *Node)`, `OpenTree()`, `CloseTree()`, `FocusPath()`. A nil `n` means every root.

- [ ] **Step 1: Write the failing tests** — `internal/ui/panes/tree/fold_test.go`:

```go
package tree

import (
	"strings"
	"testing"
)

func selectName(t *testing.T, m *Model, name string) *Node {
	t.Helper()
	for _, r := range m.visible() {
		if r.node.Name == name {
			m.selected = r.node
			return r.node
		}
	}
	t.Fatalf("%s not visible: %v", name, names(m))
	return nil
}

func TestFoldOnLeafClosesParent(t *testing.T) {
	m, _, cols, _ := tableFixture()
	selectName(t, m, "id")
	m.Fold()
	if cols.Expanded || m.Selected() != cols {
		t.Fatalf("zc on a leaf closes and selects its group; selected %s", m.Selected().Name)
	}
	m.Fold()
	if m.Selected() != cols.Parent() || cols.Parent().Expanded {
		t.Fatal("zc on a closed group closes the table")
	}
}

func TestCloseAllAndOpenLoaded(t *testing.T) {
	m, users, cols, keys := tableFixture()
	selectName(t, m, "id")
	m.CloseAll(users)
	if users.Expanded || cols.Expanded || keys.Expanded {
		t.Fatal("zC closes the whole subtree")
	}
	if m.Selected() != users {
		t.Fatalf("cursor falls back to the visible ancestor, got %s", m.Selected().Name)
	}
	m.OpenLoaded(users)
	if !users.Expanded || !cols.Expanded || !keys.Expanded {
		t.Fatal("zO opens every loaded node under the cursor")
	}
}

func TestOpenTreeLoadsNothing(t *testing.T) {
	m, users, _, _ := tableFixture()
	m.CloseTree()
	prod := m.Roots()[1]
	broken := &Node{Kind: KindTable, Name: "gone", Conn: "local", NS: users.NS, Err: "not found"}
	users.Parent().Children = append(users.Parent().Children, broken)
	broken.parent = users.Parent()
	m.OpenTree()
	if prod.Expanded || prod.Loading {
		t.Fatal("zR leaves an unloaded root alone")
	}
	if broken.Expanded {
		t.Fatal("zR leaves a failed node alone")
	}
	if got := strings.Join(names(m), ","); got != "local,app,users,Columns,id,name,Keys,PRIMARY,gone,prod" {
		t.Fatalf("visible = %s", got)
	}
}

func TestCloseTreeSelectsTheConnection(t *testing.T) {
	m, _, _, _ := tableFixture()
	selectName(t, m, "name")
	m.CloseTree()
	if got := strings.Join(names(m), ","); got != "local,prod" {
		t.Fatalf("visible = %s", got)
	}
	if m.Selected().Name != "local" {
		t.Fatalf("selected %s", m.Selected().Name)
	}
}

func TestFocusPathKeepsOnlyTheCursorPath(t *testing.T) {
	m, users, _, keys := tableFixture()
	keys.Expanded = true
	selectName(t, m, "PRIMARY")
	m.FocusPath()
	if got := strings.Join(names(m), ","); got != "local,app,users,Columns,Keys,PRIMARY,prod" {
		t.Fatalf("visible = %s", got)
	}
	if m.Selected().Name != "PRIMARY" || !users.Expanded {
		t.Fatal("zx keeps the cursor and its ancestors open")
	}
}

func TestCloseAllLeavesLoadingNodesOpen(t *testing.T) {
	m, users, _, _ := tableFixture()
	m.StartLoad(users)
	m.CloseAll(nil)
	if !users.Expanded {
		t.Fatal("a loading node keeps showing loading…")
	}
}
```

Note `TestCloseAllLeavesLoadingNodesOpen`: `CloseAll(nil)` closes `local` and `app`, so `users` is hidden either way; the check is only that its own flag is untouched while it loads.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/ui/panes/tree/ -run 'Fold|Close|Open|Focus'`
Expected: compile errors (`Fold`, `CloseAll`, … undefined).

- [ ] **Step 3: Implement** — `internal/ui/panes/tree/fold.go`:

```go
package tree

// The vim fold keys: zc, zO, zC, zR, zM, zx. Folding only flips Expanded;
// loaded children stay, so opening again needs no query.

// roots returns n alone, or every root for nil.
func (m *Model) scope(n *Node) []*Node {
	if n == nil {
		return m.roots
	}
	return []*Node{n}
}

// Fold is zc: it closes the selected node, or, on a leaf or a closed
// node, closes its parent and selects it.
func (m *Model) Fold() {
	n := m.Selected()
	if n == nil {
		return
	}
	if n.Expanded && !n.Loading {
		n.Expanded = false
		return
	}
	if p := n.parent; p != nil {
		p.Expanded = false
		m.selected = p
	}
}

// OpenLoaded is zO: it opens n and every loaded node under it (every
// root's for nil) without loading anything. Failed and loading nodes stay
// as they are.
func (m *Model) OpenLoaded(n *Node) {
	var walk func(x *Node)
	walk = func(x *Node) {
		if !x.Expandable() || !x.Loaded || x.Loading || x.Err != "" {
			return
		}
		x.Expanded = true
		for _, c := range x.Children {
			walk(c)
		}
	}
	for _, x := range m.scope(n) {
		walk(x)
	}
}

// CloseAll is zC: it closes n and everything under it (every root for
// nil). A loading node stays open so its "loading…" shows.
func (m *Model) CloseAll(n *Node) {
	var walk func(x *Node)
	walk = func(x *Node) {
		if !x.Loading {
			x.Expanded = false
		}
		for _, c := range x.Children {
			walk(c)
		}
	}
	for _, x := range m.scope(n) {
		walk(x)
	}
	m.cursor(m.visible())
}

// OpenTree is zR: every loaded node opens.
func (m *Model) OpenTree() { m.OpenLoaded(nil) }

// CloseTree is zM: everything closes down to the connections, and the
// cursor goes to its connection.
func (m *Model) CloseTree() {
	sel := m.Selected()
	m.CloseAll(nil)
	for sel != nil && sel.parent != nil {
		sel = sel.parent
	}
	if sel != nil {
		m.selected = sel
	}
}

// FocusPath is zx: everything closes but the path to the cursor.
func (m *Model) FocusPath() {
	sel := m.Selected()
	if sel == nil {
		return
	}
	m.CloseAll(nil)
	for p := sel.parent; p != nil; p = p.parent {
		p.Expanded = true
	}
	m.selected = sel
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/ui/panes/tree/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/panes/tree
git commit -m "feat(ui): 树的折叠操作（zc zO zC zR zM zx）"
```

---

### Task 3: Load a table's children

**Files:**
- Create: `internal/ui/treenodes.go`, `internal/ui/treenodes_test.go`
- Modify: `internal/ui/loaders.go` (`loadChildren`, `fetchChildren`)

**Interfaces:**
- Consumes: `tree.Adopt`, `tree.KindGroup…KindIndex`, `tree.Group*` (Task 1)
- Produces: `func tableChildren(conn string, s db.TableSchema) []*tree.Node`; `fetchChildren(ctx, c, kind, connName, ns, name string)` loads `KindTable` (via `TableSchema`) and `KindView` (via `Columns`).

- [ ] **Step 1: Write the failing tests** — `internal/ui/treenodes_test.go`:

```go
package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// render lists nodes as "Group: name detail; …" for compact comparison.
func render(groups []*tree.Node) string {
	var b strings.Builder
	for _, g := range groups {
		b.WriteString(g.Name + ":")
		for _, c := range g.Children {
			b.WriteString(" " + c.Name)
			if c.Detail != "" {
				b.WriteString(" " + c.Detail)
			}
			b.WriteString(";")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func TestTableChildrenGroups(t *testing.T) {
	app := db.Namespace{Database: "app"}
	s := db.TableSchema{
		Ref: db.TableRef{Namespace: app, Name: "users"},
		Columns: []db.Column{
			{Name: "org_id", Type: "bigint", Position: 3},
			{Name: "id", Type: "bigint", PrimaryKey: true, Position: 1},
			{Name: "email", Type: "varchar(255)", Position: 2},
		},
		Indexes: []db.Index{
			{Name: "idx_org", Columns: []string{"org_id"}},
			{Name: "uk_email", Columns: []string{"email"}, Unique: true},
			{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true},
			{Name: "idx_tags", Columns: []string{"tags"}, Method: "gin", Where: "tags IS NOT NULL"},
		},
		ForeignKeys: []db.ForeignKey{
			{Name: "fk_org", Columns: []string{"org_id"}, RefTable: db.TableRef{Namespace: app, Name: "orgs"}, RefColumns: []string{"id"}},
			{Name: "fk_audit", Columns: []string{"id"}, RefTable: db.TableRef{Namespace: db.Namespace{Database: "audit"}, Name: "log"}, RefColumns: []string{"user_id"}},
		},
	}
	groups := tableChildren("local", s)
	want := "Columns: id bigint PK; email varchar(255); org_id bigint;\n" +
		"Keys: PRIMARY (id); uk_email (email);\n" +
		"Foreign Keys: fk_org (org_id) → orgs(id); fk_audit (id) → audit.log(user_id);\n" +
		"Indexes: idx_org (org_id); uk_email (email) unique; PRIMARY (id) unique; idx_tags (tags) gin where tags IS NOT NULL;\n"
	if got := render(groups); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if !groups[0].Expanded || groups[1].Expanded {
		t.Fatal("Columns starts expanded, the other groups closed")
	}
	fk := groups[2].Children[0]
	if fk.Kind != tree.KindForeignKey || fk.Ref.Name != "orgs" || fk.Parent() != groups[2] {
		t.Fatalf("fk = %+v", fk)
	}
	for _, g := range groups {
		if g.Kind != tree.KindGroup || !g.Loaded || g.Conn != "local" || g.NS != app {
			t.Fatalf("group %+v", g)
		}
	}
}

func TestTableChildrenHidesEmptyGroups(t *testing.T) {
	groups := tableChildren("local", db.TableSchema{Columns: []db.Column{{Name: "a", Type: "int"}}})
	if len(groups) != 1 || groups[0].Group != tree.GroupColumns {
		t.Fatalf("groups = %s", render(groups))
	}
}

func TestFetchChildrenOfTableAndView(t *testing.T) {
	c := &fakeConn{}
	c.setSchemas(usersSchema())
	app := db.Namespace{Database: "app"}
	nodes, err := fetchChildren(context.Background(), c, tree.KindTable, "local", app, "users")
	// usersSchema has columns and only the PRIMARY index: Keys and Indexes both list it.
	if err != nil || len(nodes) != 3 || nodes[0].Name != "Columns" || nodes[1].Name != "Keys" || nodes[2].Name != "Indexes" {
		t.Fatalf("table: %v %s", err, render(nodes))
	}
	if _, err := fetchChildren(context.Background(), c, tree.KindTable, "local", app, "missing"); err == nil || !strings.Contains(err.Error(), "table missing not found") {
		t.Fatalf("missing table: %v", err)
	}
	nodes, err = fetchChildren(context.Background(), c, tree.KindView, "local", app, "active")
	if err != nil || len(nodes) != 1 || nodes[0].Group != tree.GroupColumns {
		t.Fatalf("view: %v %s", err, render(nodes))
	}
}
```

`usersSchema()` lives in `structedit_test.go`; `fakeConn` (in `fakes_test.go`) implements `db.Conn`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/ui/ -run 'TableChildren|FetchChildren'`
Expected: compile errors (`tableChildren` undefined, `fetchChildren` arity).

- [ ] **Step 3: Implement the builder** — `internal/ui/treenodes.go`:

```go
package ui

import (
	"cmp"
	"slices"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// tableChildren builds what the tree shows under a table or view:
// Columns, Keys (primary and unique), Foreign Keys and Indexes, leaving
// out empty groups. Columns starts expanded.
func tableChildren(conn string, s db.TableSchema) []*tree.Node {
	ns := s.Ref.Namespace
	leaf := func(kind tree.Kind, name, detail string) *tree.Node {
		return &tree.Node{Kind: kind, Name: name, Detail: detail, Conn: conn, NS: ns}
	}
	var groups []*tree.Node
	add := func(g tree.Group, name string, children []*tree.Node) {
		if len(children) == 0 {
			return
		}
		n := &tree.Node{Kind: tree.KindGroup, Group: g, Name: name, Conn: conn, NS: ns, Expanded: g == tree.GroupColumns}
		tree.Adopt(n, children)
		groups = append(groups, n)
	}

	cols := slices.Clone(s.Columns)
	slices.SortStableFunc(cols, func(a, b db.Column) int { return cmp.Compare(a.Position, b.Position) })
	var columns []*tree.Node
	for _, c := range cols {
		detail := c.Type
		if c.PrimaryKey {
			detail += " PK"
		}
		columns = append(columns, leaf(tree.KindColumn, c.Name, detail))
	}
	add(tree.GroupColumns, "Columns", columns)

	var keys []*tree.Node
	for _, primary := range []bool{true, false} {
		for _, ix := range s.Indexes {
			if ix.Primary == primary && (ix.Primary || ix.Unique) {
				keys = append(keys, leaf(tree.KindKey, ix.Name, columnList(ix.Columns)))
			}
		}
	}
	add(tree.GroupKeys, "Keys", keys)

	var fks []*tree.Node
	for _, fk := range s.ForeignKeys {
		n := leaf(tree.KindForeignKey, fk.Name, columnList(fk.Columns)+" → "+refName(ns, fk.RefTable)+columnList(fk.RefColumns))
		n.Ref = fk.RefTable
		fks = append(fks, n)
	}
	add(tree.GroupForeignKeys, "Foreign Keys", fks)

	var indexes []*tree.Node
	for _, ix := range s.Indexes {
		detail := columnList(ix.Columns)
		if ix.Unique {
			detail += " unique"
		}
		if ix.Method != "" && ix.Method != "btree" {
			detail += " " + ix.Method
		}
		if ix.Where != "" {
			detail += " where " + ix.Where
		}
		indexes = append(indexes, leaf(tree.KindIndex, ix.Name, detail))
	}
	add(tree.GroupIndexes, "Indexes", indexes)
	return groups
}

func columnList(cols []string) string { return "(" + strings.Join(cols, ", ") + ")" }

// refName is a referenced table's name, qualified when it lives in
// another database or schema than ns.
func refName(ns db.Namespace, ref db.TableRef) string {
	if ref.Namespace == ns {
		return ref.Name
	}
	q := ref.Schema
	if q == "" {
		q = ref.Database
	}
	return q + "." + ref.Name
}
```

The `Keys` loop lists the primary key first, then the unique indexes in the driver's order; `(ix.Primary || ix.Unique)` keeps a primary key even if a driver leaves `Unique` unset.

- [ ] **Step 4: Load tables and views** — in `internal/ui/loaders.go`:

`loadChildren`: capture the name and pass it:

```go
	kind, connName, ns, name := n.Kind, n.Conn, n.NS, n.Name
	…
			children, err = fetchChildren(ctx, c, kind, connName, ns, name)
```

`fetchChildren`: new signature and two cases before the final `return`:

```go
func fetchChildren(ctx context.Context, c db.Conn, kind tree.Kind, connName string, ns db.Namespace, name string) ([]*tree.Node, error) {
	…
	case tree.KindTable:
		ref := db.TableRef{Namespace: ns, Name: name}
		s, err := meta.TableSchema(ctx, ref)
		if errors.Is(err, db.ErrNotFound) {
			return nil, fmt.Errorf("table %s not found", name)
		}
		if err != nil {
			return nil, err
		}
		return tableChildren(connName, s), nil
	case tree.KindView:
		ref := db.TableRef{Namespace: ns, Name: name}
		cols, err := meta.Columns(ctx, ref)
		if err != nil {
			return nil, err
		}
		return tableChildren(connName, db.TableSchema{Ref: ref, Columns: cols}), nil
	}
```

Add `"errors"` to the imports if missing. Update any other caller of `fetchChildren` (`grep -n 'fetchChildren(' internal/ui/*.go`) to pass the name.

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/ui/ -run 'TableChildren|FetchChildren'` then `go test -race ./...`
Expected: PASS (the app still opens data on `l`; nothing reaches the new cases yet except through tests).

- [ ] **Step 6: Commit**

```bash
git add internal/ui/treenodes.go internal/ui/treenodes_test.go internal/ui/loaders.go
git commit -m "feat(ui): 读取表结构生成树中的列、键、外键和索引节点"
```

---

### Task 4: Keys — `l` expands, `Enter` opens, z folds, refresh

**Files:**
- Modify: `internal/ui/keymap/keymap.go`, `internal/ui/keymap/defaults.go`, `internal/ui/keymap/keymap_test.go`
- Create: `internal/ui/treekeys.go`, `internal/ui/treekeys_test.go`
- Modify: `internal/ui/app.go` (`treeAction`), `internal/ui/nodemenu.go` (table entry), existing UI tests that open a table with `l`

**Interfaces:**
- Consumes: Task 1 (`Table()`), Task 2 (fold methods), Task 3 (loading)
- Produces:
  - `keymap.Open = "tree.open"`, `FoldOpen = "tree.fold.open"`, `FoldClose = "tree.fold.close"`, `FoldToggle = "tree.fold.toggle"`, `FoldOpenAll = "tree.fold.open_all"`, `FoldCloseAll = "tree.fold.close_all"`, `FoldCloseTree = "tree.fold.close_tree"`, `FoldOpenTree = "tree.fold.open_tree"`, `FoldFocus = "tree.fold.focus"`
  - `func (m *Model) expandNode() tea.Cmd`, `func (m *Model) openNode() tea.Cmd`, `func (m *Model) refreshNode() tea.Cmd` (in `treekeys.go`; Task 5 extends `openNode`)

- [ ] **Step 1: Write the failing keymap test** — in `internal/ui/keymap/keymap_test.go` `TestResolve`, replace `{Tree, []string{"z"}, "", NoMatch},` with:

```go
		{Tree, []string{"z"}, "", Pending},
		{Tree, []string{"z", "M"}, FoldCloseTree, Matched},
		{Tree, []string{"z", "o"}, FoldOpen, Matched},
		{Tree, []string{"enter"}, Open, Matched},
		{Tree, []string{"right"}, Expand, Matched},
```

- [ ] **Step 2: Write the failing UI tests** — `internal/ui/treekeys_test.go`:

```go
package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
)

// expandUsers selects local/app/users and expands it with l.
func expandUsers(t *testing.T, m *Model, b *fakeBackend) {
	t.Helper()
	b.conn.setSchemas(usersSchema())
	selectNode(t, m, "local", "app", "users")
	press(t, m, "l")
}

func TestLExpandsATableAndEnterOpensIt(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	if len(m.tabs) != 0 {
		t.Fatal("l on a table expands it, it opens no tab")
	}
	s := screen(m)
	for _, want := range []string{"Columns", "id  bigint PK", "Keys", "Indexes"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
	press(t, m, "enter")
	if !strings.Contains(screen(m), "[1]users") {
		t.Fatalf("Enter on a table opens its data:\n%s", screen(m))
	}
}

func TestEnterOnGroupToggles(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Keys")
	press(t, m, "enter")
	if !m.tree.Selected().Expanded {
		t.Fatal("Enter opens a group")
	}
	press(t, m, "enter")
	if m.tree.Selected().Expanded {
		t.Fatal("Enter again closes it")
	}
}

func TestZKeysFold(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	press(t, m, "z", "M")
	if s := screen(m); strings.Contains(s, "users") {
		t.Fatalf("zM closes everything:\n%s", s)
	}
	press(t, m, "z", "R")
	if s := screen(m); !strings.Contains(s, "id  bigint PK") || !strings.Contains(s, "PRIMARY") {
		t.Fatalf("zR opens everything loaded:\n%s", s)
	}
	if prod := m.tree.Roots()[1]; prod.Expanded || prod.Loading {
		t.Fatal("zR loads nothing")
	}
	selectNode(t, m, "local", "app", "users", "Columns", "id")
	press(t, m, "z", "c")
	if n := m.tree.Selected(); n.Name != "Columns" || n.Expanded {
		t.Fatalf("zc on a column closes Columns; selected %+v", n)
	}
	press(t, m, "z", "a")
	if !m.tree.Selected().Expanded {
		t.Fatal("za opens it again")
	}
}

func TestRefreshFromALeafReloadsTheTable(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Keys")
	press(t, m, "l") // the user opened Keys
	s := usersSchema()
	s.Columns = append(s.Columns, db.Column{Name: "email", Type: "text", Position: 3})
	b.conn.setSchemas(s)
	selectNode(t, m, "local", "app", "users", "Columns", "id")
	press(t, m, "space", "r")
	if !strings.Contains(screen(m), "email  text") {
		t.Fatalf("refresh on a column reloads the table:\n%s", screen(m))
	}
	users := m.tree.FindTable("local", db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"})
	if !users.Children[1].Expanded {
		t.Fatal("Keys stays open across the reload")
	}
	if sel := m.tree.Selected(); sel.Table() != users {
		t.Fatalf("cursor stays in the table, got %s", sel.Name)
	}
}

func TestRefreshOnAnUnloadedTableDoesNotExpandIt(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setSchemas(usersSchema())
	selectNode(t, m, "local", "app", "users")
	press(t, m, "space", "r")
	if n := m.tree.Selected(); n.Expanded || n.Loaded {
		t.Fatalf("users = %+v", n)
	}
}
```

- [ ] **Step 3: Run to verify they fail**

Run: `go test ./internal/ui/keymap/ ./internal/ui/ -run 'Resolve|Expands|Group|ZKeys|Refresh'`
Expected: compile error on `FoldCloseTree` / `Open`, then UI failures.

- [ ] **Step 4: Add the actions** — `internal/ui/keymap/keymap.go`, after `Collapse`:

```go
	Open              Action = "tree.open"
	FoldOpen          Action = "tree.fold.open"
	FoldClose         Action = "tree.fold.close"
	FoldToggle        Action = "tree.fold.toggle"
	FoldOpenAll       Action = "tree.fold.open_all"
	FoldCloseAll      Action = "tree.fold.close_all"
	FoldOpenTree      Action = "tree.fold.open_tree"
	FoldCloseTree     Action = "tree.fold.close_tree"
	FoldFocus         Action = "tree.fold.focus"
```

`internal/ui/keymap/defaults.go`, replace the three `Expand` tree bindings with:

```go
		Binding{Tree, keys("l"), Expand, "Expand"},
		Binding{Tree, keys("right"), Expand, "Expand"},
		Binding{Tree, keys("enter"), Open, "Open: table data, a column or index in the structure, a foreign key's table"},
```

and after the `YankQualified` binding append:

```go
		Binding{Tree, keys("z", "o"), FoldOpen, "Expand"},
		Binding{Tree, keys("z", "c"), FoldClose, "Collapse (on a leaf, its parent)"},
		Binding{Tree, keys("z", "a"), FoldToggle, "Toggle expanded"},
		Binding{Tree, keys("z", "O"), FoldOpenAll, "Expand everything loaded under the cursor"},
		Binding{Tree, keys("z", "C"), FoldCloseAll, "Collapse everything under the cursor"},
		Binding{Tree, keys("z", "R"), FoldOpenTree, "Expand everything loaded"},
		Binding{Tree, keys("z", "M"), FoldCloseTree, "Collapse everything"},
		Binding{Tree, keys("z", "x"), FoldFocus, "Collapse all but the path to the cursor"},
```

- [ ] **Step 5: Implement the tree keys** — `internal/ui/treekeys.go`:

```go
package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// expandNode is l: it expands the selected node, loading its children
// when needed; on a disconnected connection it retries first.
func (m *Model) expandNode() tea.Cmd {
	t := m.tree
	n := t.Selected()
	if n == nil {
		return nil
	}
	if n.Kind == tree.KindConn && n.Broken {
		m.backend.Reset(n.Conn)
		t.ResetConn(n.Conn)
	}
	if t.Expand() {
		return loadChildren(m.backend, n)
	}
	return nil
}

// openNode is Enter: a table's data; a group toggles; anything else expands.
func (m *Model) openNode() tea.Cmd {
	n := m.tree.Selected()
	if n == nil {
		return nil
	}
	switch n.Kind {
	case tree.KindTable, tree.KindView:
		return m.openData(n.Conn, n.TableRef())
	case tree.KindGroup:
		if n.Expanded {
			n.Expanded = false
			return nil
		}
	}
	return m.expandNode()
}

// toggleNode is za.
func (m *Model) toggleNode() tea.Cmd {
	if n := m.tree.Selected(); n != nil && n.Expanded && !n.Loading {
		n.Expanded = false
		return nil
	}
	return m.expandNode()
}

// refreshNode is <space>r in the tree: a table, or the table of a group or
// leaf, reloads its children when they were loaded; a connection, database
// or schema reloads its children.
func (m *Model) refreshNode() tea.Cmd {
	t := m.tree
	n := t.Selected()
	if n == nil || n.Loading {
		return nil
	}
	if tbl := n.Table(); tbl != nil {
		if !tbl.Loaded || tbl.Loading {
			return nil
		}
		n = tbl
	} else if !n.Expandable() {
		return nil
	}
	t.StartLoad(n)
	return loadChildren(m.backend, n)
}
```

Cursor note for `refreshNode` from a leaf: the old leaf's ancestors are its old group and then the table, which is still in the tree, so `cursor()` falls back to the table — the test checks `sel.Table() == users`.

- [ ] **Step 6: Dispatch** — in `internal/ui/app.go` `treeAction`, replace the `keymap.Expand` and `keymap.Refresh` cases with:

```go
	case keymap.Expand, keymap.FoldOpen:
		return m.expandNode()
	case keymap.Open:
		return m.openNode()
	case keymap.FoldToggle:
		return m.toggleNode()
	case keymap.FoldClose:
		t.Fold()
	case keymap.FoldOpenAll:
		t.OpenLoaded(t.Selected())
	case keymap.FoldCloseAll:
		t.CloseAll(t.Selected())
	case keymap.FoldOpenTree:
		t.OpenTree()
	case keymap.FoldCloseTree:
		t.CloseTree()
	case keymap.FoldFocus:
		t.FocusPath()
	case keymap.Refresh:
		return m.refreshNode()
```

In `internal/ui/nodemenu.go`, the table entry `{keymap.Expand, "Open data"}` becomes `{keymap.Open, "Open data"}`.

- [ ] **Step 7: Run the new tests**

Run: `go test -race ./internal/ui/keymap/ ./internal/ui/ -run 'Resolve|Expands|Group|ZKeys|Refresh'`
Expected: PASS.

- [ ] **Step 8: Move the old tests from `l` to `Enter`** — run `go test ./internal/ui/ 2>&1 | grep -- '--- FAIL'`. Every failure where a test pressed `l` (or `right`) **on a table or view to open its data** now expands it instead. Change that one key press to `enter` and nothing else. Known sites:
  - `app_test.go` `openUsers`: `press(t, m, "l", "j", "l", "j", "enter")`
  - `app_test.go` `TestBrowseToTableData`: the last `press(t, m, "j", "l")` → `press(t, m, "j", "enter")`
  - `app_test.go` `TestReopeningATableReusesItsTab`: `press(t, m, "ctrl+h", "enter")`
  - tests asserting the help text `"Expand / open"` → `"Expand"`
  Re-run until `go test -race ./...` is green. Do not change a test's intent; if a failure is not about opening a table with `l`, stop and investigate.

- [ ] **Step 9: Lint and commit**

Run: `make lint && make test`

```bash
git add internal/ui
git commit -m "feat(keymap): 树中 l 展开表、Enter 打开数据，增加 z 折叠键"
```

---

### Task 5: Leaf actions — jump, modify and stage drops in the structure tab

**Files:**
- Modify: `internal/ui/panes/structure/structure.go` (`Find`)
- Create: `internal/ui/structfocus.go`
- Modify: `internal/ui/tabs.go` (`structureTab.focus` field; `Update` on `structureLoadedMsg`), `internal/ui/structedit.go` (`schemaLoaded`), `internal/ui/treekeys.go`, `internal/ui/app.go` (`EditNode`, `DeleteNode` cases)
- Test: `internal/ui/treekeys_test.go`, `internal/ui/panes/structure/structure_test.go`

**Interfaces:**
- Consumes: `openNode` (Task 4), `tree.Node.Table()`, `tablePath` (`tableops.go`), `m.reveal` (`search.go`), `errViewNotModifiable` (`structedit.go`)
- Produces:
  - `func (m *structure.Model) Find(s Section, name string) (int, bool)`
  - `type structFocus struct { section structure.Section; name string; drop bool }`
  - `func (t *structureTab) target(f structFocus)`, `func (t *structureTab) applyFocus()`
  - `func leafFocus(n *tree.Node) (structFocus, bool)`
  - `func (m *Model) openStructureAt(conn string, ref db.TableRef, f structFocus) tea.Cmd`
  - `func (m *Model) modifyTableAt(conn string, ref db.TableRef, f structFocus) tea.Cmd`
  - `func (m *Model) editNode() tea.Cmd`, `func (m *Model) deleteNode() tea.Cmd`
  - `var errFKNotModifiable`

- [ ] **Step 1: Write the failing structure test** — append to `internal/ui/panes/structure/structure_test.go` (it already has `data()`):

```go
func TestFind(t *testing.T) {
	m := New(sqlx.MySQL)
	m.SetData(data())
	for _, c := range data().Columns {
		if row, ok := m.Find(Columns, c.Name); !ok || data().Columns[row].Name != c.Name {
			t.Fatalf("Find(Columns, %s) = %d %v", c.Name, row, ok)
		}
	}
	if _, ok := m.Find(Columns, "nope"); ok {
		t.Fatal("unknown name")
	}
	ix := data().Indexes[0].Name
	if row, ok := m.Find(Indexes, ix); !ok || row != 0 {
		t.Fatalf("Find(Indexes, %s) = %d %v", ix, row, ok)
	}
}
```

(If `data()` has no indexes, add one to the fixture: `Indexes: []db.Index{{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true}}`. Import `sqlx` if the file does not yet.)

- [ ] **Step 2: Write the failing UI tests** — append to `internal/ui/treekeys_test.go`:

```go
func structTab(t *testing.T, m *Model) *structureTab {
	t.Helper()
	st, ok := m.activeTab().(*structureTab)
	if !ok {
		t.Fatalf("active tab %T:\n%s", m.activeTab(), screen(m))
	}
	return st
}

func TestEnterOnColumnOpensItsStructureRow(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Columns", "name")
	press(t, m, "enter")
	st := structTab(t, m)
	if st.view.Section() != structure.Columns || st.view.CursorName() != "name" || st.ed != nil {
		t.Fatalf("section %v cursor %q editing %v", st.view.Section(), st.view.CursorName(), st.ed != nil)
	}
}

func TestEnterOnIndexOpensTheIndexesSection(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Indexes", "PRIMARY")
	press(t, m, "enter")
	st := structTab(t, m)
	if st.view.Section() != structure.Indexes || st.view.CursorName() != "PRIMARY" {
		t.Fatalf("section %v cursor %q", st.view.Section(), st.view.CursorName())
	}
}

func TestEOnColumnModifiesAtIt(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Columns", "name")
	press(t, m, "e")
	st := structTab(t, m)
	if st.ed == nil || st.view.CursorName() != "name" || st.changes() != 0 {
		t.Fatalf("editing %v cursor %q changes %d", st.ed != nil, st.view.CursorName(), st.changes())
	}
}

func TestDDOnColumnStagesItsDrop(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Columns", "name")
	press(t, m, "d", "d")
	st := structTab(t, m)
	if st.ed == nil || st.changes() != 1 {
		t.Fatalf("editing %v changes %d", st.ed != nil, st.changes())
	}
	if execs := b.conn.execList(); len(execs) != 0 {
		t.Fatalf("dd only stages; ran %q", execs)
	}
	if !strings.Contains(screen(m), "drop name staged") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestDDOnColumnOfReadOnlyConnection(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	if err := b.SetReadOnly("local", true); err != nil {
		t.Fatal(err)
	}
	selectNode(t, m, "local", "app", "users", "Columns", "name")
	press(t, m, "d", "d")
	if !strings.Contains(screen(m), "read-only") {
		t.Fatalf("status:\n%s", screen(m))
	}
	if err := b.SetReadOnly("local", false); err != nil {
		t.Fatal(err)
	}
	if st, ok := m.activeTab().(*structureTab); ok {
		press(t, m, "e") // modifying later must not apply the refused drop
		if st.changes() != 0 {
			t.Fatal("a refused dd left a pending drop")
		}
	}
}

func TestStaleLeafDropsNothing(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Columns", "name")
	s := usersSchema()
	s.Columns = s.Columns[:1] // name was dropped elsewhere; the tree is stale
	b.conn.setSchemas(s)
	press(t, m, "d", "d")
	st := structTab(t, m)
	if st.changes() != 0 {
		t.Fatal("a stale leaf staged a drop of another row")
	}
	if !strings.Contains(screen(m), "name is not in users any more") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestForeignKeyLeaf(t *testing.T) {
	m, b := newTestModel(t)
	app := db.Namespace{Database: "app"}
	b.conn.setTables([]db.Table{{Name: "orgs"}, {Name: "users"}})
	s := usersSchema()
	s.ForeignKeys = []db.ForeignKey{{Name: "fk_org", Columns: []string{"name"}, RefTable: db.TableRef{Namespace: app, Name: "orgs"}, RefColumns: []string{"id"}}}
	b.conn.setSchemas(s)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "l")
	selectNode(t, m, "local", "app", "users", "Foreign Keys", "fk_org")
	press(t, m, "e")
	if !strings.Contains(screen(m), "foreign keys cannot be modified yet") {
		t.Fatalf("e on a foreign key:\n%s", screen(m))
	}
	press(t, m, "enter")
	if n := m.tree.Selected(); n.Name != "orgs" || n.Kind != tree.KindTable {
		t.Fatalf("Enter on a foreign key selects the referenced table, got %+v", n)
	}
}

func TestViewColumnsCannotBeModified(t *testing.T) {
	m, _ := newTestModel(t)
	selectNode(t, m, "local", "app", "active")
	press(t, m, "l")
	selectNode(t, m, "local", "app", "active", "Columns", "id")
	press(t, m, "d", "d")
	if !strings.Contains(screen(m), "views cannot be modified") {
		t.Fatalf("dd on a view's column:\n%s", screen(m))
	}
	if len(m.tabs) != 0 {
		t.Fatal("no tab opened")
	}
}

func TestDDOnGroupExplains(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Columns")
	press(t, m, "d", "d")
	if !strings.Contains(screen(m), "select a column or index to drop it") {
		t.Fatalf("dd on a group:\n%s", screen(m))
	}
}
```

Add imports `github.com/shepherd-yun/datavim/internal/ui/panes/structure` and `…/panes/tree` to the test file.

- [ ] **Step 3: Run to verify they fail**

Run: `go test ./internal/ui/panes/structure/ ./internal/ui/ -run 'Find|Leaf|Column|Index|Foreign|Group|ReadOnly'`
Expected: compile errors (`Find`), then failures (`dd` on a column drops the table prompt / does nothing).

- [ ] **Step 4: `structure.Model.Find`** — in `internal/ui/panes/structure/structure.go`, after `CursorName`:

```go
// Find returns the row of the column (Columns) or index (Indexes) called
// name: among the staged rows while editing, else among the rows read.
func (m *Model) Find(s Section, name string) (int, bool) {
	var names []string
	switch {
	case s == Columns && m.edit != nil:
		for _, r := range m.edit.Columns() {
			names = append(names, r.Col.Name)
		}
	case s == Columns:
		for _, c := range m.data.Columns {
			names = append(names, c.Name)
		}
	case s == Indexes && m.edit != nil:
		for _, r := range m.edit.Indexes() {
			names = append(names, r.Index.Name)
		}
	case s == Indexes:
		for _, ix := range m.data.Indexes {
			names = append(names, ix.Name)
		}
	}
	i := slices.Index(names, name)
	return i, i >= 0
}
```

(Add `"slices"` to the imports.)

- [ ] **Step 5: The structure focus** — `internal/ui/structfocus.go`:

```go
package ui

import "github.com/shepherd-yun/datavim/internal/ui/panes/structure"

// structFocus is where the tree asked a structure tab to put its cursor:
// the row called name in section, staging its drop when drop is set.
type structFocus struct {
	section structure.Section
	name    string
	drop    bool
}

// target remembers f and applies it as soon as the tab can.
func (t *structureTab) target(f structFocus) {
	t.focus = &f
	t.applyFocus()
}

// applyFocus moves the cursor to the pending focus once the structure is
// read and, to stage a drop, once the table is being modified.
func (t *structureTab) applyFocus() {
	f := t.focus
	if f == nil || t.loading || (f.drop && t.ed == nil) {
		return
	}
	t.focus = nil
	row, ok := t.view.Find(f.section, f.name)
	if !ok {
		t.setStatus(f.name+" is not in "+t.ref.Name+" any more", true)
		return
	}
	t.view.SetCursor(f.section, row)
	if !f.drop {
		return
	}
	t.setStatus("", false)
	t.deleteRow(f.section == structure.Indexes, row)
	if t.status == "" {
		t.setStatus(joinHints("drop "+f.name+" staged", t.unsavedHint("submits", "discards")), false)
	}
}
```

In `internal/ui/tabs.go` `structureTab`, add after `statusErr bool`:

```go
	focus *structFocus // where the tree asked for the cursor, until the rows are there
```

In `structureTab.Update`, case `structureLoadedMsg`: in the error branch set `t.focus = nil` before `return nil`; after `t.view.SetData(m.data)` add `t.applyFocus()`.

In `internal/ui/structedit.go` `schemaLoaded`: in the error branch add `t.focus = nil` (so a refused or failed Modify Table never applies a drop later); at the very end, after `t.setStatus("", false)`, add `t.applyFocus()`.

- [ ] **Step 6: Leaf actions** — append to `internal/ui/treekeys.go` (add imports `errors`, `db`, `structure`):

```go
var errFKNotModifiable = errors.New("foreign keys cannot be modified yet · change them in a console")

// leafFocus is the structure row of a column, key or index node.
func leafFocus(n *tree.Node) (structFocus, bool) {
	switch n.Kind {
	case tree.KindColumn:
		return structFocus{section: structure.Columns, name: n.Name}, true
	case tree.KindKey, tree.KindIndex:
		return structFocus{section: structure.Indexes, name: n.Name}, true
	}
	return structFocus{}, false
}

// openStructureAt opens ref's structure tab with the cursor on f.
func (m *Model) openStructureAt(conn string, ref db.TableRef, f structFocus) tea.Cmd {
	cmd := m.openStructure(conn, ref)
	if st, ok := m.activeTab().(*structureTab); ok {
		st.target(f)
	}
	return cmd
}

// modifyTableAt is modifyTable with the cursor on f, staging its drop when
// f.drop. modifyTable keeps its read-only check.
func (m *Model) modifyTableAt(conn string, ref db.TableRef, f structFocus) tea.Cmd {
	cmd := m.modifyTable(conn, ref)
	st, ok := m.activeTab().(*structureTab)
	if ok && st.conn == conn && st.ref == ref && (st.ed != nil || st.editLoading) {
		st.target(f)
	}
	return cmd
}

// editNode is e in the tree: modify a table (at a column or index), or
// edit a connection.
func (m *Model) editNode() tea.Cmd {
	n := m.tree.Selected()
	if n == nil {
		return nil
	}
	tbl := n.Table()
	switch {
	case n.Kind == tree.KindForeignKey:
		m.setStatus(errFKNotModifiable.Error(), true)
	case tbl != nil && tbl.Kind == tree.KindView:
		m.setStatus(errViewNotModifiable.Error(), true)
	case tbl != nil:
		if f, ok := leafFocus(n); ok {
			return m.modifyTableAt(tbl.Conn, tbl.TableRef(), f)
		}
		return m.modifyTable(tbl.Conn, tbl.TableRef())
	case m.canChange(n.Conn):
		if cfg, ok := m.backend.Connection(n.Conn); ok {
			m.overlay = overlay.NewEditConnForm(m.keys, cfg)
		}
	}
	return nil
}

// deleteNode is dd in the tree: stage a column's or index's drop in Modify
// Table, drop a table or view, or delete a connection.
func (m *Model) deleteNode() tea.Cmd {
	n := m.tree.Selected()
	if n == nil {
		return nil
	}
	if f, ok := leafFocus(n); ok {
		tbl := n.Table()
		if tbl.Kind == tree.KindView {
			m.setStatus(errViewNotModifiable.Error(), true)
			return nil
		}
		f.drop = true
		return m.modifyTableAt(tbl.Conn, tbl.TableRef(), f)
	}
	switch {
	case n.Kind == tree.KindForeignKey:
		m.setStatus(errFKNotModifiable.Error(), true)
	case n.Kind == tree.KindGroup:
		m.setStatus("select a column or index to drop it", true)
	case n.IsTable():
		return m.startTableOp(tableOp{kind: opDrop, conn: n.Conn, ref: n.TableRef(), view: n.Kind == tree.KindView})
	case n.Kind != tree.KindConn:
		m.setStatus("only connections, tables and views can be deleted", true)
	case m.canChange(n.Conn):
		m.overlay = overlay.NewConfirm("Delete connection", []string{
			"Delete " + n.Conn + " from connections.toml and its saved password?",
			"Its open tabs are closed. Type the connection name to confirm.",
		}, n.Conn, deleteConnMsg{name: n.Conn})
	}
	return nil
}
```

Add `overlay` to the imports. In `openNode`, add these two cases to its `switch n.Kind` (after the `tree.KindGroup` case):

```go
	case tree.KindColumn, tree.KindKey, tree.KindIndex:
		f, _ := leafFocus(n)
		tbl := n.Table()
		return m.openStructureAt(tbl.Conn, tbl.TableRef(), f)
	case tree.KindForeignKey:
		return m.reveal(n.Conn, tablePath(n.Ref.Namespace, n.Ref.Name))
```

In `internal/ui/app.go` `treeAction`, replace the whole `case keymap.EditNode:` body with `return m.editNode()` and the whole `case keymap.DeleteNode:` body with `return m.deleteNode()`. Remove imports `app.go` no longer uses (the compiler says which).

- [ ] **Step 7: Run the tests**

Run: `go test -race ./internal/ui/... `
Expected: PASS. `TestModifyRefusedOnReadOnlyAndViews`, `TestDropTableNeedsItsName` and the other existing `e` / `dd` tests must still pass unchanged.

- [ ] **Step 8: Lint and commit**

Run: `make lint && make test`

```bash
git add internal/ui
git commit -m "feat(ui): 树中的列、键、索引可跳到结构页、修改或暂存删除"
```

---

### Task 6: Leaves everywhere else — yank, context table, menu, reload after Modify Table

**Files:**
- Modify: `internal/ui/copy.go`, `internal/ui/ddl.go` (`contextTable`), `internal/ui/app.go` (`RenameTable` case), `internal/ui/nodemenu.go`, `internal/ui/modify.go` (`modifyApplied`), `internal/ui/treekeys.go` (`reloadTreeTable`)
- Test: `internal/ui/treekeys_test.go`, `internal/ui/copy_test.go`, `internal/ui/nodemenu_test.go`

**Interfaces:**
- Consumes: `tree.Model.FindTable`, `tree.Model.Forget` (Task 1), `leafFocus` (Task 5)
- Produces: `func (m *Model) reloadTreeTable(conn string, ref db.TableRef) tea.Cmd`

- [ ] **Step 1: Write the failing tests**

In `internal/ui/copy_test.go`, the table-driven tree-yank test: change its first line `m, _ := newTestModel(t)` to `m, b := newTestModel(t)` followed by `b.conn.setSchemas(usersSchema())`, and add these cases to `cases`:

```go
		{"local", []string{"app", "users", "Columns", "name"}, []string{"y", "y"}, "name"},
		{"local", []string{"app", "users", "Columns", "name"}, []string{"y", "q"}, "users.name"},
		{"local", []string{"app", "users", "Columns"}, []string{"y", "y"}, "users"},
		{"local", []string{"app", "users", "Columns"}, []string{"y", "q"}, "app.users"},
```

Append to `internal/ui/treekeys_test.go`:

```go
func TestStructureKeysWorkOnALeaf(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Columns", "name")
	press(t, m, "space", "S")
	if st := structTab(t, m); st.ref.Name != "users" {
		t.Fatalf("<space>S on a column opens its table, got %s", st.ref.Name)
	}
}

func TestModifyTableReloadsTheTreeTable(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	renameNameColumn(t, m, b, "local")
	s := usersSchema()
	s.Columns[1].Name = "name_x"
	b.conn.setSchemas(s)
	press(t, m, "space", "w", "y")
	users := m.tree.FindTable("local", db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"})
	if got := users.Children[0].Children[1].Name; got != "name_x" {
		t.Fatalf("tree column = %s, want name_x after the submit", got)
	}
}
```

`renameNameColumn` calls `modifyUsers`, which re-selects `users` in the tree and presses `e` — on a table that still means Modify Table.

Append to `internal/ui/nodemenu_test.go`:

```go
func TestActionMenuOnColumn(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Columns", "name")
	press(t, m, "a")
	s := screen(m)
	for _, want := range []string{"Show in structure", "Modify column", "Drop column", "Copy name"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "Drop table") {
		t.Fatalf("a column's menu must not offer to drop the table:\n%s", s)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/ui/ -run 'Yank|Leaf|ReloadsTheTree|MenuOnColumn'`
Expected: FAIL (`yq` gives `app.name`, `<space>S` says "select a table first", tree column stays `name`, menu has no entries).

- [ ] **Step 3: Implement**

`internal/ui/copy.go`:

```go
func (m *Model) copyNodeName(n *tree.Node, qualified bool) tea.Cmd {
	if n.Kind == tree.KindGroup {
		n = n.Table() // a group has no name of its own worth copying
	}
	…unchanged…
}
```

and in `qualifiedName`, before `default:`:

```go
	case tree.KindColumn, tree.KindKey, tree.KindForeignKey, tree.KindIndex:
		parts = []string{n.Table().Name, n.Name}
```

`internal/ui/ddl.go` `contextTable`, the tree branch:

```go
	if m.focus == focusTree {
		if n := m.tree.Selected(); n != nil {
			if tbl := n.Table(); tbl != nil {
				return tbl.Conn, tbl.TableRef(), true
			}
		}
	}
```

(Keep the `else if t := m.activeTab()` branch; restructure as `if … { … } else if … { … }` exactly as before, only the inner test changes.)

`internal/ui/app.go`, `case keymap.RenameTable:` — use the table of the selection:

```go
	case keymap.RenameTable:
		if n := t.Selected(); n != nil && n.Table() != nil {
			tbl := n.Table()
			m.askRename(tableOp{kind: opRename, conn: tbl.Conn, ref: tbl.TableRef(), view: tbl.Kind == tree.KindView})
		} else {
			m.setStatus("select a table or view to rename", true)
		}
```

`internal/ui/treekeys.go`:

```go
// reloadTreeTable brings the columns, keys and indexes the tree shows
// under ref up to date: reloaded when open, forgotten when closed.
func (m *Model) reloadTreeTable(conn string, ref db.TableRef) tea.Cmd {
	n := m.tree.FindTable(conn, ref)
	if n == nil || !n.Loaded || n.Loading {
		return nil
	}
	if !n.Expanded {
		m.tree.Forget(n)
		return nil
	}
	m.tree.StartLoad(n)
	return loadChildren(m.backend, n)
}
```

`internal/ui/modify.go` `modifyApplied`, in the `msg.err == nil` branch add `m.reloadTreeTable(msg.conn, msg.ref)` to the `cmds` list.

`internal/ui/nodemenu.go` `nodeMenu`, new cases before the closing of the `switch n.Kind`:

```go
	case tree.KindGroup:
		entries = []entry{{keymap.Open, "Expand / collapse"}, {keymap.Refresh, "Refresh"}}
		if n.Table().Kind == tree.KindTable {
			entries = append(entries, entry{keymap.EditNode, "Modify table"})
		}
	case tree.KindColumn, tree.KindKey, tree.KindIndex:
		obj := map[tree.Kind]string{tree.KindColumn: "column", tree.KindKey: "key", tree.KindIndex: "index"}[n.Kind]
		entries = []entry{{keymap.Open, "Show in structure"}}
		if n.Table().Kind == tree.KindTable {
			entries = append(entries, entry{keymap.EditNode, "Modify " + obj}, entry{keymap.DeleteNode, "Drop " + obj})
		}
		entries = append(entries, entry{keymap.Refresh, "Refresh"})
	case tree.KindForeignKey:
		entries = []entry{{keymap.Open, "Go to " + n.Ref.Name}, {keymap.Refresh, "Refresh"}}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/ui/...`
Expected: PASS.

- [ ] **Step 5: Lint and commit**

Run: `make lint && make test`

```bash
git add internal/ui
git commit -m "feat(ui): 复制、结构键、操作菜单支持树中的列与索引，修改表后刷新树"
```

---

### Task 7: Docs

**Files:**
- Modify: `README.md`, `README.zh-CN.md`, `CHANGELOG.md`, `docs/superpowers/specs/2026-10-13-datavim-design.md`, `docs/superpowers/specs/2026-10-16-tree-table-children-design.md`

- [ ] **Step 1: README.md** — in the key table replace

```
| `l` / `Enter` | Expand node / open table (retry a disconnected connection) |
```

with

```
| `l` / `→` | Expand a node; a table or view shows its Columns, Keys, Foreign Keys and Indexes (retry a disconnected connection) |
| `Enter` | Open a table's data; on a column, key or index open the structure at it, on a foreign key go to the referenced table |
| `zo` `zc` `za` | Expand / collapse (on a column, its group) / toggle |
| `zO` `zC` | Expand everything loaded / collapse everything under the cursor |
| `zR` `zM` `zx` | Expand everything loaded / collapse everything / collapse all but the path to the cursor |
```

and change the row describing tree `e` / `dd` so it reads: `Tree: edit the selected connection (saved to connections.toml), or modify the selected table — on a column or index, with the cursor on it` and `Tree: delete the selected connection, or drop the selected table or view (type its name to confirm); on a column or index, stage its drop in Modify Table`. In the feature list, after the "Browse databases" bullet's first line, add: `Expand a table in the tree to see its columns, keys, foreign keys and indexes.`

- [ ] **Step 2: README.zh-CN.md** — the same rows in Chinese:

```
| `l` / `→` | 展开节点；表或视图展开为 Columns、Keys、Foreign Keys、Indexes（对断开的连接重试） |
| `Enter` | 打开表数据；在列、键、索引上打开结构页并定位到它，在外键上跳到被引用的表 |
| `zo` `zc` `za` | 展开 / 折叠（在列上折叠所在分组）/ 切换 |
| `zO` `zC` | 展开光标下所有已加载的节点 / 折叠光标下的全部节点 |
| `zR` `zM` `zx` | 展开所有已加载的节点 / 全部折叠 / 只保留到光标的路径 |
```

plus the matching `e` / `dd` wording and the feature-list sentence (`在树中展开表，查看列、键、外键和索引。`).

- [ ] **Step 3: CHANGELOG.md** — under `[Unreleased]`:

`### Added`:

```
- Tables and views expand in the tree into Columns, Keys, Foreign Keys and Indexes, each
  with its type or columns. `Enter` on a column, key or index opens the structure at it, `e`
  modifies the table there and `dd` stages the drop in Modify Table (still submitted with
  `<Space>w`); `Enter` on a foreign key goes to the referenced table.
- Vim fold keys in the tree: `zo` `zc` `za`, `zO` / `zC` for the subtree under the cursor,
  `zR` / `zM` for the whole tree and `zx` to keep only the path to the cursor. `zO` and `zR`
  open what is loaded already and never query the server.
```

`### Changed` (first bullet):

```
- In the tree, `l` and `→` on a table or view expand it; `Enter` opens its data. Rebind
  `tree.open` in `keymap.toml` to change it.
```

- [ ] **Step 4: Main spec** — `docs/superpowers/specs/2026-10-13-datavim-design.md`:
  - §4.1: `树面板（连接 → 库/schema → 表/视图）` → `树面板（连接 → 库/schema → 表/视图 → 列、键、外键、索引）`, and `<Space>b` → `<Space>e`.
  - §5.11: append `树中展开表也能查看列、键、外键和索引，在其上按 Enter 打开结构页并定位（见 2026-10-16-tree-table-children-design.md）。`
  - §6.2: add a line for `l` / `Enter` / the z keys in the same style as the neighbours.
  - §6.3 table: the `<Space>b` row → `<Space>e`; the recent-tabs row `<Space>e` → `<Space>f`.

  In the tree-children spec, set `状态：已实现`.

- [ ] **Step 5: Verify and commit**

Run: `grep -n 'Space>b' README.md README.zh-CN.md docs/superpowers/specs/2026-10-13-datavim-design.md` → no output. `make test && make lint` → pass.

```bash
git add README.md README.zh-CN.md CHANGELOG.md docs/superpowers/specs
git commit -m "docs: 树展开表结构与 z 折叠键"
```
