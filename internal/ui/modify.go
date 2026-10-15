package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// stager is a tab that stages changes until they are submitted: table
// data, and a table being modified.
type stager interface {
	tab
	changes() int
	unsavedHint(submit, discard string) string
}

// tableBlocked says why the table ref on conn cannot be changed now ("" when
// it can): a read-only connection, a console on conn in a transaction, or a
// tab other than self with staged changes of the table.
func (m *Model) tableBlocked(conn string, ref db.TableRef, self tab) string {
	if cfg, _ := m.backend.Connection(conn); cfg.IsReadOnly() {
		return readOnlyHint
	}
	for _, t := range m.tabs {
		if t == self {
			continue
		}
		switch t := t.(type) {
		case *consoleTab:
			if t.conn == conn && t.openTx() {
				return "a console on " + conn + " has an open transaction · :commit or :rollback first"
			}
		case stager:
			if c, r := t.Target(); c == conn && r == ref && t.changes() > 0 {
				return joinHints(t.Title()+" has "+plural(t.changes(), "unsaved change"), t.unsavedHint("submits", "discards"))
			}
		}
	}
	return ""
}

// modifyTable opens the structure of ref and starts modifying it.
func (m *Model) modifyTable(conn string, ref db.TableRef) tea.Cmd {
	if cfg, _ := m.backend.Connection(conn); cfg.IsReadOnly() {
		m.setStatus(readOnlyHint, true)
		return nil
	}
	open := m.openStructure(conn, ref)
	st, ok := m.activeTab().(*structureTab)
	if !ok {
		return open
	}
	return tea.Batch(open, st.startEdit())
}
