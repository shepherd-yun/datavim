package overlay

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// IndexFormMsg carries the index the new-index form describes.
type IndexFormMsg struct{ Index es.NewIndex }

const (
	ifName = iota
	ifShards
	ifReplicas
	ifMappings
	ifSettings
)

// IndexForm is the new Elasticsearch index form.
type IndexForm struct {
	keys   *keymap.Keymap
	fields []formField
	cur    int
	err    string
}

// NewIndexForm builds an empty form: one shard, one replica.
func NewIndexForm(keys *keymap.Keymap) *IndexForm {
	return &IndexForm{keys: keys, fields: []formField{
		ifName:     {label: "Name"},
		ifShards:   {label: "Shards", input: newField("1")},
		ifReplicas: {label: "Replicas", input: newField("1")},
		ifMappings: {label: "Mappings (JSON)"},
		ifSettings: {label: "Settings (JSON)"},
	}}
}

func (f *IndexForm) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
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
func (f *IndexForm) Paste(text string) { f.fields[f.cur].input.paste(text) }

func (f *IndexForm) text(i int) string { return strings.TrimSpace(f.fields[i].input.String()) }

// validIndexName is Elasticsearch's rule for index names.
func validIndexName(name string) error {
	switch {
	case name == "":
		return errors.New("name is required")
	case name == "." || name == "..":
		return errors.New(`"." and ".." are not index names`)
	case strings.ToLower(name) != name:
		return errors.New("index names are lowercase")
	case strings.ContainsAny(name, `\/*?"<>| ,#:`):
		return errors.New(`index names may not contain \ / * ? " < > | , # : or spaces`)
	case strings.HasPrefix(name, "-") || strings.HasPrefix(name, "_") || strings.HasPrefix(name, "+"):
		return errors.New("index names may not start with -, _ or +")
	case len(name) > 255:
		return errors.New("the name has 255 bytes at most")
	}
	return nil
}

func (f *IndexForm) index() (es.NewIndex, error) {
	n := es.NewIndex{Name: f.text(ifName)}
	if err := validIndexName(n.Name); err != nil {
		return n, err
	}
	var err error
	if n.Shards, err = strconv.Atoi(f.text(ifShards)); err != nil || n.Shards < 1 {
		return n, errors.New("shards must be a number ≥ 1")
	}
	if n.Replicas, err = strconv.Atoi(f.text(ifReplicas)); err != nil || n.Replicas < 0 {
		return n, errors.New("replicas must be a number ≥ 0")
	}
	obj := func(i int, what string) (json.RawMessage, error) {
		s := f.text(i)
		if s == "" {
			return nil, nil
		}
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) != nil || m == nil {
			return nil, fmt.Errorf("%s must be a JSON object (or empty)", what)
		}
		return json.RawMessage(s), nil
	}
	if n.Mappings, err = obj(ifMappings, "mappings"); err != nil {
		return n, err
	}
	n.Settings, err = obj(ifSettings, "settings")
	return n, err
}

func (f *IndexForm) submit() (Overlay, tea.Cmd) {
	n, err := f.index()
	if err != nil {
		f.err = err.Error()
		return f, nil
	}
	return nil, emit(IndexFormMsg{Index: n})
}

func (f *IndexForm) View(width, _ int) string {
	w := max(min(width-4, 90), 36)
	lines := make([]string, 0, len(f.fields)+4)
	for i, fd := range f.fields {
		label := fmt.Sprintf("%-17s", fd.label)
		v := textutil.Sanitize(fd.input.String())
		if i == f.cur {
			v = fd.input.View(w - 19)
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
		theme.Muted.Render(`Mappings: {"properties":{…}} · Settings: {"refresh_interval":"5s"} · both may stay empty`),
		theme.Muted.Render(footer("tab next · enter next/create", popupHint(f.keys, keymap.SaveForm, "create"), "esc cancel")))
	return theme.Box("New index", strings.Join(lines, "\n"), w, len(lines)+2, true)
}
