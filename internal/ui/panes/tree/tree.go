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
)

// Node is one tree entry. Children are loaded lazily by the caller.
type Node struct {
	Kind     Kind
	Name     string
	Conn     string
	NS       db.Namespace
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
func (n *Node) Expandable() bool      { return n.Kind <= KindSchema }
func (n *Node) IsTable() bool         { return n.Kind == KindTable || n.Kind == KindView }
func (n *Node) TableRef() db.TableRef { return db.TableRef{Namespace: n.NS, Name: n.Name} }

type row struct {
	node  *Node
	depth int
}

// Model holds the tree and the cursor.
type Model struct {
	roots    []*Node
	selected *Node
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

// SetChildren records the result of a load.
func (m *Model) SetChildren(n *Node, children []*Node, err error) {
	n.Loading = false
	if err != nil {
		n.Err, n.Expanded, n.Loaded = err.Error(), false, false
		return
	}
	for _, c := range children {
		c.parent = n
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
		label := strings.Repeat("  ", rows[i].depth) + icon(n) + " " + textutil.Sanitize(n.Name)
		if i == cur {
			if focused {
				label = theme.Selected.Render(label)
			} else {
				label = theme.SelectedDim.Render(label)
			}
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

func icon(n *Node) string {
	switch {
	case n.Kind == KindTable:
		return "•"
	case n.Kind == KindView:
		return "◦"
	case n.Expanded:
		return "▾"
	default:
		return "▸"
	}
}
