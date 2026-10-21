package ui

import (
	"errors"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/complete"
)

// esFilterHistory is how many filters the prompt offers per index.
const esFilterHistory = 50

// esFilterMsg is a filter entered at the F prompt.
type esFilterMsg struct {
	tabID int
	text  string
}

func (m esFilterMsg) TabID() int { return m.tabID }

func (d *esDocs) historyKey() string { return d.tab.conn + "\x00" + d.tab.target }

// prompt opens the filter prompt with text, its history read first; err
// is a refusal of the server to show under it.
func (d *esDocs) prompt(text, err string) tea.Cmd {
	t := d.tab
	st, key, id, title := t.store, d.historyKey(), t.id, "Filter "+t.target
	complete := d.completeFilter
	return func() tea.Msg {
		var hist []string
		if st != nil {
			hist, _ = st.Filters(key, esFilterHistory)
		}
		opts := []overlay.PromptOption{
			overlay.WithCompleter(complete), overlay.WithHistory(hist),
			overlay.WithValidate(func(s string) error { _, err := esx.SearchQuery(s); return err }),
		}
		if err != "" {
			opts = append(opts, overlay.WithError(err))
		}
		return openOverlayMsg{overlay: overlay.NewPrompt(title,
			`Lucene query string (status:paid AND user.name:"张三") or a {…} query DSL object · empty shows every document`,
			text, func(s string) tea.Msg { return esFilterMsg{tabID: id, text: s} }, opts...)}
	}
}

// completeFilter offers the mapped field names for the word before the
// cursor.
func (d *esDocs) completeFilter(text string, cursor int) (int, []complete.Candidate) {
	rs := []rune(text)
	cursor = min(cursor, len(rs))
	start := cursor
	for start > 0 && strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._@-", rs[start-1]) {
		start--
	}
	word := strings.ToLower(string(rs[start:cursor]))
	var out []complete.Candidate
	for _, f := range d.tab.fields {
		if !f.Object && strings.HasPrefix(strings.ToLower(f.Path), word) {
			out = append(out, complete.Candidate{Text: f.Path, Label: f.Path, Detail: f.Type, Kind: complete.Column})
		}
	}
	return start, out
}

// filtered runs a filter from the prompt and remembers it.
func (d *esDocs) filtered(text string) tea.Cmd {
	d.prevQuery, d.query, d.fromPrompt = d.query, strings.TrimSpace(text), true
	st, key, q := d.tab.store, d.historyKey(), d.query
	save := func() tea.Msg {
		if st != nil && q != "" {
			_ = st.AddFilter(key, q)
		}
		return nil
	}
	return tea.Batch(d.reload(), save)
}

// queryRefused reports a search the server refused for its query; the
// reason is the root cause's.
func queryRefused(err error) (string, bool) {
	var ae *es.APIError
	if !errors.As(err, &ae) || ae.Status != 400 {
		return "", false
	}
	if ae.RootReason != "" {
		return ae.RootReason, true
	}
	return ae.Error(), true
}

// cellFilter appends the f key's clause for the cell under the cursor.
func (d *esDocs) cellFilter(a keymap.Action) tea.Cmd {
	t := d.tab
	if a == keymap.FilterClear {
		if d.query == "" {
			return nil
		}
		d.query = ""
		return d.reload()
	}
	h, ok := d.selected()
	col := d.column()
	if !ok || col == "" {
		t.setStatus("no field to filter by here", true)
		return nil
	}
	v := cell(h, col)
	missing := v.Null || v.Text == "" && !hasField(h, col)
	var clause string
	switch a {
	case keymap.FilterEqual:
		clause = esx.FieldEquals(col, v.Text)
		if missing {
			clause = esx.FieldExists(col, false)
		}
	case keymap.FilterNotEqual:
		clause = esx.FieldNot(col, v.Text)
		if missing {
			clause = esx.FieldExists(col, true)
		}
	case keymap.FilterContains:
		clause = esx.FieldContains(col, v.Text)
	case keymap.FilterGreater:
		clause = esx.FieldCompare(col, ">", v.Text)
	case keymap.FilterLess:
		clause = esx.FieldCompare(col, "<", v.Text)
	case keymap.FilterNull:
		clause = esx.FieldExists(col, false)
	case keymap.FilterNotNull:
		clause = esx.FieldExists(col, true)
	default:
		return nil
	}
	d.query = esx.AndQuery(d.query, clause)
	return d.reload()
}

func hasField(h esx.Hit, col string) bool {
	if col == "_id" || col == "_index" {
		return true
	}
	_, ok := esx.GetPath(h.Source, col)
	return ok
}

// hitWords are the highlighted words of the field of a column.
func hitWords(h esx.Hit, col string) []string {
	var frags []string
	for f, fs := range h.Highlight {
		if f == col || strings.HasPrefix(f, col+".") {
			frags = append(frags, fs...)
		}
	}
	if frags == nil {
		return nil
	}
	return highlightWords(esx.Hit{Highlight: map[string][]string{col: frags}})
}

// extraHits is "+ remark, notes": the hit's highlighted fields that are
// not columns.
func extraHits(h esx.Hit, cols []string) string {
	var extra []string
	for f := range h.Highlight {
		base := f
		for _, c := range cols {
			if strings.HasPrefix(f, c+".") {
				base = c
			}
		}
		if !slices.Contains(cols, base) {
			extra = append(extra, f)
		}
	}
	if extra == nil {
		return ""
	}
	slices.Sort(extra)
	return "+ " + strings.Join(extra, ", ")
}
