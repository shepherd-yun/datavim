package ui

import (
	"context"
	"encoding/json"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

// esReindexMsg is Reindex… picked on an index.
type esReindexMsg struct{ conn, source string }

// esReindexPlanMsg is what the reindex's destination is, and the source's
// mapping and settings to create it with.
type esReindexPlanMsg struct {
	conn, source  string
	form          overlay.ReindexFormMsg
	query         json.RawMessage
	exists, alias bool
	mapping       json.RawMessage
	settings      map[string]json.RawMessage
	err           error
}

func (m esReindexPlanMsg) failure() (string, error) { return m.conn, m.err }

// esReindexCreateMsg is the choice for a missing destination: create it
// from the source, or let the server create it.
type esReindexCreateMsg struct {
	plan   esReindexPlanMsg
	create bool
}

// esReindexRunMsg runs a confirmed reindex, creating its destination
// first when create is set.
type esReindexRunMsg struct {
	conn   string
	spec   es.ReindexSpec
	create *es.NewIndex
}

// esReindexStartedMsg is a reindex's task, or why it did not start.
type esReindexStartedMsg struct {
	conn, task string
	err        error
}

func (m esReindexStartedMsg) failure() (string, error) { return m.conn, m.err }

func (m *Model) esReindex(msg esReindexMsg) tea.Cmd {
	if m.refuseReadOnly(msg.conn) {
		return nil
	}
	m.esFormConn, m.esReindexSource = msg.conn, msg.source
	m.overlay = overlay.NewReindexForm(m.keys, msg.source)
	return nil
}

// esReindexFormed reads what the destination is before asking.
func (m *Model) esReindexFormed(form overlay.ReindexFormMsg) tea.Cmd {
	query, err := esx.SearchQuery(form.Query)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	if form.Query == "" {
		query = nil
	}
	b, conn, source := m.backend, m.esFormConn, m.esReindexSource
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esReindexPlanMsg{conn: conn, source: source, form: form, query: query}
		out.err = withES(ctx, b, conn, func(d es.Doer) error {
			aliases, err := es.Aliases(ctx, d)
			if err != nil {
				return err
			}
			for _, a := range aliases {
				if a.Name == form.Dest {
					out.alias = true
					return nil
				}
			}
			if out.exists, err = es.IndexExists(ctx, d, form.Dest); err != nil || out.exists {
				return err
			}
			mappings, err := es.RawMappings(ctx, d, source)
			if err != nil {
				return err
			}
			out.mapping = mappings[source]
			out.settings, err = es.RawSettings(ctx, d, source)
			return err
		})
		return out
	}
}

func (m *Model) esReindexPlanned(p esReindexPlanMsg) tea.Cmd {
	switch {
	case p.err != nil:
		m.setStatus(esReason(p.err), true)
		return nil
	case p.alias:
		m.setStatus(p.form.Dest+" is an alias; reindex into an index", true)
		return nil
	case p.exists:
		return m.confirmReindex(p, false)
	}
	m.overlay = overlay.NewChoice(p.form.Dest+" does not exist", []string{
		"Create it with " + p.source + "'s mapping and settings",
		"Let Elasticsearch create it (dynamic mapping)",
	}, func(i int) tea.Msg { return esReindexCreateMsg{plan: p, create: i == 0} })
	return nil
}

func (m *Model) confirmReindex(p esReindexPlanMsg, create bool) tea.Cmd {
	f := p.form
	spec := es.ReindexSpec{Source: p.source, Dest: f.Dest, Query: p.query, OpType: f.OpType, Conflicts: f.Conflicts}
	lines := []string{fmt.Sprintf("reindex %s → %s · op_type %s · conflicts %s", p.source, f.Dest, f.OpType, f.Conflicts)}
	if f.Query != "" {
		lines = append(lines, "query: "+f.Query)
	}
	run := esReindexRunMsg{conn: p.conn, spec: spec}
	if create {
		settings, shards, replicas := esx.CopyableSettings(p.settings)
		raw, _ := json.Marshal(settings)
		run.create = &es.NewIndex{Name: f.Dest, Shards: shards, Replicas: replicas, Mappings: p.mapping, Settings: raw}
		lines = append(lines, fmt.Sprintf("create %s: %s, %s, %s's mapping", f.Dest, plural(shards, "shard"), plural(replicas, "replica"), p.source))
	}
	body := es.ReindexBody(spec)
	plan := esPlan{
		Class: esx.Classify("POST", "_reindex", body), Count: -1, Lines: lines,
		Method: "POST", Path: "_reindex?wait_for_completion=false", Body: body,
	}
	return m.confirmES(p.conn, plan, run)
}

func (m *Model) esReindexRun(msg esReindexRunMsg) tea.Cmd {
	b := m.backend
	m.setStatus("starting the reindex…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esReindexStartedMsg{conn: msg.conn}
		out.err = withES(ctx, b, msg.conn, func(d es.Doer) error {
			if msg.create != nil {
				if err := es.CreateIndex(ctx, d, *msg.create); err != nil {
					return fmt.Errorf("create %s: %w", msg.create.Name, err)
				}
			}
			var err error
			out.task, err = es.Reindex(ctx, d, msg.spec)
			return err
		})
		return out
	}
}

func (m *Model) esReindexStarted(msg esReindexStartedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(esReason(msg.err), true)
		return nil
	}
	cmd := m.openESTasks(msg.conn)
	m.setStatus("reindex started: "+msg.task, false)
	return tea.Batch(cmd, m.reloadESTree(msg.conn))
}
