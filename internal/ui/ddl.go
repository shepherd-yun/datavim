package ui

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

type ddlLoadedMsg struct {
	conn string
	ref  db.TableRef
	ddl  string
	err  error
}

func (m ddlLoadedMsg) failure() (string, error) { return m.conn, m.err }

// contextTable is the table under the tree cursor, or behind the active
// tab when the tabs have focus.
func (m *Model) contextTable() (string, db.TableRef, bool) {
	if m.focus == focusTree {
		if n := m.tree.Selected(); n != nil {
			if tbl := n.Table(); tbl != nil {
				return tbl.Conn, tbl.TableRef(), true
			}
		}
	} else if t := m.activeTab(); t != nil {
		if conn, ref := t.Target(); ref.Name != "" {
			return conn, ref, true
		}
	}
	m.setStatus("select a table first", true)
	return "", db.TableRef{}, false
}

// openFullStructure is <space>S: the structure tab, without the columns popup.
func (m *Model) openFullStructure() tea.Cmd {
	conn, ref, ok := m.contextTable()
	if !ok {
		return nil
	}
	return m.openStructure(conn, ref)
}

// copyDDL reads the context table's DDL afresh and puts it on the clipboard.
func (m *Model) copyDDL() tea.Cmd {
	conn, ref, ok := m.contextTable()
	if !ok {
		return nil
	}
	b := m.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := ddlLoadedMsg{conn: conn, ref: ref}
		msg.err = withConn(ctx, b, conn, ref.Database, func(c db.Conn) error {
			var err error
			msg.ddl, err = c.Meta().DDL(ctx, ref)
			return err
		})
		return msg
	}
}

func (m *Model) ddlLoaded(msg ddlLoadedMsg) tea.Cmd {
	switch {
	case errors.Is(msg.err, db.ErrNotFound):
		m.setStatus(fmt.Sprintf("table %s not found", msg.ref.Name), true)
	case msg.err != nil:
		m.setStatus(msg.ref.Name+": "+msg.err.Error(), true)
	default:
		m.setStatus("copied the DDL of "+msg.ref.Name, false)
		return m.copy(msg.ddl)
	}
	return nil
}

// copyText is y in a detail popup.
func (m *Model) copyText(msg overlay.CopyMsg) tea.Cmd {
	m.setStatus("copied "+msg.What, false)
	return m.copy(msg.Text)
}
