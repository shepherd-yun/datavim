package ui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// esIndexOpMsg is an index operation picked from the actions menu.
type esIndexOpMsg struct{ conn, index, op string }

// The operations of esIndexOpMsg and esIndexRunMsg.
const (
	opOpen       = "open"
	opClose      = "close"
	opRefresh    = "refresh"
	opFlush      = "flush"
	opCache      = "cache"
	opForceMerge = "forcemerge"
	opDelete     = "delete"
	opCreate     = "create"
)

// esForceMergeMsg is the max segments typed for a force merge.
type esForceMergeMsg struct{ conn, index, text string }

// esDeletePlanMsg is what a rollover group's pattern expands to.
type esDeletePlanMsg struct {
	conn, expr string
	resolved   []string
	err        error
}

func (m esDeletePlanMsg) failure() (string, error) { return m.conn, m.err }

// esIndexRunMsg runs a confirmed index operation.
type esIndexRunMsg struct {
	conn, op string
	indices  []string
	maxSeg   int
	index    es.NewIndex
}

// esIndexDoneMsg is an index operation's outcome.
type esIndexDoneMsg struct {
	conn, done string
	err        error
}

func (m esIndexDoneMsg) failure() (string, error) { return m.conn, m.err }

// esIndexMenu are the actions menu's index operations.
func esIndexMenu(conn, index string, closed bool) []menuEntry {
	op := func(o, desc string) menuEntry {
		return menuEntry{desc: desc, msg: esIndexOpMsg{conn: conn, index: index, op: o}}
	}
	out := []menuEntry{op(opClose, "Close index…")}
	if closed {
		out = []menuEntry{op(opOpen, "Open (unclose) index…")}
	}
	return append(out, op(opRefresh, "Refresh index"), op(opFlush, "Flush index"), op(opCache, "Clear caches"), op(opForceMerge, "Force merge…"),
		menuEntry{desc: "Reindex…", msg: esReindexMsg{conn: conn, source: index}})
}

// indexOpRequest is an operation's method and path, for its class.
func indexOpRequest(op, index string, maxSeg int) (method, path string) {
	switch op {
	case opOpen:
		return "POST", index + "/_open"
	case opClose:
		return "POST", index + "/_close"
	case opRefresh:
		return "POST", index + "/_refresh"
	case opFlush:
		return "POST", index + "/_flush"
	case opCache:
		return "POST", index + "/_cache/clear"
	case opForceMerge:
		return "POST", index + "/_forcemerge?max_num_segments=" + strconv.Itoa(maxSeg)
	}
	return "DELETE", index
}

func (m *Model) esIndexOp(msg esIndexOpMsg) tea.Cmd {
	if msg.op == opForceMerge {
		if m.refuseReadOnly(msg.conn) {
			return nil
		}
		conn, index := msg.conn, msg.index
		m.overlay = overlay.NewPrompt("Force merge "+index, "Max segments per shard (merging blocks writes and costs I/O)", "1",
			func(text string) tea.Msg { return esForceMergeMsg{conn: conn, index: index, text: text} })
		return nil
	}
	return m.confirmIndexOp(msg.conn, msg.op, msg.index, 0)
}

func (m *Model) confirmIndexOp(conn, op, index string, maxSeg int) tea.Cmd {
	method, path := indexOpRequest(op, index, maxSeg)
	p := esPlan{Class: esx.Classify(method, path, nil), Method: method, Path: path, Lines: []string{method + " " + path}}
	return m.confirmES(conn, p, esIndexRunMsg{conn: conn, op: op, indices: []string{index}, maxSeg: maxSeg})
}

func (m *Model) esForceMerge(msg esForceMergeMsg) tea.Cmd {
	n, err := strconv.Atoi(strings.TrimSpace(msg.text))
	if err != nil || n < 1 {
		m.setStatus("max segments must be a number ≥ 1", true)
		return nil
	}
	return m.confirmIndexOp(msg.conn, opForceMerge, msg.index, n)
}

