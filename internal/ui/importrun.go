package ui

import (
	"errors"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/importer"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

// importFileMsg is the file typed at the prompt that starts an import.
type importFileMsg struct {
	conn   string
	ref    db.TableRef // the table; for a new table only the namespace
	create bool
	path   string
}

// importBlocked says why an import on conn cannot start now ("" when it
// can): read-only, a console in a transaction on conn, staged changes of the
// table.
func (m *Model) importBlocked(conn string, ref db.TableRef, create bool) string {
	if !create {
		return m.tableBlocked(conn, ref, nil)
	}
	if cfg, _ := m.backend.Connection(conn); cfg.IsReadOnly() {
		return readOnlyHint
	}
	for _, t := range m.tabs {
		if c, ok := t.(*consoleTab); ok && c.conn == conn && c.openTx() {
			return "a console on " + conn + " has an open transaction · :commit or :rollback first"
		}
	}
	return ""
}

// importFromNode asks for the file to import into the selected table, or
// into a new table of the selected database or schema.
func (m *Model) importFromNode() tea.Cmd {
	n := m.tree.Selected()
	if n == nil {
		return nil
	}
	if p := schemaToolsProblem(m.backend, n.Conn); p != "" {
		m.setStatus(p, true)
		return nil
	}
	cfg, _ := m.backend.Connection(n.Conn)
	var ref db.TableRef
	create := false
	switch {
	case n.Kind == tree.KindTable:
		ref = n.TableRef()
	case n.Kind == tree.KindSchema, n.Kind == tree.KindDatabase && !sqlx.DialectOf(cfg.Driver).Features().Schemas:
		ref, create = db.TableRef{Namespace: n.NS}, true
	case n.Kind == tree.KindDatabase:
		m.setStatus("PostgreSQL creates tables in a schema · select a schema", true)
		return nil
	default:
		m.setStatus("select a table, database or schema to import into", true)
		return nil
	}
	if why := m.importBlocked(n.Conn, ref, create); why != "" {
		m.setStatus(why, true)
		return nil
	}
	// A namespace-only ref joins as "app.", hence the TrimSuffix.
	where := strings.TrimSuffix(strings.Join(tableParts(sqlx.DialectOf(cfg.Driver), ref), "."), ".")
	title := "Import into " + where
	if create {
		title = "Create a table in " + where
	}
	conn := n.Conn
	start := m.settings.ExportDirectory(m.home) + string(filepath.Separator)
	m.overlay = overlay.NewPrompt(title, "File (CSV, TSV or XLSX):", start, func(s string) tea.Msg {
		return importFileMsg{conn: conn, ref: ref, create: create, path: s}
	}, overlay.WithValidate(func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New("enter a file")
		}
		return nil
	}))
	return nil
}

// openImport opens the Import tab for the file typed at the prompt.
func (m *Model) openImport(msg importFileMsg) tea.Cmd {
	path := expandHome(strings.TrimSpace(msg.path), m.home)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	ref := msg.ref
	if msg.create {
		ref.Name = importer.Ident(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), "imported")
	}
	cfg, _ := m.backend.Connection(msg.conn)
	return m.showTab(importTabKey(msg.conn, msg.ref, path), func(id int) tab {
		t := newImportTab(id, m.backend, m.home, msg.conn, cfg.Driver, ref, msg.create, path)
		t.keys = m.keys
		return t
	})
}

// importConfirmed starts the confirmed import unless something changed
// meanwhile (read-only, a console transaction, staged changes).
func (m *Model) importConfirmed(msg importConfirmedMsg) tea.Cmd {
	t, ok := m.tabByID(msg.tabID).(*importTab)
	if !ok {
		return nil
	}
	if why := m.importBlocked(t.conn, t.ref, t.create); why != "" {
		t.job = nil
		t.setStatus(why, true)
		m.setStatus(why, true)
		return nil
	}
	return t.start()
}

// importDone updates the tab, then the tree and the catalog: a new table
// shows up, and a failed one is looked up again in case it was left over.
func (m *Model) importDone(msg importDoneMsg) tea.Cmd {
	m.tree.SetBroken(msg.conn, db.IsConnLost(msg.err))
	var cmd tea.Cmd
	if t := m.tabByID(msg.tabID); t != nil {
		cmd = t.Update(msg)
	}
	if msg.err != nil && !msg.create {
		return cmd
	}
	cmds := []tea.Cmd{cmd, m.metaStale(metaStaleMsg{conn: msg.conn, database: msg.ref.Database})}
	if msg.create {
		cmds = append(cmds, m.reloadTables(msg.conn, msg.ref.Namespace, ""))
	}
	return tea.Batch(cmds...)
}
