package ui

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/runner"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

// tableOpKind is a change made to a table from the tree.
type tableOpKind int

const (
	opDrop tableOpKind = iota
	opRename
	opTruncate
)

// tableOp is one drop, rename or truncate of a table or view.
type tableOp struct {
	kind    tableOpKind
	conn    string
	ref     db.TableRef
	view    bool
	newName string // opRename only
}

func (o tableOp) object() string {
	if o.view {
		return "view"
	}
	return "table"
}

func (o tableOp) title() string {
	verb := [...]string{opDrop: "Drop", opRename: "Rename", opTruncate: "Truncate"}[o.kind]
	return verb + " " + o.object() + " " + o.ref.Name
}

func (o tableOp) done() string {
	switch o.kind {
	case opRename:
		return "renamed " + o.object() + " " + o.ref.Name + " to " + o.newName
	case opTruncate:
		return "truncated table " + o.ref.Name
	}
	return "dropped " + o.object() + " " + o.ref.Name
}

func (o tableOp) warning() string {
	switch {
	case o.kind == opRename:
		return "SQL and code that use the old name stop working."
	case o.kind == opTruncate:
		return "Every row of the table is deleted. This cannot be undone."
	case o.view:
		return "The view is deleted."
	}
	return "The table and all its rows are deleted. This cannot be undone."
}

// renamed is the table after a rename.
func (o tableOp) renamed() db.TableRef {
	to := o.ref
	to.Name = o.newName
	return to
}

func (o tableOp) statement(w db.DDLWriter) string {
	switch o.kind {
	case opRename:
		return w.RenameTable(o.ref, o.newName, o.view)
	case opTruncate:
		return w.TruncateTable(o.ref)
	}
	return w.DropTable(o.ref, o.view)
}

type (
	// tableOpPreparedMsg carries op's statement, written by the
	// connection's dialect.
	tableOpPreparedMsg struct {
		op  tableOp
		sql string
		err error
	}
	// tableOpConfirmedMsg is the user's go-ahead for sql.
	tableOpConfirmedMsg struct {
		op  tableOp
		sql string
	}
	tableOpDoneMsg struct {
		op  tableOp
		err error
	}
)

func (m tableOpPreparedMsg) failure() (string, error) { return m.op.conn, m.err }

// tableOpBlocked says why op cannot run now ("" when it can).
func (m *Model) tableOpBlocked(op tableOp) string {
	if cfg, _ := m.backend.Connection(op.conn); cfg.IsReadOnly() {
		return "read-only connection · :set noreadonly allows changes for this session"
	}
	for _, t := range m.tabs {
		switch t := t.(type) {
		case *consoleTab:
			if t.conn == op.conn && t.openTx() {
				return "a console on " + op.conn + " has an open transaction · :commit or :rollback first"
			}
		case *dataTab:
			if t.conn == op.conn && t.ref == op.ref && t.changes() > 0 {
				return joinHints(t.Title()+" has "+plural(t.changes(), "unsaved change"), t.unsavedHint("submits", "discards"))
			}
		}
	}
	return ""
}

// startTableOp checks op, then has the connection's dialect write its SQL.
func (m *Model) startTableOp(op tableOp) tea.Cmd {
	if msg := m.tableOpBlocked(op); msg != "" {
		m.setStatus(msg, true)
		return nil
	}
	b := m.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := tableOpPreparedMsg{op: op}
		msg.err = withConn(ctx, b, op.conn, op.ref.Database, func(c db.Conn) error {
			msg.sql = op.statement(c.Dialect().DDL())
			return nil
		})
		return msg
	}
}

// tableOpPrepared asks before running: drop and truncate always want the
// table's name typed, a rename only on prod.
func (m *Model) tableOpPrepared(msg tableOpPreparedMsg) {
	op := msg.op
	if msg.err != nil {
		m.setStatus(op.object()+" "+op.ref.Name+": "+errorText(msg.err), true)
		return
	}
	if m.overlay != nil {
		return // the user opened something else meanwhile
	}
	cfg, _ := m.backend.Connection(op.conn)
	policy := runner.Policy{ReadOnly: cfg.IsReadOnly(), Prod: cfg.Env == config.EnvProd}
	stmts := []sqlx.Statement{{Text: msg.sql}}
	if chk := runner.Guard(stmts, sqlx.DialectOf(cfg.Driver), policy); chk.Verdict == runner.Reject {
		m.setStatus(chk.Reason, true)
		return
	}
	title := op.title()
	if policy.Prod {
		title = "PROD · " + title
	}
	ok := tableOpConfirmedMsg{op: op, sql: msg.sql}
	if op.kind == opRename && !policy.Prod {
		m.overlay = overlay.NewPreview(title, []string{msg.sql + ";"}, ok)
		return
	}
	lines := []string{msg.sql + ";", "", op.warning(), "Type " + op.ref.Name + " to confirm."}
	m.overlay = overlay.NewConfirm(title, lines, op.ref.Name, ok)
}

