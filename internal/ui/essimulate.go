package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

// esPipelinesMsg is the pipelines to simulate on hit, with its index's
// default and final pipelines.
type esPipelinesMsg struct {
	tabID           int
	conn            string
	hit             esx.Hit
	names           []string
	defaultP, final string
	err             error
}

func (m esPipelinesMsg) TabID() int               { return m.tabID }
func (m esPipelinesMsg) esMsg()                   {}
func (m esPipelinesMsg) failure() (string, error) { return m.conn, m.err }

// esSimulatePickMsg is the pipeline picked to run on hit.
type esSimulatePickMsg struct {
	tabID    int
	pipeline string
	hit      esx.Hit
}

func (m esSimulatePickMsg) TabID() int { return m.tabID }

// esSimulatedMsg is a _simulate's response.
type esSimulatedMsg struct {
	tabID    int
	conn     string
	pipeline string
	hit      esx.Hit
	out      json.RawMessage
	err      error
}

func (m esSimulatedMsg) TabID() int               { return m.tabID }
func (m esSimulatedMsg) esMsg()                   {}
func (m esSimulatedMsg) failure() (string, error) { return m.conn, m.err }

// simulate is <leader>p: run a pipeline on the selected document, a read
// (spec §8.2).
func (d *esDocs) simulate() tea.Cmd {
	t := d.tab
	h, ok := d.selected()
	if !ok {
		t.setStatus("select a document first", true)
		return nil
	}
	id, conn := t.id, t.conn
	return t.load(func(ctx context.Context, dr es.Doer) tea.Msg {
		msg := esPipelinesMsg{tabID: id, conn: conn, hit: h}
		ps, err := es.Pipelines(ctx, dr)
		if err != nil {
			msg.err = err
			return msg
		}
		for _, p := range ps {
			msg.names = append(msg.names, p.Name)
		}
		if set, err := es.Settings(ctx, dr, h.Index); err == nil {
			for _, s := range set {
				switch s.Key {
				case "index.default_pipeline":
					msg.defaultP = s.Value
				case "index.final_pipeline":
					msg.final = s.Value
				}
			}
		}
		return msg
	}, func(err error) tea.Msg { return esPipelinesMsg{tabID: id, conn: conn, err: err} })
}

// pickPipeline lists the pipelines, the index's default and final first.
func (t *esIndexTab) pickPipeline(m esPipelinesMsg) tea.Cmd {
	if m.err != nil {
		t.setStatus(esReason(m.err), true)
		return nil
	}
	if len(m.names) == 0 {
		t.setStatus("no ingest pipelines", true)
		return nil
	}
	var names, labels []string
	add := func(name, label string) {
		if name != "" && slices.Contains(m.names, name) && !slices.Contains(names, name) {
			names, labels = append(names, name), append(labels, name+label)
		}
	}
	add(m.defaultP, " (default)")
	add(m.final, " (final)")
	for _, n := range m.names {
		add(n, "")
	}
	id, hit := t.id, m.hit
	return openOverlay(overlay.NewPicker("Simulate pipeline on "+hit.ID, labels, func(i int) tea.Msg {
		return esSimulatePickMsg{tabID: id, pipeline: names[i], hit: hit}
	}))
}

func (t *esIndexTab) runSimulate(m esSimulatePickMsg) tea.Cmd {
	id, conn := t.id, t.conn
	return t.load(func(ctx context.Context, d es.Doer) tea.Msg {
		out := esSimulatedMsg{tabID: id, conn: conn, pipeline: m.pipeline, hit: m.hit}
		out.out, out.err = es.SimulatePipeline(ctx, d, m.pipeline, []esx.Hit{m.hit})
		return out
	}, func(err error) tea.Msg { return esSimulatedMsg{tabID: id, conn: conn, err: err} })
}

func (t *esIndexTab) simulated(m esSimulatedMsg) tea.Cmd {
	if m.err != nil {
		t.setStatus(esReason(m.err), true)
		return nil
	}
	title := m.pipeline + " on " + m.hit.ID
	if p := failedProcessor(m.out); p != "" {
		title += " · failed at " + p
	}
	var b bytes.Buffer
	text := string(m.out)
	if json.Indent(&b, m.out, "", "  ") == nil {
		text = b.String()
	}
	return openOverlay(overlay.NewDetail(title, text))
}

// failedProcessor is the type of the first processor a verbose _simulate
// reports an error for.
func failedProcessor(out json.RawMessage) string {
	var r struct {
		Docs []struct {
			Results []struct {
				Type  string          `json:"processor_type"`
				Error json.RawMessage `json:"error"`
			} `json:"processor_results"`
		} `json:"docs"`
	}
	if json.Unmarshal(out, &r) != nil {
		return ""
	}
	for _, d := range r.Docs {
		for _, p := range d.Results {
			if len(p.Error) > 0 && string(p.Error) != "null" {
				return p.Type
			}
		}
	}
	return ""
}
