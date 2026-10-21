package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

// esServerDetailMsg is an allocation explanation or a node's details,
// read for a popup.
type esServerDetailMsg struct {
	tabID       int
	conn, title string
	text        string
	err         error
}

func (m esServerDetailMsg) TabID() int               { return m.tabID }
func (m esServerDetailMsg) esMsg()                   {}
func (m esServerDetailMsg) failure() (string, error) { return m.conn, m.err }

// esShardFilterMsg is the Shards filter typed in the prompt.
type esShardFilterMsg struct {
	tabID int
	text  string
}

func (m esShardFilterMsg) TabID() int { return m.tabID }

// handleShards is a key on the Shards section.
func (t *esServerTab) handleShards(a keymap.Action) (tea.Cmd, bool) {
	switch a {
	case keymap.ToggleInternal:
		t.shardsAll = !t.shardsAll
		t.renderShards()
		return nil, true
	case keymap.FilterPrompt:
		id := t.id
		return openOverlay(overlay.NewPrompt("Filter shards", "Index name contains (or a * pattern)", t.shardFilter,
			func(text string) tea.Msg { return esShardFilterMsg{tabID: id, text: strings.TrimSpace(text)} })), true
	case keymap.FilterClear:
		t.shardFilter = ""
		return t.setSection(secESShards), true
	case keymap.Open:
		r, _ := t.grids[secESShards].Position()
		if r >= len(t.shardView) {
			return nil, true
		}
		s := t.shardView[r]
		if s.State != "UNASSIGNED" {
			t.setStatus("only unassigned shards have an allocation explanation", false)
			return nil, true
		}
		return t.explain(s), true
	}
	return nil, false
}

func (t *esServerTab) explain(s es.Shard) tea.Cmd {
	pr := "r"
	if s.Primary {
		pr = "p"
	}
	id, b, conn := t.id, t.backend, t.conn
	title := fmt.Sprintf("%s[%d] %s", s.Index, s.Shard, pr)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esServerDetailMsg{tabID: id, conn: conn, title: title}
		out.err = withES(ctx, b, conn, func(d es.Doer) error {
			raw, err := es.AllocationExplain(ctx, d, s.Index, s.Shard, s.Primary)
			out.text = strings.Join(esx.ExplainSummary(raw), "\n")
			return err
		})
		return out
	}
}

func (t *esServerTab) nodeDetail(n es.Node) tea.Cmd {
	id, b, conn := t.id, t.backend, t.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esServerDetailMsg{tabID: id, conn: conn, title: n.Name}
		out.err = withES(ctx, b, conn, func(d es.Doer) error {
			raw, err := es.NodeStats(ctx, d, n.ID)
			if err != nil {
				return err
			}
			rows := esx.NodeSummary(raw)
			w := 0
			for _, r := range rows {
				w = max(w, len(r[0]))
			}
			lines := make([]string, len(rows))
			for i, r := range rows {
				lines[i] = fmt.Sprintf("%-*s  %s", w, r[0], r[1])
			}
			out.text = strings.Join(lines, "\n")
			return nil
		})
		return out
	}
}

// openESShards shows conn's cluster tab on the shards of index, started
// ones included.
func (m *Model) openESShards(conn, index string) tea.Cmd {
	cmd := m.openESServer(conn)
	t, ok := m.activeTab().(*esServerTab)
	if !ok || t.conn != conn {
		return cmd
	}
	t.shardFilter, t.shardsAll = index, true
	return tea.Batch(cmd, t.setSection(secESShards))
}
