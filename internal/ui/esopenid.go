package ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// esAllIndices is what a bare _id is looked up in from the connection:
// every index but the system ones.
const esAllIndices = "*,-.*"

// esOpenIDMsg is an _id typed at the OpenKey prompt; target is empty on
// the connection, where text may be "index/_id".
type esOpenIDMsg struct {
	conn, target string
	kind         targetKind
	text         string
}

// esIDFoundMsg is what looking an _id up found.
type esIDFoundMsg struct {
	conn, target string
	kind         targetKind
	id           string
	hits         []esx.Hit
	err          error
}

func (m esIDFoundMsg) failure() (string, error) { return m.conn, m.err }

// esIDContext is the connection and target OpenKey works on: the Index
// tab in view, or the selected tree node; ok is false off elasticsearch.
func (m *Model) esIDContext() (conn, target string, kind targetKind, ok bool) {
	if t, isIdx := m.activeTab().(*esIndexTab); isIdx && m.focus == focusTabs {
		return t.conn, t.target, t.kind, true
	}
	n := m.tree.Selected()
	if n == nil || !isES(m.backend, n.Conn) {
		return "", "", 0, false
	}
	switch n.Kind {
	case tree.KindESIndex, tree.KindESAlias, tree.KindDataStream, tree.KindIndexFolder:
		msg := m.openESNode(n)().(openIndexMsg)
		return msg.conn, msg.target, msg.kind, true
	}
	return n.Conn, "", 0, true
}

// openIDPrompt asks for an _id to open; false off elasticsearch.
func (m *Model) openIDPrompt() bool {
	conn, target, kind, ok := m.esIDContext()
	if !ok {
		return false
	}
	title, label := "Open document", "_id in "+target
	if target == "" {
		label = "index/_id, or an _id to look for in every index"
	}
	m.overlay = overlay.NewPrompt(title, label, "", func(text string) tea.Msg {
		return esOpenIDMsg{conn: conn, target: target, kind: kind, text: strings.TrimSpace(text)}
	})
	return true
}

// lookUpID reads one index's document by _id, or searches an alias,
// pattern or data stream (which may hold several) with an ids query.
func (m *Model) lookUpID(msg esOpenIDMsg) tea.Cmd {
	if msg.text == "" {
		return nil
	}
	target, kind, id := msg.target, msg.kind, msg.text
	if target == "" {
		target, kind = esAllIndices, targetPattern
		if i, rest, ok := strings.Cut(id, "/"); ok && i != "" && rest != "" {
			target, kind, id = i, targetIndex, rest
		}
	}
	b, conn := m.backend, msg.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esIDFoundMsg{conn: conn, target: target, kind: kind, id: id}
		out.err = withES(ctx, b, conn, func(d es.Doer) error {
			if kind == targetIndex && !strings.ContainsAny(target, "*,") {
				h, err := es.GetDoc(ctx, d, target, id)
				if errors.Is(err, es.ErrNotFound) {
					return nil
				}
				out.hits = []esx.Hit{h}
				return err
			}
			var err error
			out.hits, err = es.SearchIDs(ctx, d, target, id)
			return err
		})
		return out
	}
}

func (m *Model) idFound(msg esIDFoundMsg) tea.Cmd {
	where := msg.target
	if where == esAllIndices {
		where = "every index"
	}
	switch {
	case msg.err != nil:
		m.setStatus(esReason(msg.err), true)
	case len(msg.hits) == 0:
		m.setStatus("no document with _id "+msg.id+" in "+where, true)
	case len(msg.hits) == 1:
		m.overlay = newESDocDetailOf(m.keys, msg.hits[0])
	default:
		// the same _id in several indices: list them
		cmd := m.openIndex(msg.conn, msg.target, msg.kind)
		t, _ := m.activeTab().(*esIndexTab)
		if t == nil {
			return cmd
		}
		q := `{"ids":{"values":[` + jsonQuote(msg.id) + `]}}`
		t.docs.query, t.section = q, idxDocuments
		if t.docs.started {
			return tea.Batch(cmd, t.docs.reload())
		}
		return cmd
	}
	return nil
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
