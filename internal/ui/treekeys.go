package ui

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/structure"
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
		if n.Kind == tree.KindRedisDB {
			return m.startScan(n)
		}
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
	case tree.KindGroup, tree.KindKeyFolder:
		if n.Expanded {
			n.Expanded = false
			return nil
		}
	case tree.KindRedisKey:
		k, _ := redisKeyOf(n)
		return m.openKey(k.conn, k.db, n.Key)
	case tree.KindMore:
		return m.loadMore(n)
	case tree.KindColumn, tree.KindKey, tree.KindIndex:
		f, _ := leafFocus(n)
		tbl := n.Table()
		return m.openStructureAt(tbl.Conn, tbl.TableRef(), f)
	case tree.KindForeignKey:
		return m.reveal(n.Conn, tablePath(n.Ref.Namespace, n.Ref.Name))
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
	if n == nil {
		return nil
	}
	if _, ok := redisKeyOf(n); ok {
		return m.refreshKeys(n)
	}
	if n.Loading {
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
	if n.Kind == tree.KindRedisKey {
		k, _ := redisKeyOf(n)
		return m.askDeleteKey(k.conn, k.db, n.Key, n.KeyType)
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
