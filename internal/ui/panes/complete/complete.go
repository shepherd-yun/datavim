// Package complete is the console's completion popup.
package complete

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"

	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// Kind tags a candidate.
type Kind int

const (
	Keyword Kind = iota
	Table
	View
	Column
	Namespace // a database (MySQL) or schema (PostgreSQL)
	Command   // a redis command or subcommand
	Key       // a redis key
)

var tags = [...]string{"kw", "table", "view", "col", "ns", "cmd", "key"}

// Candidate is one completion.
type Candidate struct {
	Text   string // inserted text, quoted if needed
	Label  string // the name as shown and matched
	Kind   Kind
	Detail string // e.g. a column type
}

// Rank puts the candidates whose label is prefix (ignoring case) first,
// then those that start with it in their given order, then the others that
// match it fuzzily, best first; at most limit. An empty prefix keeps every
// candidate.
func Rank(prefix string, cands []Candidate, limit int) []Candidate {
	var out []Candidate
	if prefix == "" {
		out = append(out, cands...)
	} else {
		p := strings.ToLower(prefix)
		var rest []int
		var starts []Candidate
		for i, c := range cands {
			switch l := strings.ToLower(c.Label); {
			case l == p:
				out = append(out, c)
			case strings.HasPrefix(l, p):
				starts = append(starts, c)
			default:
				rest = append(rest, i)
			}
		}
		out = append(out, starts...)
		labels := make([]string, len(rest))
		for i, k := range rest {
			labels[i] = cands[k].Label
		}
		for _, m := range fuzzy.Find(prefix, labels) {
			out = append(out, cands[rest[m.Index]])
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

const maxRows = 10

// Model is the popup: candidates and the selected one.
type Model struct {
	items  []Candidate
	sel    int
	offset int
}

// New opens a popup on items with the first one selected.
func New(items []Candidate) *Model { return &Model{items: items} }

// NewUnselected opens a popup on items with none selected: until Next or
// Prev picks one there is nothing to accept.
func NewUnselected(items []Candidate) *Model { return &Model{items: items, sel: -1} }

func (m *Model) Len() int { return len(m.items) }

// Labels lists the candidates as shown.
func (m *Model) Labels() []string {
	out := make([]string, len(m.items))
	for i, c := range m.items {
		out[i] = c.Label
	}
	return out
}

// Next selects the next candidate, wrapping around.
func (m *Model) Next() {
	if n := len(m.items); n > 0 {
		m.sel = (m.sel + 1) % n
	}
}

// Prev selects the previous candidate, wrapping around.
func (m *Model) Prev() {
	if n := len(m.items); n > 0 {
		if m.sel <= 0 {
			m.sel = n - 1
		} else {
			m.sel--
		}
	}
}

// Selected returns the selected candidate, if any.
func (m *Model) Selected() (Candidate, bool) {
	if m.sel < 0 || m.sel >= len(m.items) {
		return Candidate{}, false
	}
	return m.items[m.sel], true
}

// Items are the popup's candidates.
func (m *Model) Items() []Candidate { return m.items }

// Chosen is the candidate to insert: the selected one, else the first.
func (m *Model) Chosen() Candidate {
	if c, ok := m.Selected(); ok {
		return c
	}
	if len(m.items) == 0 {
		return Candidate{}
	}
	return m.items[0]
}

func pad(s string, w int) string { return s + strings.Repeat(" ", max(w-ansi.StringWidth(s), 0)) }

// View draws the popup with a border, at most maxW wide and maxH tall.
func (m *Model) View(maxW, maxH int) string {
	h := min(len(m.items), maxRows, max(maxH-2, 1))
	if m.sel >= 0 {
		if m.sel < m.offset {
			m.offset = m.sel
		}
		if m.sel >= m.offset+h {
			m.offset = m.sel - h + 1
		}
	}
	labelW, detailW := 0, 0
	for _, c := range m.items {
		labelW = max(labelW, ansi.StringWidth(textutil.Sanitize(c.Label)))
		detailW = max(detailW, ansi.StringWidth(textutil.Sanitize(c.Detail)))
	}
	inner := 6 + labelW
	if detailW > 0 {
		inner += 2 + detailW
	}
	w := max(min(inner+2, maxW), 2)
	lines := make([]string, 0, h)
	for i := m.offset; i < len(m.items) && i < m.offset+h; i++ {
		c := m.items[i]
		tag := fmt.Sprintf("%-5s ", tags[c.Kind])
		label := pad(textutil.Sanitize(c.Label), labelW)
		detail := ""
		if c.Detail != "" {
			detail = "  " + textutil.Sanitize(c.Detail)
		}
		if i == m.sel {
			lines = append(lines, theme.Selected.Render(textutil.Fit(tag+label+detail, w-2)))
			continue
		}
		lines = append(lines, textutil.Fit(theme.Muted.Render(tag)+label+theme.Muted.Render(detail), w-2))
	}
	return theme.Box("", strings.Join(lines, "\n"), w, h+2, true)
}
