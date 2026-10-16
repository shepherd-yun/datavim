// Package tree is the connection / database / schema / table navigator.
package tree

import (
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// Kind is the type of a tree node.
type Kind int

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
	KindRedisDB   // a redis db; NS.Database is its number
	KindKeyFolder // keys grouped by a prefix; Key is the raw prefix with its separator
	KindRedisKey  // Key is the raw key, KeyType its TYPE
	KindMore      // a "load more" / progress line at the end of a redis db
)

// Group tells the groups under a table apart.
type Group int

const (
	GroupColumns Group = iota
	GroupKeys
	GroupForeignKeys
	GroupIndexes
)

// Node is one tree entry. Children are loaded lazily by the caller.
type Node struct {
	Kind     Kind
	Name     string
	Conn     string
	Driver   string // a connection's driver, for its icon
	NS       db.Namespace
	Group    Group       // a KindGroup node's group
	Detail   string      // shown muted after the name: a column's type, an index's columns
	Ref      db.TableRef // a foreign key's referenced table
	Key      string      // redis: the raw key (KindRedisKey) or prefix (KindKeyFolder); Name is its escaped form
	KeyType  string      // a KindRedisKey's TYPE
	Prod     bool
	Expanded bool
	Loaded   bool
	Loading  bool
	Err      string
	Broken   bool // the connection was lost and reconnecting failed
	Children []*Node

	parent *Node
}

func (n *Node) Parent() *Node         { return n.parent }
func (n *Node) Expandable() bool {
	return n.Kind <= KindView || n.Kind == KindGroup || n.Kind == KindRedisDB || n.Kind == KindKeyFolder
}
func (n *Node) IsTable() bool         { return n.Kind == KindTable || n.Kind == KindView }
func (n *Node) TableRef() db.TableRef { return db.TableRef{Namespace: n.NS, Name: n.Name} }

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

type row struct {
	node  *Node
	depth int
}

// Model holds the tree and the cursor.
// MarkKey identifies a node across reloads, for the compare mark.
type MarkKey struct {
	Conn string
	Kind Kind
	NS   db.Namespace
	Name string
}

func (n *Node) MarkKey() MarkKey { return MarkKey{Conn: n.Conn, Kind: n.Kind, NS: n.NS, Name: n.Name} }

// IconStyle picks the glyphs drawn before the names.
type IconStyle int

const (
	IconsPlain IconStyle = iota // arrows and dots, any font
	IconsNerd                   // Nerd Font glyphs: driver logos, databases, tables, folders
)

type Model struct {
	icons    IconStyle
	roots    []*Node
	selected *Node
	mark     *MarkKey // the compare source, drawn with ◆
	offset   int
	height   int
}

// New builds a tree from root nodes.
func New(roots []*Node) *Model {
	m := &Model{roots: roots}
	if len(roots) > 0 {
		m.selected = roots[0]
	}
	return m
}

// SetIcons picks the icon set.
func (m *Model) SetIcons(s IconStyle) { m.icons = s }

// Roots returns the connection nodes.
func (m *Model) Roots() []*Node { return m.roots }

// SetHeight sets the number of visible rows.
func (m *Model) SetHeight(h int) { m.height = max(h, 0) }

func (m *Model) visible() []row {
	var out []row
	var walk func(nodes []*Node, depth int)
	walk = func(nodes []*Node, depth int) {
		for _, n := range nodes {
			out = append(out, row{node: n, depth: depth})
			if n.Expanded {
				walk(n.Children, depth+1)
			}
		}
	}
	walk(m.roots, 0)
	return out
}

// cursor returns the index of the selected node, falling back to the nearest
// visible ancestor, and re-anchors the selection there.
func (m *Model) cursor(rows []row) int {
	for n := m.selected; n != nil; n = n.parent {
		for i, r := range rows {
			if r.node == n {
				m.selected = n
				return i
			}
		}
	}
	if len(rows) > 0 {
		m.selected = rows[0].node
	}
	return 0
}

// Selected returns the node under the cursor, or nil for an empty tree.
func (m *Model) Selected() *Node {
	rows := m.visible()
	if len(rows) == 0 {
		return nil
	}
	return rows[m.cursor(rows)].node
}

// Move moves the cursor by delta rows.
func (m *Model) Move(delta int) {
	rows := m.visible()
	if len(rows) == 0 {
		return
	}
	m.selected = rows[textutil.Clamp(m.cursor(rows)+delta, 0, len(rows)-1)].node
}

func (m *Model) Top()    { m.Move(-1 << 30) }
func (m *Model) Bottom() { m.Move(1 << 30) }

// HalfPage moves half the visible height in direction dir (+1 / -1).
func (m *Model) HalfPage(dir int) { m.Move(dir * max(m.height/2, 1)) }

