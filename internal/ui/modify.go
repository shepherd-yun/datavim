package ui

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/runner"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

// stager is a tab that stages changes until they are submitted: table
// data, and a table being modified.
type stager interface {
	tab
	changes() int
	unsavedHint(submit, discard string) string
	submit() tea.Cmd
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

// schemaToolsProblem says why conn's engine has no Modify Table, compare
// or import yet ("" when it has them).
func schemaToolsProblem(b Backend, conn string) string {
	cfg, _ := b.Connection(conn)
	if d := sqlx.DialectOf(cfg.Driver); !d.Features().SchemaTools {
		return "not supported for " + d.Driver() + " yet"
	}
	return ""
}

// modifyTable opens the structure of ref and starts modifying it.
func (m *Model) modifyTable(conn string, ref db.TableRef) tea.Cmd {
	if p := schemaToolsProblem(m.backend, conn); p != "" {
		m.setStatus(p, true)
		return nil
	}
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

// modifySubmit checks a Modify Table submit and previews it: prod wants
// the table's name typed, other connections y. The destructive and lossy
// changes come first, in red.
func (m *Model) modifySubmit(msg modifySubmitMsg) {
	self := m.tabByID(msg.tabID)
	if self == nil || m.overlay != nil {
		return // closed, or the user opened something else meanwhile
	}
	if why := m.tableBlocked(msg.conn, msg.ref, self); why != "" {
		m.setStatus(why, true)
		return
	}
	cfg, _ := m.backend.Connection(msg.conn)
	policy := runner.Policy{ReadOnly: cfg.IsReadOnly(), Prod: cfg.Env == config.EnvProd}
	batch := make([]sqlx.Statement, len(msg.stmts))
	for i, s := range msg.stmts {
		batch[i] = sqlx.Statement{Text: s}
	}
	if chk := runner.Guard(batch, sqlx.DialectOf(cfg.Driver), policy); chk.Verdict == runner.Reject {
		m.setStatus(chk.Reason, true)
		return
	}
	title := "Modify table " + msg.ref.Name + " · " + plural(msg.changes, "change")
	if policy.Prod {
		title = "PROD · " + title
	}
	lines := slices.Clone(msg.warnings)
	danger := make([]int, len(lines))
	for i := range danger {
		danger[i] = i
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	for _, s := range msg.stmts {
		lines = append(lines, s+";")
	}
	ok := modifyConfirmedMsg{tabID: msg.tabID, gen: msg.gen}
	if !policy.Prod {
		m.overlay = overlay.NewPreview(title, lines, ok, overlay.WithDanger(danger...))
		return
	}
	lines = append(lines, "", "Type "+msg.ref.Name+" to confirm.")
	m.overlay = overlay.NewConfirm(title, lines, msg.ref.Name, ok, overlay.WithDanger(danger...))
}

// modifyApplied brings the catalog and the table's data tabs up to date
// after a successful submit, then lets the structure tab finish.
func (m *Model) modifyApplied(msg modifyAppliedMsg) tea.Cmd {
	var cmds []tea.Cmd
	if msg.err == nil {
		cmds = append(cmds,
			m.metaStale(metaStaleMsg{conn: msg.conn, database: msg.ref.Database}),
			m.refreshTabsOfTable(msg.conn, msg.ref),
			m.reloadTreeTable(msg.conn, msg.ref))
	}
	if t := m.tabByID(msg.tabID); t != nil {
		cmds = append(cmds, t.Update(msg))
	}
	return tea.Batch(cmds...)
}
