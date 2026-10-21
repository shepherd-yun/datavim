package ui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
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

// resultColumnPickedMsg is a column picked in the columns popup of a
// console's result: col is its index in the result, -1 for none.
type resultColumnPickedMsg struct {
	tabID     int
	col       int
	structure bool
}

// openResultColumns is <space>s in a console: the columns of the result on
// screen, described by the catalog where the result comes from one table.
func (m *Model) openResultColumns(c *consoleTab) tea.Cmd {
	rs := c.grid.Result()
	if rs == nil || len(rs.Columns) == 0 {
		m.setStatus("run a query first", true)
		return nil
	}
	cols, known := c.resultColumns(rs)
	title := "result"
	if c.res.ref.Name != "" {
		title = c.res.ref.Name
	}
	current := -1
	if c.onResults {
		_, current = c.grid.Position()
	}
	id := c.id
	m.overlay = overlay.NewResultColumns(m.keys, title, cols, known, current, func(i int, structure bool) tea.Msg {
		return resultColumnPickedMsg{tabID: id, col: i, structure: structure}
	})
	return nil
}

// resultColumnPicked puts the console's results cursor on the column, or
// opens the structure of the table behind the result.
func (m *Model) resultColumnPicked(msg resultColumnPickedMsg) tea.Cmd {
	c, ok := m.tabByID(msg.tabID).(*consoleTab)
	if !ok {
		return nil
	}
	if msg.structure {
		if c.res.ref.Name == "" {
			m.setStatus("the result does not come from one table: no structure to open", true)
			return nil
		}
		return m.openStructure(c.conn, c.res.ref)
	}
	if msg.col >= 0 && c.grid.SelectColumnAt(msg.col) {
		c.onResults = true
	}
	return nil
}

// resultColumns describes the columns of rs: a column of the table behind
// the result as the catalog does (known), any other by its result type.
func (t *consoleTab) resultColumns(rs *db.ResultSet) ([]db.Column, []bool) {
	var table []db.Column
	if tm, ok := t.metas[t.res.ref]; ok && t.res.ref.Name != "" {
		table = tm.cols
	}
	cols := make([]db.Column, len(rs.Columns))
	known := make([]bool, len(rs.Columns))
	for i, rc := range rs.Columns {
		name := rc.Name
		if i < len(t.res.mapping) {
			name = t.res.mapping[i]
		}
		cols[i] = db.Column{Name: rc.Name, Type: strings.ToLower(rc.DatabaseType)}
		for _, tc := range table {
			if strings.EqualFold(tc.Name, name) {
				tc.Name = rc.Name
				cols[i], known[i] = tc, true
				break
			}
		}
	}
	return cols, known
}
