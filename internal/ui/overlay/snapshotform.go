package overlay

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// SnapshotFormMsg carries the snapshot the form describes; no Indices
// means all of them.
type SnapshotFormMsg struct {
	Repo, Name string
	Indices    []string
}

// SnapshotForm is the new Elasticsearch snapshot form.
type SnapshotForm struct {
	keys   *keymap.Keymap
	repo   string
	fields []formField
	cur    int
	err    string
}

// NewSnapshotForm builds the form for a snapshot into repo.
func NewSnapshotForm(keys *keymap.Keymap, repo, defaultName string) *SnapshotForm {
	return &SnapshotForm{keys: keys, repo: repo, fields: []formField{
		{label: "Name", input: newField(defaultName)},
		{label: "Indices"},
	}}
}

func (f *SnapshotForm) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
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
		f.fields[f.cur].input.handle(msg)
	}
	return f, nil
}

// Paste types text into the current field.
func (f *SnapshotForm) Paste(text string) { f.fields[f.cur].input.paste(text) }

// validSnapshotName is Elasticsearch's rule for snapshot names.
func validSnapshotName(name string) error {
	switch {
	case name == "":
		return errors.New("name is required")
	case strings.ToLower(name) != name:
		return errors.New("snapshot names are lowercase")
	case strings.ContainsAny(name, ` ,"*\/?<>|#`):
		return errors.New(`snapshot names may not contain spaces or , " * \ / ? < > | #`)
	case strings.HasPrefix(name, "_") || strings.HasPrefix(name, "-"):
		return errors.New("snapshot names may not start with _ or -")
	}
	return nil
}

// splitList splits a comma-separated list, dropping empty items.
func splitList(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (f *SnapshotForm) submit() (Overlay, tea.Cmd) {
	name := strings.TrimSpace(f.fields[0].input.String())
	if err := validSnapshotName(name); err != nil {
		f.err = err.Error()
		return f, nil
	}
	return nil, emit(SnapshotFormMsg{Repo: f.repo, Name: name, Indices: splitList(f.fields[1].input.String())})
}

func (f *SnapshotForm) View(width, _ int) string {
	w := max(min(width-4, 90), 36)
	lines := make([]string, 0, len(f.fields)+4)
	for i, fd := range f.fields {
		label := fmt.Sprintf("%-10s", fd.label)
		v := textutil.Sanitize(fd.input.String())
		if i == f.cur {
			v = fd.input.View(w - 12)
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
		theme.Muted.Render("Indices: comma-separated, * patterns allowed; empty takes every index"),
		theme.Muted.Render(footer("tab next · enter next/create", popupHint(f.keys, keymap.SaveForm, "create"), "esc cancel")))
	return theme.Box("New snapshot in "+textutil.Sanitize(f.repo), strings.Join(lines, "\n"), w, len(lines)+2, true)
}
