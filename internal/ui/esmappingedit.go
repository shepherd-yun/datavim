package ui

import (
	"context"
	"encoding/json"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
)

// esEditFor is what the Index tab's $EDITOR session is for.
type esEditFor int

const (
	editDocs    esEditFor = iota // the Documents section's own edits
	editMapping                  // new mapping fields
	editUBQ                      // an update by query's script
)

const newFieldsTemplate = "{\n  \"new_field\": { \"type\": \"keyword\" }\n}\n"

// esMappingRunMsg is a confirmed mapping change.
type esMappingRunMsg struct {
	tabID int
	props json.RawMessage
	n     int
}

func (m esMappingRunMsg) TabID() int { return m.tabID }

// esOpenShardsMsg asks for the cluster tab on an index's shards.
type esOpenShardsMsg struct{ conn, index string }

// handleMapping is a key on the Mapping section.
func (t *esIndexTab) handleMapping(a keymap.Action) (tea.Cmd, bool) {
	if a != keymap.InsertRow {
		return nil, false
	}
	if t.kind != targetIndex {
		t.setStatus("add fields on one index (open it from Indices)", false)
		return nil, true
	}
	t.editFor = editMapping
	return writeTempEdit(t.id, newFieldsTemplate, "datavim-mapping-*.json"), true
}

// mappingEdited checks the fields written in $EDITOR and asks to add them.
func (t *esIndexTab) mappingEdited(text string) tea.Cmd {
	props := json.RawMessage(strings.TrimSpace(text))
	added, conflicts, err := esx.MappingConflicts(t.fields, props)
	switch {
	case err != nil:
		t.setStatus("not a JSON object of fields: "+err.Error(), true)
		return nil
	case len(conflicts) > 0:
		t.setStatus("cannot change an existing field's type ("+strings.Join(conflicts, ", ")+"); reindex into a new index instead", true)
		return nil
	case len(added) == 0:
		t.setStatus("no new fields", false)
		return nil
	}
	body, _ := json.Marshal(map[string]json.RawMessage{"properties": props})
	path := t.target + "/_mapping"
	p := esPlan{
		Class: esx.Classify("PUT", path, body), Count: -1, Method: "PUT", Path: path, Body: body,
		Lines: []string{"add fields to " + t.target + ": " + strings.Join(added, ", ")},
	}
	cfg, _ := t.backend.Connection(t.conn)
	return confirmESCmd(cfg, p, esMappingRunMsg{tabID: t.id, props: props, n: len(added)}, t.setStatus)
}

func (t *esIndexTab) runMapping(m esMappingRunMsg) tea.Cmd {
	index, props := t.target, m.props
	return t.write(idxMapping, "added "+plural(m.n, "field"), func(ctx context.Context, d es.Doer) error {
		return es.PutMapping(ctx, d, index, props)
	})
}

// handleStats is a key on the Stats section.
func (t *esIndexTab) handleStats(a keymap.Action) (tea.Cmd, bool) {
	if a != keymap.OpenCellKey {
		return nil, false
	}
	r, _ := t.statsGrid.Position()
	if r >= len(t.stats) {
		return nil, true
	}
	msg := esOpenShardsMsg{conn: t.conn, index: t.stats[r].Index}
	return func() tea.Msg { return msg }, true
}
