package overlay

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// RestoreFormMsg is how a snapshot is to be restored.
type RestoreFormMsg struct {
	Indices                          string // empty: all the snapshot's
	RenamePattern, RenameReplacement string
	IncludeAliases                   bool
}

const (
	rsIndices = iota
	rsPattern
	rsReplacement
	rsAliases
)

// RestoreForm asks what to restore from a snapshot and under which names.
type RestoreForm struct {
	keys     *keymap.Keymap
	snapshot string
	indices  []string // the snapshot's
	fields   []formField
	cur      int
	err      string
}

// NewRestoreForm restores everything under "restored-<name>" by default,
// without aliases: restoring them takes them from the live indices.
func NewRestoreForm(keys *keymap.Keymap, snapshot string, indices []string) *RestoreForm {
	return &RestoreForm{keys: keys, snapshot: snapshot, indices: indices, fields: []formField{
		rsIndices:     {label: "Indices"},
		rsPattern:     {label: "Rename pattern", input: newField("(.+)")},
		rsReplacement: {label: "Rename to", input: newField("restored-$1")},
		rsAliases:     {label: "Aliases", choices: []string{"no", "yes"}},
	}}
}

func (f *RestoreForm) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
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
func (f *RestoreForm) Paste(text string) {
	if fd := &f.fields[f.cur]; fd.choices == nil {
		fd.input.paste(text)
	}
}

func (f *RestoreForm) text(i int) string { return strings.TrimSpace(f.fields[i].input.String()) }

func (f *RestoreForm) msg() RestoreFormMsg {
	m := RestoreFormMsg{
		Indices: f.text(rsIndices), RenamePattern: f.text(rsPattern), RenameReplacement: f.text(rsReplacement),
		IncludeAliases: f.fields[rsAliases].choice == 1,
	}
	if m.RenamePattern == "" {
		m.RenameReplacement = ""
	}
	return m
}

// targets are the picked indices and the names they restore as.
func (f *RestoreForm) targets() ([]string, []string, error) {
	m := f.msg()
	src := esx.SelectIndices(f.indices, m.Indices)
	dst, err := esx.RestoreTargets(src, m.RenamePattern, m.RenameReplacement)
	return src, dst, err
}

func (f *RestoreForm) submit() (Overlay, tea.Cmd) {
	if _, _, err := f.targets(); err != nil {
		f.err = err.Error()
		return f, nil
	}
	return nil, emit(f.msg())
}

func (f *RestoreForm) View(width, _ int) string {
	w := max(min(width-4, 90), 40)
	lines := make([]string, 0, len(f.fields)+10)
	for i, fd := range f.fields {
		label := fmt.Sprintf("%-16s", fd.label)
		var v string
		switch {
		case fd.choices != nil:
			v = "‹ " + fd.choices[fd.choice] + " ›"
		case i == f.cur:
			v = fd.input.View(w - 18)
		default:
			v = textutil.Sanitize(fd.input.String())
		}
		if i == f.cur {
			label = theme.ActiveTab.Render(label)
		}
		lines = append(lines, label+v)
	}
	lines = append(lines, "")
	src, dst, err := f.targets()
	switch {
	case err != nil:
		lines = append(lines, theme.Error.Render(textutil.Sanitize(err.Error())))
	case len(src) == 0:
		lines = append(lines, theme.Error.Render("no index of the snapshot matches"))
	default:
		for i := range min(len(src), 3) {
			lines = append(lines, textutil.Fit(textutil.Sanitize("  "+src[i]+" → "+dst[i]), w-2))
		}
		if n := len(src) - 3; n > 0 {
			lines = append(lines, theme.Muted.Render(fmt.Sprintf("  … and %d more", n)))
		}
	}
	if f.err != "" {
		for _, l := range textutil.Wrap(textutil.Sanitize(f.err), w-2) {
			lines = append(lines, theme.Error.Render(l))
		}
	}
	lines = append(lines, "",
		theme.Muted.Render("Indices: names and * patterns, empty for all · empty pattern keeps the names · never the global state"),
		theme.Muted.Render(footer("tab next · ←/→ choose · enter next/restore", popupHint(f.keys, keymap.SaveForm, "restore"), "esc cancel")))
	return theme.Box("Restore "+textutil.Sanitize(f.snapshot), strings.Join(lines, "\n"), w, len(lines)+2, true)
}
