package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
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