// Expand opens the selected node. It returns true when the caller must load children.
func (m *Model) Expand() bool {
	n := m.Selected()
	if n == nil || !n.Expandable() || n.Loading {
		return false
	}
	if n.Loaded {
		n.Expanded = true
		return false
	}
	m.StartLoad(n)
	return true
}

// Collapse closes the selected node, or selects its parent if already closed.
func (m *Model) Collapse() {
	n := m.Selected()
	if n == nil {
		return
	}
	if n.Expanded {
		n.Expanded = false
		return
	}
	if n.parent != nil {
		m.selected = n.parent
	}
}

// StartLoad marks n as loading (used for first load, retry and refresh).
func (m *Model) StartLoad(n *Node) {
	n.Loading, n.Expanded, n.Err = true, true, ""
}

// CancelLoad undoes StartLoad without recording an error.
func (m *Model) CancelLoad(n *Node) {
	n.Loading = false
	n.Expanded = n.Loaded
}

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

// ResetConn forgets everything loaded under the named connection.
func (m *Model) ResetConn(conn string) {
	for _, r := range m.roots {
		if r.Conn == conn {
			r.Children, r.Loaded, r.Expanded, r.Loading, r.Err, r.Broken = nil, false, false, false, "", false
		}
	}
}

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

// Empty reports whether there are no connections.
func (m *Model) Empty() bool { return len(m.roots) == 0 }

// AddRoot appends a connection and selects it.
func (m *Model) AddRoot(n *Node) {
	m.roots = append(m.roots, n)
	m.selected = n
}

// ReplaceRoot swaps the connection conn for n (an edited connection),
// keeping its place; the cursor moves to n if it was anywhere under conn.
func (m *Model) ReplaceRoot(conn string, n *Node) {
	for i, r := range m.roots {
		if r.Conn == conn {
			if m.under(r) {
				m.selected = n
			}
			m.roots[i] = n
		}
	}
}

// RemoveRoot drops the connection conn; the cursor moves to the root that
// takes its place.
func (m *Model) RemoveRoot(conn string) {
	for i, r := range m.roots {
		if r.Conn != conn {
			continue
		}
		sel := m.under(r)
		m.roots = append(m.roots[:i], m.roots[i+1:]...)
		if sel {
			m.selected = nil
			if len(m.roots) > 0 {
				m.selected = m.roots[min(i, len(m.roots)-1)]
			}
		}
		return
	}
}

// under reports whether the selection is root or one of its descendants.
func (m *Model) under(root *Node) bool {
	for n := m.selected; n != nil; n = n.parent {
		if n == root {
			return true
		}
	}
	return false
}

// SetBroken marks the root of conn as disconnected (or clears the mark).
func (m *Model) SetBroken(conn string, broken bool) {
	for _, r := range m.roots {
		if r.Conn == conn {
			r.Broken = broken
		}
	}
}

// SetMark draws ◆ before the node k names; nil clears the mark.
func (m *Model) SetMark(k *MarkKey) { m.mark = k }

// Find selects the next visible node (wrapping; backwards when forward is
// false) whose name contains q, ignoring case.
func (m *Model) Find(q string, forward bool) bool {
	rows := m.visible()
	if q == "" || len(rows) == 0 {
		return false
	}
	q = strings.ToLower(q)
	step := 1
	if !forward {
		step = -1
	}
	cur := m.cursor(rows)
	for i := 1; i <= len(rows); i++ {
		k := ((cur+step*i)%len(rows) + len(rows)) % len(rows)
		if strings.Contains(strings.ToLower(rows[k].node.Name), q) {
			m.selected = rows[k].node
			return true
		}
	}
	return false
}

// View renders the visible window of the tree.
func (m *Model) View(width int, focused bool) string {
	rows := m.visible()
	if len(rows) == 0 || m.height == 0 || width <= 0 {
		return ""
	}
	cur := m.cursor(rows)
	if cur < m.offset {
		m.offset = cur
	}
	if cur >= m.offset+m.height {
		m.offset = cur - m.height + 1
	}
	m.offset = textutil.Clamp(m.offset, 0, max(len(rows)-m.height, 0))

	end := min(m.offset+m.height, len(rows))
	lines := make([]string, 0, end-m.offset)
	for i := m.offset; i < end; i++ {
		n := rows[i].node
		name := textutil.Sanitize(n.Name)
		if m.mark != nil && n.MarkKey() == *m.mark {
			name = "◆ " + name
		}
		ic := icon(n, m.icons)
		if m.icons == IconsNerd && n.Kind == KindConn && i != cur {
			g := nerdGlyph(n) // the selection style would end at an inner color
			ic = strings.Replace(ic, g, theme.Accent.Render(g), 1)
		}
		switch {
		case n.Kind == KindRedisKey && i != cur:
			ic = theme.KeyType(n.KeyType).Render(ic)
		case n.Kind == KindMore:
			ic, name = "", theme.Muted.Render(name)
		}
		label := strings.Repeat("  ", rows[i].depth) + ic + " " + name
		if n.Kind == KindMore {
			label = strings.Repeat("  ", rows[i].depth) + name
		}
		if i == cur {
			if focused {
				label = theme.Selected.Render(label)
			} else {
				label = theme.SelectedDim.Render(label)
			}
		}
		if n.Detail != "" {
			label += "  " + theme.Muted.Render(textutil.Sanitize(n.Detail))
		}
		if n.Prod {
			label += " " + theme.Prod.Render("[PROD]")
		}
		if n.Loading {
			label += " " + theme.Muted.Render("loading…")
		}
		if n.Broken {
			label += " " + theme.Error.Render("✗ disconnected · enter to retry")
		}
		if n.Err != "" {
			label += " " + theme.Error.Render("✗ "+textutil.Sanitize(n.Err))
		}
		lines = append(lines, textutil.Fit(label, width))
	}
	return strings.Join(lines, "\n")
}