// runTableOp runs the confirmed statement on a pooled connection. It never
// retries: a dropped connection may or may not have run it.
func runTableOp(b Backend, st Store, op tableOp, sql string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		c, err := b.Conn(ctx, op.conn, op.ref.Database)
		if err == nil {
			start := time.Now()
			_, err = c.Exec(ctx, sql)
			entry := history.Entry{Time: start, Conn: op.conn, Database: op.ref.Database, SQL: sql + ";", Duration: time.Since(start)}
			if err != nil {
				entry.Err = errorText(err)
			}
			if herr := st.AddHistory(entry); herr != nil {
				slog.Warn("save history", "err", herr)
			}
		}
		if db.IsConnLost(err) {
			b.Reset(op.conn)
		}
		return tableOpDoneMsg{op: op, err: err}
	}
}

// tableOpDone brings the tree, the catalog and the open tabs up to date.
// It marks the connection itself rather than through connFailure, whose
// generic "connection lost" status would hide that the statement may have
// run.
func (m *Model) tableOpDone(msg tableOpDoneMsg) tea.Cmd {
	op := msg.op
	m.tree.SetBroken(op.conn, db.IsConnLost(msg.err))
	if msg.err != nil {
		m.setStatus(op.title()+": "+errorText(msg.err), true)
		return nil
	}
	m.setStatus(op.done(), false)
	cmds := []tea.Cmd{m.metaStale(metaStaleMsg{conn: op.conn, database: op.ref.Database})}
	switch op.kind {
	case opDrop:
		cmds = append(cmds, m.closeTabsOfTable(op.conn, op.ref), m.reloadTables(op.conn, op.ref.Namespace, ""))
	case opRename:
		cmds = append(cmds, m.retargetTabs(op.conn, op.ref, op.renamed()), m.reloadTables(op.conn, op.ref.Namespace, op.newName))
	case opTruncate:
		cmds = append(cmds, m.refreshTabsOfTable(op.conn, op.ref))
	}
	return tea.Batch(cmds...)
}

// closeTabsOfTable closes the data and structure tabs of ref.
func (m *Model) closeTabsOfTable(conn string, ref db.TableRef) tea.Cmd {
	var cmds []tea.Cmd
	for i := len(m.tabs) - 1; i >= 0; i-- {
		if c, r := m.tabs[i].Target(); c == conn && r == ref {
			cmds = append(cmds, m.closeTab(i))
		}
	}
	return tea.Batch(cmds...)
}

// reloadTables reloads the tree node listing the tables of ns and, when
// selectName is set, selects that table once it is loaded.
func (m *Model) reloadTables(conn string, ns db.Namespace, selectName string) tea.Cmd {
	n := m.tree.Namespace(conn, ns)
	if n == nil {
		return nil
	}
	m.tree.StartLoad(n)
	if selectName != "" {
		m.revealing = &revealTarget{conn: conn, path: tablePath(ns, selectName)}
	}
	return loadChildren(m.backend, n)
}

// tablePath is the tree path of a table below its connection.
func tablePath(ns db.Namespace, name string) []string {
	path := []string{ns.Database}
	if ns.Schema != "" {
		path = append(path, ns.Schema)
	}
	return append(path, name)
}

// renameEnteredMsg is the new name typed at the rename prompt.
type renameEnteredMsg struct{ op tableOp }

// askRename opens the prompt for the new name of the selected table.
func (m *Model) askRename(op tableOp) {
	if msg := m.tableOpBlocked(op); msg != "" {
		m.setStatus(msg, true)
		return
	}
	old := op.ref.Name
	m.overlay = overlay.NewPrompt(op.title(), "New name:", old, func(s string) tea.Msg {
		op.newName = strings.TrimSpace(s)
		return renameEnteredMsg{op: op}
	}, overlay.WithValidate(func(s string) error {
		switch strings.TrimSpace(s) {
		case "":
			return errors.New("enter a name")
		case old:
			return errors.New("the name is unchanged")
		}
		return nil
	}))
}

// retargetTabs points the tabs of from at to.
func (m *Model) retargetTabs(conn string, from, to db.TableRef) tea.Cmd {
	var cmds []tea.Cmd
	for _, t := range m.tabs {
		r, ok := t.(retargeter)
		if !ok {
			continue
		}
		if c, ref := t.Target(); c == conn && ref == from {
			cmds = append(cmds, r.retarget(to))
		}
	}
	return tea.Batch(cmds...)
}

// refreshTabsOfTable reloads the data tabs of ref.
func (m *Model) refreshTabsOfTable(conn string, ref db.TableRef) tea.Cmd {
	var cmds []tea.Cmd
	for _, t := range m.tabs {
		if d, ok := t.(*dataTab); ok && d.conn == conn && d.ref == ref {
			cmds = append(cmds, d.Handle(keymap.Refresh))
		}
	}
	return tea.Batch(cmds...)
}
