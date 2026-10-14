package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

type columnsLoadedMsg struct {
	conn string
	ref  db.TableRef
	cols []db.Column
	err  error
}

func (m columnsLoadedMsg) failure() (string, error) { return m.conn, m.err }

type columnPickedMsg struct {
	conn      string
	ref       db.TableRef
	name      string
	structure bool
}

// openColumns shows the columns popup of a table. The active data tab of
// that table already knows its columns; any other table's are read first.
func (m *Model) openColumns(conn string, ref db.TableRef) tea.Cmd {
	if t, ok := m.activeTab().(*dataTab); ok && t.Key() == dataTabKey(conn, ref) && len(t.cols) > 0 {
		m.showColumns(conn, ref, t.cols)
		return nil
	}
	b := m.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := columnsLoadedMsg{conn: conn, ref: ref}
		msg.err = withConn(ctx, b, conn, ref.Database, func(c db.Conn) error {
			var err error
			msg.cols, err = c.Meta().Columns(ctx, ref)
			return err
		})
		return msg
	}
}

func (m *Model) columnsLoaded(msg columnsLoadedMsg) {
	switch {
	case msg.err != nil:
		m.setStatus(msg.ref.Name+": "+msg.err.Error(), true)
	case m.overlay == nil: // something else opened while the columns were read
		m.showColumns(msg.conn, msg.ref, msg.cols)
	}
}

// showColumns opens the popup with the cursor on the data tab's column.
func (m *Model) showColumns(conn string, ref db.TableRef, cols []db.Column) {
	var current string
	if t, ok := m.activeTab().(*dataTab); ok && t.Key() == dataTabKey(conn, ref) {
		if c, ok := t.grid.Column(); ok {
			current = c.Name
		}
	}
	m.overlay = overlay.NewColumns(m.keys, ref.Name, cols, current, func(name string, structure bool) tea.Msg {
		return columnPickedMsg{conn: conn, ref: ref, name: name, structure: structure}
	})
}

// columnPicked puts the cursor of the table's data tab (opened if need be)
// on the column, or opens the full structure.
func (m *Model) columnPicked(msg columnPickedMsg) tea.Cmd {
	if msg.structure {
		return m.openStructure(msg.conn, msg.ref)
	}
	cmd := m.openData(msg.conn, msg.ref)
	if t, ok := m.activeTab().(*dataTab); ok {
		t.focusColumn(msg.name)
	}
	return cmd
}
