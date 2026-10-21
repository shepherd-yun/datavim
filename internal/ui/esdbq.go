package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
)

// esTaskPoll is how often a delete-by-query task is polled.
var esTaskPoll = time.Second

// esDBQPlanMsg is what a delete by query would delete.
type esDBQPlanMsg struct {
	tabID    int
	conn     string
	query    json.RawMessage
	resolved []string
	count    int64
	err      error
}

func (m esDBQPlanMsg) TabID() int               { return m.tabID }
func (m esDBQPlanMsg) esMsg()                   {}
func (m esDBQPlanMsg) failure() (string, error) { return m.conn, m.err }

// esDBQRunMsg runs the confirmed delete on the indices it was shown.
type esDBQRunMsg struct {
	tabID   int
	indices []string
	query   json.RawMessage
}

func (m esDBQRunMsg) TabID() int { return m.tabID }

// esTaskMsg is a delete-by-query task's progress.
type esTaskMsg struct {
	tabID  int
	conn   string
	task   string
	status es.TaskStatus
	err    error
}

func (m esTaskMsg) TabID() int               { return m.tabID }
func (m esTaskMsg) esMsg()                   {}
func (m esTaskMsg) failure() (string, error) { return m.conn, m.err }

// deleteByQuery is X: the indices the target expands to and how many
// documents the filter matches, for the confirmation.
func (d *esDocs) deleteByQuery() tea.Cmd {
	t := d.tab
	if cfg, _ := t.backend.Connection(t.conn); cfg.IsReadOnly() {
		t.setStatus(readOnlyHint, true)
		return nil
	}
	query, err := esx.SearchQuery(d.query)
	if err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	b, conn, id, target, tabCtx := t.backend, t.conn, t.id, t.target, t.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		msg := esDBQPlanMsg{tabID: id, conn: conn, query: query}
		msg.err = withES(ctx, b, conn, func(doer es.Doer) error {
			r, err := es.ResolveIndex(ctx, doer, target)
			if err != nil {
				return err
			}
			msg.resolved = r.Indices
			msg.count, err = es.Count(ctx, doer, target, query)
			return err
		})
		return msg
	}
}

func (d *esDocs) dbqPlanned(m esDBQPlanMsg) tea.Cmd {
	t := d.tab
	if m.err != nil {
		t.setStatus(esReason(m.err), true)
		return nil
	}
	if len(m.resolved) == 0 {
		t.setStatus("no index matches "+t.target, true)
		return nil
	}
	q := strings.TrimSpace(d.query)
	cls := esx.Class{Level: esx.LevelDanger, Op: "delete by query", Targets: []string{t.target}}
	indices := strings.Join(m.resolved, ", ")
	lines := []string{fmt.Sprintf("delete %s matching %s in %s", plural(int(m.count), "document"), q, indices)}
	if q == "" {
		lines = []string{fmt.Sprintf("deletes ALL %s documents in %s", groupDigits(m.count), indices)}
	}
	if q == "" || len(m.resolved) > 1 {
		cls.Level = esx.LevelCritical
	}
	p := esPlan{Class: cls, Resolved: m.resolved, Count: m.count, Lines: lines, Expect: t.target}
	cfg, _ := t.backend.Connection(t.conn)
	return confirmESCmd(cfg, p, esDBQRunMsg{tabID: t.id, indices: m.resolved, query: m.query}, t.setStatus)
}

// runDBQ starts the delete on the indices shown in the confirmation.
func (d *esDocs) runDBQ(m esDBQRunMsg) tea.Cmd {
	t := d.tab
	b, conn, id, tabCtx := t.backend, t.conn, t.id, t.ctx
	t.setStatus("deleting…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		out := esTaskMsg{tabID: id, conn: conn}
		doer, err := b.ES(ctx, conn)
		if err == nil {
			out.task, err = es.DeleteByQuery(ctx, doer, m.indices, m.query)
		}
		out.err = err
		return out
	}
}

// pollTask reads the task's progress after esTaskPoll.
func (d *esDocs) pollTask(task string) tea.Cmd {
	t := d.tab
	b, conn, id, tabCtx := t.backend, t.conn, t.id, t.ctx
	return tea.Tick(esTaskPoll, func(time.Time) tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		out := esTaskMsg{tabID: id, conn: conn, task: task}
		doer, err := b.ES(ctx, conn)
		if err == nil {
			out.status, err = es.GetTask(ctx, doer, task)
		}
		out.err = err
		return out
	})
}

func (d *esDocs) tasked(m esTaskMsg) tea.Cmd {
	t := d.tab
	s := m.status
	switch {
	case m.err != nil:
		t.setStatus("delete by query: "+esReason(m.err), true)
		return nil
	case s.Err != "":
		t.setStatus("delete by query failed: "+s.Err, true)
		return d.reload()
	case !s.Completed:
		t.setStatus(fmt.Sprintf("deleting… %s / %s", groupDigits(s.Deleted), groupDigits(s.Total)), false)
		return d.pollTask(m.task)
	}
	msg := "deleted " + plural(int(s.Deleted), "document")
	if s.Failures > 0 {
		msg += fmt.Sprintf(" · %d failures", s.Failures)
	}
	t.setStatus(msg, s.Failures > 0)
	return d.reload()
}
