package ui

import (
	"context"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/panes/complete"
)

// esFieldsMsg is the field names of an index, for completion.
type esFieldsMsg struct {
	tabID  int
	index  string
	fields []string
	err    error
}

func (m esFieldsMsg) TabID() int { return m.tabID }

// esIndicesMsg is the index, alias and data stream names, for completion.
type esIndicesMsg struct {
	tabID int
	names []string
	err   error
}

func (m esIndicesMsg) TabID() int { return m.tabID }

var esMethodNames = []string{"GET", "POST", "PUT", "DELETE", "HEAD"}

// popupKey moves in and accepts from the completion popup.
func (t *esConsoleTab) popupKey(key string) (cmd tea.Cmd, used bool) {
	if t.popup == nil {
		if key == "ctrl+n" || key == "ctrl+p" {
			return t.suggest(true), true
		}
		return nil, false
	}
	switch key {
	case "down", "ctrl+n", "ctrl+j", "tab":
		t.popup.Next()
	case "up", "ctrl+p", "ctrl+k", "shift+tab":
		t.popup.Prev()
	case "enter", "ctrl+y":
		c := t.popup.Chosen()
		t.popup = nil
		t.editor.ReplaceBeforeCursor(t.popStart, c.Text)
		return t.scheduleDraft(), true
	case "esc":
		t.popup = nil
	default:
		return nil, false
	}
	return nil, true
}

// pathHead is the typed path of the request line up to its last "/",
// with an index as {index}: "_cat/" or "{index}/".
func pathHead(before string) string {
	f := strings.Fields(before)
	if len(f) < 2 {
		return ""
	}
	p := strings.TrimLeft(f[1], "/")
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return ""
	}
	head := p[:i+1]
	if seg, rest, _ := strings.Cut(head, "/"); !strings.HasPrefix(seg, "_") || seg == "_all" {
		head = "{index}/" + rest
	}
	return head
}

// endpointWords are the endpoints' next path segments after head (""
// for the first segment), without placeholders.
func endpointWords(head string) []string {
	var out []string
	for _, e := range esx.Endpoints {
		rest, ok := strings.CutPrefix(e.Path, head)
		if !ok || rest == "" || strings.HasPrefix(rest, "{") {
			continue
		}
		w, _, _ := strings.Cut(rest, "/")
		if !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// suggest opens (or closes) the completion popup for the word at the
// cursor; without force an empty word opens nothing, except for a body
// key or field just quoted.
func (t *esConsoleTab) suggest(force bool) tea.Cmd {
	t.popup = nil
	line, col := t.editor.Cursor()
	c := esx.CompleteAt(t.editor.Value(), line, col)
	var cands []complete.Candidate
	add := func(kind complete.Kind, detail string, words ...string) {
		for _, w := range words {
			cands = append(cands, complete.Candidate{Text: w, Label: w, Kind: kind, Detail: detail})
		}
	}
	var load tea.Cmd
	switch c.Kind {
	case esx.CompleteNone:
		return nil
	case esx.CompleteMethod:
		if c.Prefix == "" && !force {
			return nil
		}
		add(complete.Keyword, "", esMethodNames...)
	case esx.CompleteIndex:
		if c.Prefix == "" && !force {
			return nil
		}
		if t.indexNames == nil {
			load = t.loadIndexNames()
		}
		add(complete.Table, "", t.indexNames...)
		add(complete.Keyword, "api", endpointWords("")...)
	case esx.CompletePath:
		lines := strings.Split(t.editor.Value(), "\n")
		before := string([]rune(lines[line])[:min(col, len([]rune(lines[line])))])
		add(complete.Keyword, "api", endpointWords(pathHead(before))...)
	case esx.CompleteKey:
		add(complete.Keyword, "", c.Keys...)
	case esx.CompleteField:
		fields, ok := t.fieldCache[c.Index]
		if !ok {
			return t.loadFields(c.Index)
		}
		add(complete.Column, "", fields...)
	}
	ranked := complete.Rank(c.Prefix, cands, maxCandidates)
	if len(ranked) == 0 || !force && len(ranked) == 1 && ranked[0].Label == c.Prefix {
		return load
	}
	t.popup = complete.New(ranked)
	t.popStart = t.editor.Offset() - len(c.Prefix)
	return load
}

func (t *esConsoleTab) loadIndexNames() tea.Cmd {
	t.indexNames = []string{} // one load at a time
	if t.treeNames != nil {
		if names := t.treeNames(); len(names) > 0 {
			t.indexNames = names
			return nil
		}
	}
	b, conn, id := t.backend, t.conn, t.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esIndicesMsg{tabID: id}
		out.err = withES(ctx, b, conn, func(d es.Doer) error {
			idx, err := es.Indices(ctx, d)
			for _, i := range idx {
				if !i.System {
					out.names = append(out.names, i.Name)
				}
			}
			return err
		})
		return out
	}
}

// loadFields reads index's mapping for its field names (cached until a
// refresh); "" is every index.
func (t *esConsoleTab) loadFields(index string) tea.Cmd {
	if t.fieldCache == nil {
		t.fieldCache = map[string][]string{}
	}
	t.fieldCache[index] = nil // one load at a time
	target := index
	if target == "" {
		target = esAllIndices
	}
	b, conn, id := t.backend, t.conn, t.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esFieldsMsg{tabID: id, index: index}
		out.err = withES(ctx, b, conn, func(d es.Doer) error {
			per, err := es.Mappings(ctx, d, target)
			for _, f := range esx.MergeMappings(per) {
				if !f.Object {
					out.fields = append(out.fields, f.Path)
					out.fields = append(out.fields, f.Sub...)
				}
			}
			return err
		})
		return out
	}
}

// completeUpdate takes the completion loads; ok is false for other messages.
func (t *esConsoleTab) completeUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case esFieldsMsg:
		if m.err != nil {
			delete(t.fieldCache, m.index)
			return nil, true
		}
		t.fieldCache[m.index] = m.fields
	case esIndicesMsg:
		if m.err != nil {
			t.indexNames = nil
			return nil, true
		}
		t.indexNames = m.names
	default:
		return nil, false
	}
	if t.CapturesInput() {
		return t.suggest(false), true
	}
	return nil, true
}
