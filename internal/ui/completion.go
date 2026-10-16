package ui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/meta"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/complete"
)

// maxCandidates bounds the completion popup.
const maxCandidates = 50

// defaultNamespace is where a console resolves unqualified table names:
// its database (MySQL; the configured one when the console has none), or
// its schema, else public (PostgreSQL).
func defaultNamespace(cfg config.Connection, key meta.Key, ns db.Namespace) db.Namespace {
	if cfg.Driver == "postgres" {
		schema := ns.Schema
		if schema == "" {
			schema = "public"
		}
		return db.Namespace{Database: key.Database, Schema: schema}
	}
	database := ns.Database
	if database == "" {
		database = cfg.Database
	}
	return db.Namespace{Database: database}
}

// popupKey handles the completion keys in INSERT mode; used reports that
// the key was consumed.
func (t *consoleTab) popupKey(key string) (cmd tea.Cmd, used bool) {
	if t.popup == nil {
		if key == "ctrl+n" || key == "ctrl+p" {
			t.suggest(true)
			return nil, true
		}
		return nil, false
	}
	switch key {
	case "down", "ctrl+n", "ctrl+j", "tab":
		t.popup.Next()
	case "up", "ctrl+p", "ctrl+k", "shift+tab":
		t.popup.Prev()
	case "enter", "ctrl+y":
		if _, ok := t.popup.Selected(); !ok { // a popup opened by a space
			t.popup = nil
			return nil, key == "ctrl+y" // enter types its newline
		}
		return t.acceptCompletion(), true
	case "esc": // closes the popup; a second esc leaves INSERT mode
		t.popup = nil
	default:
		return nil, false
	}
	return nil, true
}

// suggest opens (or refreshes, or closes) the popup for the word at the
// cursor. Without force it opens only once a word is started or right
// after "qualifier.".
func (t *consoleTab) suggest(force bool) {
	t.popup = nil
	c := sqlx.Complete(t.editor.Value(), t.editor.Offset(), t.dialect)
	if c.Kind == sqlx.CompleteNone {
		return
	}
	if !force && c.Prefix == "" && c.Qualifier == "" {
		return
	}
	ranked := complete.Rank(c.Prefix, t.candidates(c), maxCandidates)
	if len(ranked) == 0 || !force && len(ranked) == 1 && strings.EqualFold(ranked[0].Label, c.Prefix) {
		return // nothing to offer, or the word is already complete
	}
	t.popup, t.popStart = complete.New(ranked), c.Start
}

func (t *consoleTab) candidates(c sqlx.Completion) []complete.Candidate {
	// The words that usually come next replace the keywords once the
	// prefix starts one of them: "from users or" is completed to ORDER BY,
	// not OR.
	var words []complete.Candidate
	if c.Qualifier == "" && c.Prefix != "" {
		nx := sqlx.Following(t.editor.Value(), c.Start, t.dialect)
		words = wordCandidates(nx.Words, strings.ToUpper(c.Prefix) != c.Prefix)
		if !slices.ContainsFunc(words, func(w complete.Candidate) bool {
			return strings.HasPrefix(strings.ToLower(w.Label), strings.ToLower(c.Prefix))
		}) {
			words = nil
		}
	}
	snap, _ := t.catalogs.Get(t.metaKey)
	out := words
	switch c.Kind {
	case sqlx.CompleteTable:
		out = append(out, t.tableCandidates(snap, c.Qualifier)...)
	case sqlx.CompleteColumn:
		out = append(out, t.columnCandidates(snap, c)...)
	}
	if c.Qualifier == "" && c.Kind != sqlx.CompleteTable && words == nil {
		out = append(out, keywordCandidates(c.Prefix, t.dialect)...)
	}
	return out
}

// suggestNext opens the popup a typed space asks for: what usually comes
// next in the statement, with nothing selected so that enter still types
// a newline.
func (t *consoleTab) suggestNext() {
	t.popup = nil
	src, off := t.editor.Value(), t.editor.Offset()
	nx := sqlx.Following(src, off, t.dialect)
	out := wordCandidates(nx.Words, nx.Lower)
	if nx.Names {
		snap, _ := t.catalogs.Get(t.metaKey)
		switch c := sqlx.Complete(src, off, t.dialect); c.Kind {
		case sqlx.CompleteTable:
			out = append(out, t.tableCandidates(snap, "")...)
		case sqlx.CompleteColumn:
			out = append(out, t.columnCandidates(snap, c)...)
		}
	}
	if len(out) == 0 {
		return
	}
	t.popup, t.popStart = complete.NewUnselected(out[:min(len(out), maxCandidates)]), off
}

