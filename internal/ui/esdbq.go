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
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
)

// esTaskPoll is how often a by-query task is polled.
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

// Ops of the by-query tasks.
const (
	taskDelete = "delete"
	taskUpdate = "update"
)

// esTaskMsg is a by-query task's progress.
type esTaskMsg struct {
	tabID  int
	conn   string
	op     string // taskDelete or taskUpdate
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
		out := esTaskMsg{tabID: id, conn: conn, op: taskDelete}
		doer, err := b.ES(ctx, conn)
		if err == nil {
			out.task, err = es.DeleteByQuery(ctx, doer, m.indices, m.query)
		}
		out.err = err
		return out
	}
}

// pollTask reads the task's progress after esTaskPoll.
func (d *esDocs) pollTask(task, op string) tea.Cmd {
	t := d.tab
	b, conn, id, tabCtx := t.backend, t.conn, t.id, t.ctx
	return tea.Tick(esTaskPoll, func(time.Time) tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		out := esTaskMsg{tabID: id, conn: conn, op: op, task: task}
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
	name, doing, done, n := "delete by query", "deleting…", "deleted ", s.Deleted
	if m.op == taskUpdate {
		name, doing, done, n = "update by query", "updating…", "updated ", s.Updated
	}
	switch {
	case m.err != nil:
		t.setStatus(name+": "+esReason(m.err), true)
		return nil
	case s.Err != "":
		t.setStatus(name+" failed: "+s.Err, true)
		return d.reload()
	case !s.Completed:
		t.setStatus(fmt.Sprintf("%s %s / %s", doing, groupDigits(n), groupDigits(s.Total)), false)
		return d.pollTask(m.task, m.op)
	}
	msg := done + plural(int(n), "document")
	if s.Failures > 0 {
		msg += fmt.Sprintf(" · %d failures", s.Failures)
	}
	t.setStatus(msg, s.Failures > 0)
	return d.reload()
}

// esUBQPlanMsg is what an update by query would update.
type esUBQPlanMsg struct {
	tabID    int
	conn     string
	query    json.RawMessage
	script   string
	resolved []string
	count    int64
	err      error
}

func (m esUBQPlanMsg) TabID() int               { return m.tabID }
func (m esUBQPlanMsg) esMsg()                   {}
func (m esUBQPlanMsg) failure() (string, error) { return m.conn, m.err }

// esUBQRunMsg runs the confirmed update on the indices it was shown.
type esUBQRunMsg struct {
	tabID   int
	indices []string
	query   json.RawMessage
	script  string
}

func (m esUBQRunMsg) TabID() int { return m.tabID }

// updateByQuery is <Space>u: the painless script is written in $EDITOR.
func (d *esDocs) updateByQuery() tea.Cmd {
	t := d.tab
	if cfg, _ := t.backend.Connection(t.conn); cfg.IsReadOnly() {
		t.setStatus(readOnlyHint, true)
		return nil
	}
	if _, err := esx.SearchQuery(d.query); err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	filter := strings.TrimSpace(d.query)
	if filter == "" {
		filter = "(all documents)"
	}
	text := "// update by query on " + t.target + " · filter: " + filter + "\n" +
		"// ctx._source.field = value;\n\n"
	t.editFor = editUBQ
	return writeTempEdit(t.id, text, "datavim-ubq-*.painless")
}

// ubqEdited plans the update with the script written in $EDITOR.
func (d *esDocs) ubqEdited(text string) tea.Cmd {
	t := d.tab
	var lines []string
	for l := range strings.SplitSeq(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "//") {
			lines = append(lines, l)
		}
	}
	script := strings.TrimSpace(strings.Join(lines, "\n"))
	if script == "" {
		t.setStatus("no script; nothing to do", false)
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
		msg := esUBQPlanMsg{tabID: id, conn: conn, query: query, script: script}
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

func (d *esDocs) ubqPlanned(m esUBQPlanMsg) tea.Cmd {
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
	indices := strings.Join(m.resolved, ", ")
	lines := []string{fmt.Sprintf("update %s matching %s in %s", plural(int(m.count), "document"), q, indices)}
	if q == "" {
		lines = []string{fmt.Sprintf("updates ALL %s documents in %s", groupDigits(m.count), indices)}
	}
	lines = append(lines, "")
	lines = append(lines, firstLines(m.script, 5)...)
	p := esPlan{
		Class: esx.Classify("POST", t.target+"/_update_by_query", nil), Resolved: m.resolved, Count: m.count,
		Lines: lines, Expect: t.target,
	}
	cfg, _ := t.backend.Connection(t.conn)
	return confirmESCmd(cfg, p, esUBQRunMsg{tabID: t.id, indices: m.resolved, query: m.query, script: m.script}, t.setStatus)
}

// runUBQ starts the update on the indices shown in the confirmation.
func (d *esDocs) runUBQ(m esUBQRunMsg) tea.Cmd {
	t := d.tab
	b, conn, id, tabCtx := t.backend, t.conn, t.id, t.ctx
	t.setStatus(joinHints("updating…", keyHint(t.keys, keymap.Global, keymap.ServerInfo, "→ Tasks to watch or cancel")), false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		out := esTaskMsg{tabID: id, conn: conn, op: taskUpdate}
		doer, err := b.ES(ctx, conn)
		if err == nil {
			out.task, err = es.UpdateByQuery(ctx, doer, m.indices, m.query, m.script)
		}
		out.err = err
		return out
	}
}
