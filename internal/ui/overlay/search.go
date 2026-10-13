package overlay

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// SearchItem is one row of the global search.
type SearchItem struct {
	Kind  string // table, view, column, routine, schema, database
	Label string
	Where string
}

// SearchQueryMsg asks for the results of Text; the reply carries Gen.
type SearchQueryMsg struct {
	Text string
	Gen  int
}

// SearchPickMsg reports the chosen row; Structure is set for ctrl+s.
type SearchPickMsg struct {
	Index     int
	Structure bool
}

// Search is the <space><space> popup. The root model computes the results
// (asynchronously) and hands them back with SetResults.
type Search struct {
	query    field
	gen      int // generation of the current query
	answered int // generation of the results shown
	items    []SearchItem
	cur      int
	offset   int
	note     string
}

// NewSearch opens an empty search; note explains missing results.
func NewSearch(note string) *Search { return &Search{note: note} }

func (s *Search) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, nil
	case "enter", "ctrl+s":
		if len(s.items) == 0 {
			return s, nil
		}
		return nil, emit(SearchPickMsg{Index: s.cur, Structure: msg.String() == "ctrl+s"})
	case "down", "ctrl+n", "ctrl+j":
		s.cur = textutil.Clamp(s.cur+1, 0, len(s.items)-1)
	case "up", "ctrl+p", "ctrl+k":
		s.cur = textutil.Clamp(s.cur-1, 0, len(s.items)-1)
	default:
		before := s.query.String()
		s.query.handle(msg)
		if s.query.String() != before {
			text, gen := s.Requery()
			return s, emit(SearchQueryMsg{Text: text, Gen: gen})
		}
	}
	return s, nil
}

// Requery starts a new generation for the current text and returns both.
func (s *Search) Requery() (string, int) {
	s.gen++
	return s.query.String(), s.gen
}

// SetResults shows the results of generation gen; it reports false (and
// changes nothing) for results of an older query.
func (s *Search) SetResults(gen int, items []SearchItem) bool {
	if gen != s.gen {
		return false
	}
	s.items, s.answered, s.cur, s.offset = items, gen, 0, 0
	return true
}

// SetNote replaces the footer note.
func (s *Search) SetNote(note string) { s.note = note }

// Paste appends text to the query; the caller then runs Requery.
func (s *Search) Paste(text string) { s.query.paste(text) }

func (s *Search) View(width, height int) string {
	w := max(min(width-4, 110), 30)
	h := max(min(height-2, 24), 7)
	listH := h - 5 // border, query, status, footer
	if s.cur < s.offset {
		s.offset = s.cur
	}
	if s.cur >= s.offset+listH {
		s.offset = s.cur - listH + 1
	}
	var status string
	switch {
	case s.query.Len() == 0:
		status = "tables, views, columns, routines, schemas, databases · start with @ to search every connection"
	case s.answered != s.gen:
		status = "searching…"
	case len(s.items) == 0:
		status = "(no matches)"
	default:
		status = fmt.Sprintf("%d matches", len(s.items))
	}
	lines := []string{
		"> " + textutil.Sanitize(s.query.String()) + "█",
		theme.Muted.Render(textutil.Fit(status, w-2)),
	}
	for i := s.offset; i < len(s.items) && i < s.offset+listH; i++ {
		it := s.items[i]
		tag := fmt.Sprintf("%-9s", it.Kind)
		label, where := textutil.Sanitize(it.Label), textutil.Sanitize(it.Where)
		if i == s.cur {
			lines = append(lines, theme.Selected.Render(textutil.Fit(tag+label+"  "+where, w-2)))
			continue
		}
		lines = append(lines, textutil.Fit(theme.KindTag(it.Kind).Render(tag)+label+"  "+theme.Muted.Render(where), w-2))
	}
	footer := "enter open · ctrl+s structure · ↑/↓ move · esc close"
	if s.note != "" {
		footer = s.note + " · " + footer
	}
	body := textutil.PadLines(strings.Join(lines, "\n"), h-3) + "\n" +
		theme.Muted.Render(textutil.Fit(textutil.Sanitize(footer), w-2))
	return theme.Box("Search", body, w, h, true)
}