func icon(n *Node, style IconStyle) string {
	arrow := "▸"
	if n.Expanded {
		arrow = "▾"
	}
	switch n.Kind {
	case KindRedisKey:
		return KeyBadge(n.KeyType)
	case KindMore:
		return ""
	}
	if style == IconsNerd {
		if !n.Expandable() {
			return nerdGlyph(n)
		}
		return arrow + " " + nerdGlyph(n)
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

// nerdGlyph is n's Nerd Font (v3) icon.
func nerdGlyph(n *Node) string {
	switch n.Kind {
	case KindConn:
		switch n.Driver {
		case "mysql":
			return "\ue704" // dev-mysql
		case "postgres":
			return "\ue76e" // dev-postgresql
		case "redis":
			return "\ue76d" // dev-redis
		}
		return "\U000f01bc" // md-database
	case KindDatabase, KindRedisDB:
		return "\U000f01bc" // md-database
	case KindSchema:
		return "\uf0e8" // fa-sitemap
	case KindTable:
		return "\U000f04eb" // md-table
	case KindView:
		return "\U000f1094" // md-table_eye
	case KindGroup, KindKeyFolder:
		if n.Expanded {
			return "\U000f0770" // md-folder_open
		}
		return "\U000f024b" // md-folder
	case KindColumn:
		return "\U000f0835" // md-table_column
	case KindKey:
		return "\U000f0306" // md-key
	case KindForeignKey:
		return "\U000f119f" // md-key_link
	case KindIndex:
		return "\U000f04bc" // md-sort_ascending
	}
	return ""
}

// KeyBadge is the one-letter tag of a redis key type; T is a set, X a
// stream and ? any other (module) type.
func KeyBadge(typ string) string {
	switch typ {
	case "string":
		return "S"
	case "hash":
		return "H"
	case "list":
		return "L"
	case "set":
		return "T"
	case "zset":
		return "Z"
	case "stream":
		return "X"
	}
	return "?"
}

// RevealResult says how far Reveal got.
type RevealResult int

const (
	Revealed  RevealResult = iota // the whole path is expanded and its end selected
	NeedsLoad                     // the returned node was marked loading: load its children, then call Reveal again
	Waiting                       // a node on the path is loading already: call Reveal again when it is done
	NotFound                      // the connection or a name on the path does not exist
)

// Namespace returns the loaded node whose children are the tables of ns on
// conn: a database node for MySQL, a schema node for PostgreSQL. It
// returns nil when no such node is loaded.
func (m *Model) Namespace(conn string, ns db.Namespace) *Node {
	var walk func(nodes []*Node) *Node
	walk = func(nodes []*Node) *Node {
		for _, n := range nodes {
			if n.Conn != conn {
				continue
			}
			if (n.Kind == KindDatabase || n.Kind == KindSchema) && n.NS == ns && n.Loaded {
				return n
			}
			if found := walk(n.Children); found != nil {
				return found
			}
		}
		return nil
	}
	return walk(m.roots)
}

// Reveal expands the tree of conn along path (database, then schema) and
// selects the deepest node it reaches.
func (m *Model) Reveal(conn string, path []string) (*Node, RevealResult) {
	var n *Node
	for _, r := range m.roots {
		if r.Conn == conn {
			n = r
		}
	}
	if n == nil {
		return nil, NotFound
	}
	for _, name := range path {
		m.selected = n
		switch {
		case n.Loading:
			return nil, Waiting
		case !n.Loaded:
			m.StartLoad(n)
			return n, NeedsLoad
		}
		n.Expanded = true
		var next *Node
		for _, c := range n.Children {
			if c.Name == name {
				next = c
				break
			}
		}
		if next == nil {
			return nil, NotFound
		}
		n = next
	}
	m.selected = n
	return nil, Revealed
}
