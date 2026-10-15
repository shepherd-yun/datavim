package ui

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/edit"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/session"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// resultFilter is the statement a console result came from and the filter
// narrowing it ("" for none).
type resultFilter struct{ base, where string }

type (
	// consoleMetaMsg is the key and columns of the table behind a result.
	consoleMetaMsg struct {
		tabID int
		ref   db.TableRef
		meta  *tableMeta
		err   error
	}
	// consoleEditsMsg is the outcome of submitting a result's changes.
	consoleEditsMsg struct {
		tabID       int
		gen         int
		session     db.Session
		readOnly    bool
		reopened    bool
		reconnected bool
		began       bool // a transaction was started for manual commit
		n           int
		sql         string
		took        time.Duration
		err         error
	}
)

func (m consoleMetaMsg) TabID() int  { return m.tabID }
func (m consoleEditsMsg) TabID() int { return m.tabID }

// stager: a console stages changes to its result.
func (t *consoleTab) changes() int { return t.res.changes() }

func (t *consoleTab) unsavedHint(submit, discard string) string {
	return t.res.unsavedHint(submit, discard)
}
func (t *consoleTab) submit() tea.Cmd { return t.res.submit() }

// ---- resultHost ----

func (t *consoleTab) busy() string {
	switch {
	case t.running:
		return "a query is running · ctrl+c to cancel"
	case t.txAborted:
		return "the transaction failed · :rollback first"
	}
	return ""
}

func (t *consoleTab) filter() string      { return t.shown.where }
func (t *consoleTab) filterTitle() string { return "Filter result" }

// Task 7 replaces these two.
func (t *consoleTab) checkFilter(string) error { return sqlx.ErrNotSelect }

func (t *consoleTab) refilter(string) tea.Cmd {
	t.setStatus(sqlx.ErrNotSelect.Error(), true)
	return nil
}

// resetResult makes rs the result to edit and finds the table behind it.
func (t *consoleTab) resetResult(rs *db.ResultSet) tea.Cmd {
	r := t.res
	r.table = edit.New(rs.Columns, rs.Rows, nil)
	r.ref, r.cols, r.key, r.mapping, r.addOnly = db.TableRef{}, nil, edit.Key{}, nil, ""
	names := make([]string, len(rs.Columns))
	for i, c := range rs.Columns {
		names[i] = c.Name
	}
	src, why := sqlx.ResultSource(t.dialect, t.shown.base, names)
	if why != "" {
		r.readOnly = "read-only result: " + why + " · edit single-table SELECTs"
		return nil
	}
	r.ref = db.TableRef{Namespace: t.qualified(src.Qualifier), Name: src.Table}
	r.mapping = src.Columns
	r.readOnly = "reading the keys of " + src.Table + "…"
	if tm, ok := t.metas[r.ref]; ok {
		t.applyMeta(tm)
		return nil
	}
	return t.loadMeta(r.ref)
}

func (t *consoleTab) loadMeta(ref db.TableRef) tea.Cmd {
	id, b, conn, d := t.id, t.backend, t.conn, t.dialect
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg := consoleMetaMsg{tabID: id, ref: ref}
		msg.err = withConn(ctx, b, conn, ref.Database, func(c db.Conn) error {
			var err error
			msg.meta, err = readTableMeta(ctx, c.Meta(), d, ref)
			return err
		})
		return msg
	}
}

func (t *consoleTab) metaLoaded(m consoleMetaMsg) {
	if m.err == nil {
		if t.metas == nil {
			t.metas = map[db.TableRef]*tableMeta{}
		}
		t.metas[m.ref] = m.meta
	}
	if m.ref != t.res.ref || t.res.cols != nil {
		return // another result is shown now
	}
	if m.err != nil {
		t.res.readOnly = "read-only result: cannot read the keys of " + m.ref.Name + ": " + errorText(m.err)
		return
	}
	t.applyMeta(m.meta)
}

func (t *consoleTab) applyMeta(tm *tableMeta) {
	r := t.res
	mapping, why := resolveMapping(r.mapping, tm.cols)
	if why != "" {
		r.readOnly = why
		return
	}
	r.readOnly, r.cols, r.key, r.mapping = "", tm.cols, tm.key, mapping
	r.addOnly = keyBlocked(r.ref.Name, tm.key, tm.cols, mapping)
}

// resolveMapping spells each result's table column as the catalog does.
func resolveMapping(names []string, cols []db.Column) ([]string, string) {
	out := make([]string, len(names))
	for i, n := range names {
		j := slices.IndexFunc(cols, func(c db.Column) bool { return strings.EqualFold(c.Name, n) })
		if j < 0 {
			return nil, "read-only result: " + n + " is not a column of the table"
		}
		out[i] = cols[j].Name
	}
	return out, ""
}