// askDeleteIndex is dd on an index: its documents, size, aliases and
// data stream in the confirmation; on a rollover group, the indices its
// pattern expands to now.
func (m *Model) askDeleteIndex(n *tree.Node) tea.Cmd {
	if m.refuseReadOnly(n.Conn) {
		return nil
	}
	if n.Kind == tree.KindIndexFolder {
		b, conn, expr := m.backend, n.Conn, n.Key+"*"
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
			defer cancel()
			out := esDeletePlanMsg{conn: conn, expr: expr}
			out.err = withES(ctx, b, conn, func(d es.Doer) error {
				r, err := es.ResolveIndex(ctx, d, expr)
				out.resolved = r.Indices
				return err
			})
			return out
		}
	}
	name := n.Key
	lines := []string{"delete index " + name}
	if st := m.esTrees[n.Conn]; st != nil {
		for _, i := range st.indices {
			if i.Name == name {
				lines[0] += fmt.Sprintf(" · %s docs · %s", groupDigits(i.Docs), humanBytes(i.StoreBytes))
			}
		}
		var aliases []string
		for _, a := range st.aliases {
			if a.Index == name {
				aliases = append(aliases, a.Name)
			}
		}
		if aliases != nil {
			lines = append(lines, "aliases: "+strings.Join(aliases, ", "))
		}
		for _, s := range st.streams {
			if slices.Contains(s.Indices, name) {
				lines = append(lines, "backs the data stream "+s.Name)
			}
		}
	}
	p := esPlan{Class: esx.Classify("DELETE", name, nil), Lines: lines, Method: "DELETE", Path: name}
	return m.confirmES(n.Conn, p, esIndexRunMsg{conn: n.Conn, op: opDelete, indices: []string{name}})
}

func (m *Model) esDeletePlanned(msg esDeletePlanMsg) tea.Cmd {
	switch {
	case msg.err != nil:
		m.setStatus(esReason(msg.err), true)
		return nil
	case len(msg.resolved) == 0:
		m.setStatus("no index matches "+msg.expr, true)
		return nil
	}
	p := esPlan{Class: esx.Classify("DELETE", msg.expr, nil), Resolved: msg.resolved, Lines: []string{"delete the indices of " + msg.expr}}
	return m.confirmES(msg.conn, p, esIndexRunMsg{conn: msg.conn, op: opDelete, indices: msg.resolved})
}

// newIndexForm is o under Indices.
func (m *Model) newIndexForm(n *tree.Node) tea.Cmd {
	if n.Section != esSecIndices && n.Kind != tree.KindConn {
		m.setStatus("select Indices to add an index", true)
		return nil
	}
	if m.refuseReadOnly(n.Conn) {
		return nil
	}
	m.esFormConn = n.Conn
	m.overlay = overlay.NewIndexForm(m.keys)
	return nil
}

func (m *Model) esIndexFormed(msg overlay.IndexFormMsg) tea.Cmd {
	n, conn := msg.Index, m.esFormConn
	line := fmt.Sprintf("create index %s · %s · %s", n.Name, plural(n.Shards, "shard"), plural(n.Replicas, "replica"))
	p := esPlan{Class: esx.Classify("PUT", n.Name, nil), Lines: []string{line}, Method: "PUT", Path: n.Name}
	return m.confirmES(conn, p, esIndexRunMsg{conn: conn, op: opCreate, index: n})
}

func (m *Model) esIndexRun(msg esIndexRunMsg) tea.Cmd {
	b := m.backend
	m.setStatus("working…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esIndexDoneMsg{conn: msg.conn}
		names := strings.Join(msg.indices, ", ")
		d, err := b.ES(ctx, msg.conn)
		if err != nil {
			out.err = err
			return out
		}
		target := strings.Join(msg.indices, ",")
		switch msg.op {
		case opCreate:
			err, out.done = es.CreateIndex(ctx, d, msg.index), "created "+msg.index.Name
		case opDelete:
			err, out.done = es.DeleteIndices(ctx, d, msg.indices), "deleted "+names
		case opOpen:
			err, out.done = es.OpenIndex(ctx, d, target), "opened "+names
		case opClose:
			err, out.done = es.CloseIndex(ctx, d, target), "closed "+names
		case opRefresh:
			err, out.done = es.Refresh(ctx, d, target), "refreshed "+names
		case opFlush:
			err, out.done = es.Flush(ctx, d, target), "flushed "+names
		case opCache:
			err, out.done = es.ClearCache(ctx, d, target), "cleared the caches of "+names
		case opForceMerge:
			err, out.done = es.ForceMerge(ctx, d, target, msg.maxSeg), "force-merged "+names
		}
		out.err = err
		return out
	}
}

func (m *Model) esIndexDone(msg esIndexDoneMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(esReason(msg.err), true)
	} else {
		m.setStatus(msg.done, false)
	}
	return m.reloadESTree(msg.conn)
}

// esTreeWrite handles the tree's write keys on an elasticsearch node.
func (m *Model) esTreeWrite(a keymap.Action, n *tree.Node) (tea.Cmd, bool) {
	switch a {
	case keymap.NewKey:
		return m.newIndexForm(n), true
	case keymap.DeleteNode:
		switch n.Kind {
		case tree.KindConn:
			return nil, false
		case tree.KindESIndex, tree.KindIndexFolder:
			return m.askDeleteIndex(n), true
		case tree.KindESTemplate:
			return m.askDeleteTemplate(n), true
		}
		m.setStatus("only indices, templates (and the connection) can be deleted here", true)
		return nil, true
	}
	return nil, false
}