// wordCandidates offers the keywords (and operators) words, in lower case
// when lower is set.
func wordCandidates(words []string, lower bool) []complete.Candidate {
	out := make([]complete.Candidate, len(words))
	for i, w := range words {
		if lower {
			w = strings.ToLower(w)
		}
		out[i] = complete.Candidate{Text: w, Label: w, Kind: complete.Keyword}
	}
	return out
}

// qualified resolves the qualifier written before a table name.
func (t *consoleTab) qualified(q string) db.Namespace {
	switch {
	case q == "":
		return t.defNS
	case t.dialect == sqlx.Postgres:
		return db.Namespace{Database: t.defNS.Database, Schema: q}
	}
	return db.Namespace{Database: q}
}

func (t *consoleTab) tableCandidates(snap *meta.Snapshot, qualifier string) []complete.Candidate {
	if snap == nil {
		return nil
	}
	var out []complete.Candidate
	for _, tb := range snap.Tables(t.qualified(qualifier)) {
		kind := complete.Table
		if tb.Kind == db.TableKindView {
			kind = complete.View
		}
		out = append(out, complete.Candidate{Text: sqlx.QuoteIfNeeded(tb.Name, t.dialect), Label: tb.Name, Kind: kind})
	}
	if qualifier != "" {
		return out
	}
	for _, ns := range snap.Catalog.Namespaces {
		name := ns.Database
		if t.dialect == sqlx.Postgres {
			name = ns.Schema
		}
		out = append(out, complete.Candidate{Text: sqlx.QuoteIfNeeded(name, t.dialect), Label: name, Kind: complete.Namespace})
	}
	return out
}

func (t *consoleTab) columnCandidates(snap *meta.Snapshot, c sqlx.Completion) []complete.Candidate {
	if snap == nil {
		return nil
	}
	uses := c.Tables
	if c.Qualifier != "" {
		uses = nil
		for _, u := range c.Tables {
			if strings.EqualFold(u.Alias, c.Qualifier) || u.Alias == "" && strings.EqualFold(u.Name, c.Qualifier) {
				uses = append(uses, u)
			}
		}
		if len(uses) == 0 {
			uses = []sqlx.TableUse{{Name: c.Qualifier}}
		}
	}
	var out []complete.Candidate
	for _, u := range uses {
		tb, ok := snap.FindTable(t.qualified(u.Qualifier), u.Name)
		if !ok {
			continue
		}
		for _, col := range snap.Columns(db.TableRef{Namespace: tb.Namespace, Name: tb.Name}) {
			detail := col.Type
			if len(uses) > 1 {
				detail = tb.Name + " · " + col.Type
			}
			out = append(out, complete.Candidate{
				Text: sqlx.QuoteIfNeeded(col.Name, t.dialect), Label: col.Name, Kind: complete.Column, Detail: detail,
			})
		}
	}
	return out
}

// keywordCandidates offers keywords in the case the user is typing.
func keywordCandidates(prefix string, d sqlx.Dialect) []complete.Candidate {
	lower := strings.ToUpper(prefix) != prefix
	kws := sqlx.Keywords(d)
	out := make([]complete.Candidate, len(kws))
	for i, kw := range kws {
		if lower {
			kw = strings.ToLower(kw)
		}
		out[i] = complete.Candidate{Text: kw, Label: kw, Kind: complete.Keyword}
	}
	return out
}

func (t *consoleTab) acceptCompletion() tea.Cmd {
	c := t.popup.Chosen()
	t.popup = nil
	t.editor.ReplaceBeforeCursor(t.popStart, c.Text)
	return t.scheduleDraft()
}

// drawPopup draws the completion popup at the cursor: below it when it
// fits, otherwise above.
func (t *consoleTab) drawPopup(view string) string {
	if t.popup == nil || !t.CapturesInput() {
		return view
	}
	x, y := t.editor.CursorPos()
	x, y = x+1, y+1 // inside the editor's frame
	box := t.popup.View(min(t.width, 60), min(t.height, 12))
	w, h := lipgloss.Width(box), lipgloss.Height(box)
	top := y + 1
	if top+h > t.height+2 {
		top = max(y-h, 0)
	}
	return overlay.PlaceAt(view, box, min(x, max(t.width+2-w, 0)), top)
}
