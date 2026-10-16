package overlay

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// SearchItem is one row of the global search.
type SearchItem struct {
	Kind  string // table, view, column, routine, schema, database
	Label string
	Where string
}

// SearchScope is what the search looks through.
type SearchScope int

const (
	ScopeObjects  SearchScope = iota // the catalogs: tables, columns, …
	ScopeConsoles                    // the text of the open consoles
	ScopeHistory                     // the SQL history
	scopeCount
)

func (s SearchScope) String() string { return [...]string{"objects", "consoles", "history"}[s] }

// SearchQueryMsg asks for the results of Text in Scope; the reply carries Gen.
type SearchQueryMsg struct {
	Text  string
	Gen   int
	Scope SearchScope
}

// SearchPickMsg reports the chosen row; Structure is set for the
// popup's structure key (ctrl+o).
type SearchPickMsg struct {
	Index     int
	Structure bool
}

// Search is the <space><space> popup. The root model computes the results
// (asynchronously) and hands them back with SetResults. The scope keys
// switch between the catalogs, the open consoles and the SQL history.
type Search struct {
	keys     *keymap.Keymap
	query    field
	gen      int // generation of the current query
	answered int // generation of the results shown
	items    []SearchItem
	cur      int
	offset   int
	note     string
	conn     string // the connection a leading @ narrows the search to
	scope    SearchScope
}

// NewSearch opens an empty search of the catalogs; note explains missing
// results and conn names the connection a leading @ narrows to ("" for
// none).
func NewSearch(keys *keymap.Keymap, note, conn string) *Search {
	return &Search{keys: keys, note: note, conn: conn}
}

// Scope is what the search looks through.
func (s *Search) Scope() SearchScope { return s.scope }

func (s *Search) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	key := msg.String()
	if step := s.scopeStep(key); step != 0 {
		s.scope = (s.scope + step + scopeCount) % scopeCount
		return s, emit(s.Requery())
	}
	structure := s.keys.Triggers(keymap.Popup, keymap.OpenFullStructure, key)
	if structure && s.scope != ScopeObjects {
		return s, nil
	}
	if key == "enter" || structure {
		if len(s.items) == 0 {
			return s, nil
		}
		return nil, emit(SearchPickMsg{Index: s.cur, Structure: structure})
	}
	switch msg.String() {
	case "esc":
		return nil, nil
	case "down", "ctrl+n", "ctrl+j":
		s.cur = textutil.Clamp(s.cur+1, 0, len(s.items)-1)
	case "up", "ctrl+p", "ctrl+k":
		s.cur = textutil.Clamp(s.cur-1, 0, len(s.items)-1)
	default:
		before := s.query.String()
		s.query.handle(msg)
		if s.query.String() != before {
			return s, emit(s.Requery())
		}
	}
	return s, nil
}

func (s *Search) scopeStep(key string) SearchScope {
	switch {
	case s.keys.Triggers(keymap.Popup, keymap.NextSearchScope, key):
		return 1
	case s.keys.Triggers(keymap.Popup, keymap.PrevSearchScope, key):
		return -1
	}
	return 0
}

// Requery starts a new generation for the current text and scope.
func (s *Search) Requery() SearchQueryMsg {
	s.gen++
	return SearchQueryMsg{Text: s.query.String(), Gen: s.gen, Scope: s.scope}
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
	listH := h - 6 // border, scopes, query, status, footer
	if s.cur < s.offset {
		s.offset = s.cur
	}
	if s.cur >= s.offset+listH {
		s.offset = s.cur - listH + 1
	}
	scopes := make([]string, scopeCount)
	for sc := range scopeCount {
		scopes[sc] = theme.Muted.Render(sc.String())
		if sc == s.scope {
			scopes[sc] = theme.Selected.Render(sc.String())
		}
	}
	lines := []string{
		textutil.Fit(strings.Join(scopes, "  "), w-2),
		"> " + s.query.View(w-4),
		theme.Muted.Render(textutil.Fit(s.status(), w-2)),
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
	structure := ""
	if s.scope == ScopeObjects {
		structure = popupHint(s.keys, keymap.OpenFullStructure, "structure")
	}
	foot := footer(s.note, "enter open", structure, popupHint(s.keys, keymap.NextSearchScope, "next scope"), "↑/↓ move · esc close")
	body := textutil.PadLines(strings.Join(lines, "\n"), h-3) + "\n" +
		theme.Muted.Render(textutil.Fit(textutil.Sanitize(foot), w-2))
	return theme.Box("Search", body, w, h, true)
}

func (s *Search) status() string {
	empty := s.query.Len() == 0
	switch {
	case empty && s.scope != ScopeHistory:
		hint := "tables, views, columns, routines, schemas, databases"
		if s.scope == ScopeConsoles {
			hint = "lines of the open consoles"
		}
		if s.conn != "" {
			hint += " · start with @ to search only " + s.conn
		}
		return hint
	case s.answered != s.gen:
		return "searching…"
	case len(s.items) == 0:
		return "(no matches)"
	case empty:
		return fmt.Sprintf("%d recent statements", len(s.items))
	}
	return fmt.Sprintf("%d matches", len(s.items))
}