// keyBlocked says why rows read through a result whose table columns are
// mapping cannot be changed ("" when they can): every column that locates
// a row must be in the result, and a console result has no ctid.
func keyBlocked(table string, key edit.Key, cols []db.Column, mapping []string) string {
	switch key.Kind {
	case edit.PrimaryKey, edit.UniqueKey:
		kind := "primary key"
		if key.Kind == edit.UniqueKey {
			kind = "unique key"
		}
		for _, k := range key.Columns {
			if !slices.Contains(mapping, k) {
				return k + " (" + kind + ") not in the result: rows can only be added"
			}
		}
	case edit.AllColumns:
		for _, c := range cols {
			if !slices.Contains(mapping, c.Name) {
				return table + " has no key and " + c.Name + " is not in the result: rows can only be added"
			}
		}
	default:
		return table + " has no primary or unique key: rows can only be added"
	}
	return ""
}

// applyEdits runs confirmed changes on the console's session: in a
// transaction of their own under autocommit, else inside the console's
// transaction behind a savepoint (starting it under manual commit).
func (t *consoleTab) applyEdits(stmts []string) tea.Cmd {
	if t.running {
		t.setStatus("a query is already running · ctrl+c to cancel", true)
		return nil
	}
	cfg, _ := t.backend.Connection(t.conn)
	ro := cfg.IsReadOnly()
	ctx, cancel := context.WithCancel(context.Background())
	t.gen++
	t.running, t.cancel, t.started = true, cancel, time.Now()
	t.res.submitting = true
	t.setStatus("", false)
	gen, id, b, conn, ns, d := t.gen, t.id, t.backend, t.conn, t.ns, t.dialect
	s, sessRO, inTx, manual := t.session, t.sessionRO, t.inTx, t.manualTx
	t.session = nil // owned by the command until consoleEditsMsg hands it back
	run := func() tea.Msg {
		defer cancel()
		msg := consoleEditsMsg{tabID: id, gen: gen, readOnly: ro, n: len(stmts), sql: strings.Join(stmts, ";\n") + ";"}
		if s != nil && sessRO != ro {
			_ = s.Close()
			s = nil
			msg.reopened = true
		}
		sess, reconnected, err := ensureSession(ctx, b, conn, ns, s, inTx)
		msg.reconnected = reconnected
		if err != nil {
			msg.err = err
			return msg
		}
		start := time.Now()
		switch {
		case inTx:
			msg.err = edit.ApplyInTx(ctx, sess, stmts)
		case manual:
			if _, err := sess.Exec(ctx, sqlx.BeginSQL(d)); err != nil {
				msg.err = err
				break
			}
			msg.began = true
			msg.err = edit.ApplyInTx(ctx, sess, stmts)
		default:
			msg.err = edit.Apply(ctx, sess, d, stmts)
		}
		msg.took = time.Since(start)
		if db.IsConnLost(msg.err) {
			_ = sess.Close()
			sess = nil
		}
		msg.session = sess
		return msg
	}
	return tea.Batch(run, t.tick(gen))
}

// editsDone takes the session back and reads the result again after a
// submit, or keeps the changes staged when it failed.
func (t *consoleTab) editsDone(m consoleEditsMsg) tea.Cmd {
	t.running, t.cancel = false, nil
	t.session, t.sessionRO = m.session, m.readOnly
	t.res.submitting = false
	t.sessionNotes(m.reconnected, m.reopened, m.readOnly)
	if errors.Is(m.err, errTxLost) {
		t.clearTx()
	}
	if m.began {
		t.inTx, t.txWrites, t.txAborted = true, 0, false
	}
	if m.session == nil && t.inTx && m.err != nil && !errors.Is(m.err, errTxLost) {
		t.log("✗ " + errTxLost.Error())
		t.clearTx()
	}
	record := t.recordEdit(m)
	if m.err != nil {
		t.res.retrySubmit = errors.Is(m.err, session.ErrPasswordRequired)
		t.log("✗ " + oneLine(m.sql) + " — " + errorText(m.err))
		t.setStatus("✗ changes kept · "+errorText(m.err), true)
		return record
	}
	if t.inTx {
		t.txWrites += m.n
	}
	t.log("✓ submitted " + plural(m.n, "change"))
	t.res.table.Reset()
	t.res.showChanges()
	f := t.shown
	t.runText, t.runFilter, t.runNote = "", &f, "submitted "+plural(m.n, "change")
	return tea.Batch(record, t.execute([]sqlx.Statement{{Text: t.last.SQL}}, t.lastLimit, true))
}

func (t *consoleTab) recordEdit(m consoleEditsMsg) tea.Cmd {
	e := history.Entry{Time: time.Now(), Conn: t.conn, Database: t.ns.Database, SQL: m.sql, Duration: m.took}
	if m.err != nil {
		e.Err = errorText(m.err)
	}
	st := t.store
	return func() tea.Msg {
		if err := st.AddHistory(e); err != nil {
			slog.Warn("save history", "err", err)
		}
		return nil
	}
}
