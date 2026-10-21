package overlay

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// ReindexFormMsg carries the reindex the form describes; Query is in the
// filter prompt's syntax, empty for every document.
type ReindexFormMsg struct{ Dest, Query, OpType, Conflicts string }

const (
	rfDest = iota
	rfQuery
	rfOpType
	rfConflicts
)

var (
	reindexOpTypes   = []string{"index", "create"}
	reindexConflicts = []string{"abort", "proceed"}
)

// ReindexForm is the reindex form of one source index.
type ReindexForm struct {
	keys   *keymap.Keymap
	source string
	fields []formField
	cur    int
	err    string
}

// NewReindexForm builds the form for source; the destination starts as
// "<source>-reindex".
func NewReindexForm(keys *keymap.Keymap, source string) *ReindexForm {
	return &ReindexForm{keys: keys, source: source, fields: []formField{
		rfDest:      {label: "Destination", input: newField(source + "-reindex")},
		rfQuery:     {label: "Query"},
		rfOpType:    {label: "Op type", choices: reindexOpTypes},
		rfConflicts: {label: "Conflicts", choices: reindexConflicts},
	}}
}

func (f *ReindexForm) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	fd := &f.fields[f.cur]
	if f.keys.Triggers(keymap.Popup, keymap.SaveForm, msg.String()) {
		return f.submit()
	}
	switch key := msg.String(); key {
	case "esc":
		return nil, nil
	case "tab", "down":
		f.cur = (f.cur + 1) % len(f.fields)
	case "shift+tab", "up":
		f.cur = (f.cur + len(f.fields) - 1) % len(f.fields)
	case "enter":
		if f.cur == len(f.fields)-1 {
			return f.submit()
		}
		f.cur++
	default:
		f.err = ""
		if fd.choices == nil {
			fd.input.handle(msg)
			return f, nil
		}
		switch key {
		case "space", "right", "l":
			fd.choice = (fd.choice + 1) % len(fd.choices)
		case "left", "h":
			fd.choice = (fd.choice + len(fd.choices) - 1) % len(fd.choices)
		}
	}
	return f, nil
}

// Paste types text into the current field; choices ignore it.
func (f *ReindexForm) Paste(text string) {
	if fd := &f.fields[f.cur]; fd.choices == nil {
		fd.input.paste(text)
	}
}

func (f *ReindexForm) text(i int) string { return strings.TrimSpace(f.fields[i].input.String()) }

func (f *ReindexForm) reindex() (ReindexFormMsg, error) {
	m := ReindexFormMsg{
		Dest: f.text(rfDest), Query: f.text(rfQuery),
		OpType: reindexOpTypes[f.fields[rfOpType].choice], Conflicts: reindexConflicts[f.fields[rfConflicts].choice],
	}
	if m.Dest == f.source {
		return m, errors.New("the destination is the source; name a new index")
	}
	if err := validIndexName(m.Dest); err != nil {
		return m, fmt.Errorf("destination: %w", err)
	}
	if _, err := esx.SearchQuery(m.Query); err != nil {
		return m, fmt.Errorf("query: %w", err)
	}
	return m, nil
}

func (f *ReindexForm) submit() (Overlay, tea.Cmd) {
	m, err := f.reindex()
	if err != nil {
		f.err = err.Error()
		return f, nil
	}
	return nil, emit(m)
}

func (f *ReindexForm) View(width, _ int) string {
	w := max(min(width-4, 80), 36)
	lines := make([]string, 0, len(f.fields)+5)
	lines = append(lines, theme.Muted.Render("from "+textutil.Sanitize(f.source)), "")
	for i, fd := range f.fields {
		label := fmt.Sprintf("%-13s", fd.label)
		var v string
		switch {
		case fd.choices != nil:
			v = "‹ " + fd.choices[fd.choice] + " ›"
		case i == f.cur:
			v = fd.input.View(w - 15)
		default:
			v = textutil.Sanitize(fd.input.String())
		}
		if i == f.cur {
			label = theme.ActiveTab.Render(label)
		}
		lines = append(lines, label+v)
	}
	lines = append(lines, "")
	if f.err != "" {
		for _, l := range textutil.Wrap(textutil.Sanitize(f.err), w-2) {
			lines = append(lines, theme.Error.Render(l))
		}
	}
	lines = append(lines,
		theme.Muted.Render("Query: like the Documents filter, empty copies every document · create skips documents the destination has"),
		theme.Muted.Render(footer("tab next · ←/→ choose · enter next/reindex", popupHint(f.keys, keymap.SaveForm, "reindex"), "esc cancel")))
	return theme.Box("Reindex", strings.Join(lines, "\n"), w, len(lines)+2, true)
}
