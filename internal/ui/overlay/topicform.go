package overlay

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// TopicFormMsg carries the topic the new-topic form describes.
type TopicFormMsg struct{ Topic kafka.NewTopic }

const (
	tfName = iota
	tfPartitions
	tfReplication
	tfRetention
	tfCleanup
	tfMinISR
	tfOther
)

// cleanupChoices are cleanup.policy; default leaves it to the broker.
var cleanupChoices = []string{"default", "delete", "compact", "compact,delete"}

// TopicForm is the new-topic form.
type TopicForm struct {
	keys   *keymap.Keymap
	fields []formField
	cur    int
	err    string
}

// NewTopicForm builds an empty form: one partition, the broker's default
// replication.
func NewTopicForm(keys *keymap.Keymap) *TopicForm {
	return &TopicForm{keys: keys, fields: []formField{
		tfName:        {label: "Name"},
		tfPartitions:  {label: "Partitions", input: newField("1")},
		tfReplication: {label: "Replication"},
		tfRetention:   {label: "retention.ms"},
		tfCleanup:     {label: "cleanup.policy", choices: cleanupChoices},
		tfMinISR:      {label: "min.insync.replicas"},
		tfOther:       {label: "Other configs"},
	}}
}

func (f *TopicForm) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
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

// Paste types text into the current field; the choice ignores it.
func (f *TopicForm) Paste(text string) {
	if fd := &f.fields[f.cur]; fd.choices == nil {
		fd.input.paste(text)
	}
}

func (f *TopicForm) text(i int) string { return strings.TrimSpace(f.fields[i].input.String()) }

// validTopicName is Kafka's rule: [a-zA-Z0-9._-], at most 249, not . or ..
func validTopicName(name string) error {
	switch {
	case name == "":
		return errors.New("name is required")
	case name == "." || name == "..":
		return errors.New(`"." and ".." are not topic names`)
	case len(name) > 249:
		return errors.New("the name has 249 characters at most")
	}
	for _, r := range name {
		if !topicChar(r) {
			return errors.New("the name has letters, digits, . _ and - only")
		}
	}
	return nil
}

func topicChar(r rune) bool {
	return r == '.' || r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

func (f *TopicForm) topic() (kafka.NewTopic, error) {
	t := kafka.NewTopic{Name: f.text(tfName), Replication: -1}
	if err := validTopicName(t.Name); err != nil {
		return t, err
	}
	n, err := strconv.ParseInt(f.text(tfPartitions), 10, 32)
	if err != nil || n < 1 {
		return t, errors.New("partitions must be a number ≥ 1")
	}
	t.Partitions = int32(n)
	if s := f.text(tfReplication); s != "" {
		r, err := strconv.ParseInt(s, 10, 16)
		if err != nil || r < 1 {
			return t, errors.New("replication must be a number ≥ 1, or empty for the broker default")
		}
		t.Replication = int16(r)
	}
	cfg := map[string]string{}
	if s := f.text(tfRetention); s != "" {
		cfg["retention.ms"] = s
	}
	if c := f.fields[tfCleanup]; c.choice > 0 {
		cfg["cleanup.policy"] = c.choices[c.choice]
	}
	if s := f.text(tfMinISR); s != "" {
		cfg["min.insync.replicas"] = s
	}
	if s := f.text(tfOther); s != "" {
		for part := range strings.SplitSeq(s, ",") {
			k, v, ok := strings.Cut(part, "=")
			if k = strings.TrimSpace(k); !ok || k == "" {
				return t, fmt.Errorf("other configs: write name=value, … (got %q)", strings.TrimSpace(part))
			}
			cfg[k] = strings.TrimSpace(v)
		}
	}
	if len(cfg) > 0 {
		t.Configs = cfg
	}
	return t, nil
}

func (f *TopicForm) submit() (Overlay, tea.Cmd) {
	t, err := f.topic()
	if err != nil {
		f.err = err.Error()
		return f, nil
	}
	return nil, emit(TopicFormMsg{Topic: t})
}

func (f *TopicForm) View(width, _ int) string {
	w := max(min(width-4, 70), 36)
	lines := make([]string, 0, len(f.fields)+4)
	for i, fd := range f.fields {
		label := fmt.Sprintf("%-21s", fd.label)
		var v string
		switch {
		case fd.choices != nil:
			v = "‹ " + fd.choices[fd.choice] + " ›"
		case i == f.cur:
			v = fd.input.View(w - 23)
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
		theme.Muted.Render("Replication: empty is the broker default · Other configs: k=v, k=v"),
		theme.Muted.Render(footer("tab next · enter next/create", popupHint(f.keys, keymap.SaveForm, "create"), "esc cancel")))
	return theme.Box("New topic", strings.Join(lines, "\n"), w, len(lines)+2, true)
}
