package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/edit"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/session"
)

// cellEditedMsg is a value saved in the cell editor.
type cellEditedMsg struct {
	tabID    int
	table    *edit.Table // the page the edit was made on
	row, col int
	value    db.Value
}

func (m cellEditedMsg) TabID() int { return m.tabID }

// plural is "1 change", "2 changes".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

type (
	// submitConfirmedMsg is y in the preview.
	submitConfirmedMsg struct{ tabID, gen int }
	// editsAppliedMsg is the outcome of a submit.
	editsAppliedMsg struct {
		tabID int
		conn  string
		n     int // statements
		err   error
	}
)

func (m submitConfirmedMsg) TabID() int { return m.tabID }
func (m editsAppliedMsg) TabID() int    { return m.tabID }

func (t *dataTab) busy() string {
	if t.loading {
		return "wait for the rows to load"
	}
	return ""
}

func (t *dataTab) filter() string      { return t.query.where }
func (t *dataTab) filterTitle() string { return "Filter " + t.ref.Name }

func (t *dataTab) refilter(where string) tea.Cmd {
	return t.applyQuery(func(q *tableQuery) { q.where = where })
}

// applyEdits runs the confirmed statements in one transaction on a session
// of their own. It never retries: a dropped connection may or may not have
// committed.
func (t *dataTab) applyEdits(stmts []string) tea.Cmd {
	t.submitting = true
	t.setStatus("submitting…", false)
	id, b, st, conn, ref, d := t.id, t.backend, t.store, t.conn, t.ref, t.dialect
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg := editsAppliedMsg{tabID: id, conn: conn, n: len(stmts)}
		c, err := b.Conn(ctx, conn, ref.Database)
		if err != nil {
			msg.err = err
			return msg
		}
		s, err := c.Session(ctx)
		if err != nil {
			if db.IsConnLost(err) {
				b.Reset(conn)
			}
			msg.err = err
			return msg
		}
		start := time.Now()
		msg.err = edit.Apply(ctx, s, d, stmts)
		_ = s.Close()
		if db.IsConnLost(msg.err) {
			b.Reset(conn)
		}
		entry := history.Entry{
			Time: start, Conn: conn, Database: ref.Database,
			SQL: strings.Join(stmts, ";\n") + ";", Duration: time.Since(start),
		}
		if msg.err != nil {
			entry.Err = errorText(msg.err)
		}
		if err := st.AddHistory(entry); err != nil {
			slog.Warn("save history", "err", err)
		}
		return msg
	}
}

// applied reloads the page after a submit, or keeps the changes staged
// when it failed. The staged changes are dropped before reloading so a
// failed reload can never submit them twice.
func (t *dataTab) applied(m editsAppliedMsg) tea.Cmd {
	t.submitting = false
	if m.err != nil {
		t.retrySubmit = errors.Is(m.err, session.ErrPasswordRequired)
		// "changes kept" first: the tab's status line is narrow.
		t.setStatus("✗ changes kept · "+errorText(m.err), true)
		return nil
	}
	t.table.Reset()
	t.showChanges()
	t.setStatus("submitted "+plural(m.n, "change"), false)
	return t.load(t.page, t.query)
}
