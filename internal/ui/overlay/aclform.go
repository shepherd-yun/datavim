package overlay

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// ACLFormMsg carries the ACLs the new-ACL form describes: one per operation.
type ACLFormMsg struct{ ACLs []kafka.ACL }

const (
	afPrincipal = iota
	afHost
	afResource
	afName
	afPattern
	afOperations
	afPermission
)

// clusterName is the one name a cluster ACL has.
const clusterName = "kafka-cluster"

var (
	aclResources   = []string{"topic", "group", "cluster", "transactional_id"}
	aclPatterns    = []string{"literal", "prefixed"}
	aclPermissions = []string{"allow", "deny"}
)

// ACLForm is the new-ACL form.
type ACLForm struct {
	keys   *keymap.Keymap
	fields []formField
	cur    int
	err    string
}

// NewACLForm builds a form for an ACL that allows a User to read, from
// any host.
func NewACLForm(keys *keymap.Keymap) *ACLForm {
	return &ACLForm{keys: keys, fields: []formField{
		afPrincipal:  {label: "Principal", input: newField("User:")},
		afHost:       {label: "Host", input: newField("*")},
		afResource:   {label: "Resource", choices: aclResources},
		afName:       {label: "Name"},
		afPattern:    {label: "Pattern", choices: aclPatterns},
		afOperations: {label: "Operations", input: newField("read")},
		afPermission: {label: "Permission", choices: aclPermissions},
	}}
}

func (f *ACLForm) cluster() bool { return aclResources[f.fields[afResource].choice] == "cluster" }

func (f *ACLForm) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
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
			if f.cur != afName || !f.cluster() {
				fd.input.handle(msg)
			}
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
func (f *ACLForm) Paste(text string) {
	if fd := &f.fields[f.cur]; fd.choices == nil && (f.cur != afName || !f.cluster()) {
		fd.input.paste(text)
	}
}

func (f *ACLForm) text(i int) string { return strings.TrimSpace(f.fields[i].input.String()) }

func (f *ACLForm) acls() ([]kafka.ACL, error) {
	base := kafka.ACL{
		Principal: f.text(afPrincipal), Host: f.text(afHost),
		ResourceType: aclResources[f.fields[afResource].choice], ResourceName: f.text(afName),
		Pattern: aclPatterns[f.fields[afPattern].choice], Permission: aclPermissions[f.fields[afPermission].choice],
	}
	if f.cluster() {
		base.ResourceName = clusterName
	}
	probe := base // the principal and host first, in the order of the form
	probe.ResourceName, probe.Operation = "x", "all"
	if err := probe.Validate(); err != nil {
		return nil, err
	}
	if base.ResourceName == "" {
		return nil, errors.New("name is required")
	}
	var out []kafka.ACL
	for op := range strings.SplitSeq(f.text(afOperations), ",") {
		a := base
		a.Operation = strings.ToLower(strings.TrimSpace(op))
		if a.Operation == "" {
			continue
		}
		if err := a.Validate(); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, errors.New("operations: name at least one (read, write, describe, all …)")
	}
	return out, nil
}

func (f *ACLForm) submit() (Overlay, tea.Cmd) {
	acls, err := f.acls()
	if err != nil {
		f.err = strings.TrimPrefix(err.Error(), "acl ")
		return f, nil
	}
	return nil, emit(ACLFormMsg{ACLs: acls})
}

func (f *ACLForm) View(width, _ int) string {
	w := max(min(width-4, 70), 36)
	lines := make([]string, 0, len(f.fields)+4)
	for i, fd := range f.fields {
		label := fmt.Sprintf("%-12s", fd.label)
		var v string
		switch {
		case fd.choices != nil:
			v = "‹ " + fd.choices[fd.choice] + " ›"
		case i == afName && f.cluster():
			v = theme.Muted.Render(clusterName)
		case i == f.cur:
			v = fd.input.View(w - 14)
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
		theme.Muted.Render("Operations: read, write, describe, all … (one ACL each) · Host: * is every host"),
		theme.Muted.Render(footer("tab next · enter next/create", popupHint(f.keys, keymap.SaveForm, "create"), "esc cancel")))
	return theme.Box("New ACL", strings.Join(lines, "\n"), w, len(lines)+2, true)
}
