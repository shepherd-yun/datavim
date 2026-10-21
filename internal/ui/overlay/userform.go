package overlay

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// UserFormMsg is a new user. The password goes no further than the
// request that creates the user.
type UserFormMsg struct{ Name, Password, Roles, BackendRoles, FullName, Email string }

// MinPassword is the shortest password Elasticsearch takes.
const MinPassword = 6

const (
	ufName = iota
	ufPassword
	ufAgain
	ufRoles
	ufExtra1 // ES: full name; OpenSearch: backend roles
	ufExtra2 // ES: email
)

// UserForm is the new-user form of a cluster's Users section.
type UserForm struct {
	keys   *keymap.Keymap
	api    es.SecurityAPI
	fields []formField
	cur    int
	err    string
}

// NewUserForm builds the form for api's users.
func NewUserForm(keys *keymap.Keymap, api es.SecurityAPI) *UserForm {
	f := &UserForm{keys: keys, api: api, fields: []formField{
		ufName:     {label: "Username"},
		ufPassword: {label: "Password", secret: true},
		ufAgain:    {label: "Confirm", secret: true},
		ufRoles:    {label: "Roles"},
	}}
	if api == es.SecurityOpenSearch {
		f.fields = append(f.fields, formField{label: "Backend roles"})
	} else {
		f.fields = append(f.fields, formField{label: "Full name"}, formField{label: "Email"})
	}
	return f
}

func (f *UserForm) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
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
func (f *UserForm) Paste(text string) { f.fields[f.cur].input.paste(text) }

func (f *UserForm) text(i int) string {
	if i >= len(f.fields) {
		return ""
	}
	return strings.TrimSpace(f.fields[i].input.String())
}

// CheckPassword is the rule a new password must follow.
func CheckPassword(pw, again string) error {
	switch {
	case len(pw) < MinPassword:
		return fmt.Errorf("the password needs at least %d characters", MinPassword)
	case pw != again:
		return errors.New("passwords differ")
	}
	return nil
}

func (f *UserForm) user() (UserFormMsg, error) {
	m := UserFormMsg{Name: f.text(ufName), Password: f.fields[ufPassword].input.String(), Roles: f.text(ufRoles)}
	if f.api == es.SecurityOpenSearch {
		m.BackendRoles = f.text(ufExtra1)
	} else {
		m.FullName, m.Email = f.text(ufExtra1), f.text(ufExtra2)
	}
	switch {
	case m.Name == "":
		return m, errors.New("username is required")
	case strings.ContainsAny(m.Name, " \t"):
		return m, errors.New("usernames may not contain spaces")
	}
	return m, CheckPassword(m.Password, f.fields[ufAgain].input.String())
}

func (f *UserForm) submit() (Overlay, tea.Cmd) {
	m, err := f.user()
	if err != nil {
		f.err = err.Error()
		return f, nil
	}
	return nil, emit(m)
}

func (f *UserForm) View(width, _ int) string {
	w := max(min(width-4, 80), 36)
	lines := make([]string, 0, len(f.fields)+4)
	for i, fd := range f.fields {
		label := fmt.Sprintf("%-15s", fd.label)
		var v string
		switch {
		case i == f.cur && fd.secret:
			v = fd.input.ViewMasked(w-17, '•')
		case i == f.cur:
			v = fd.input.View(w - 17)
		case fd.secret:
			v = strings.Repeat("•", fd.input.Len())
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
		lines = append(lines, theme.Error.Render(textutil.Sanitize(f.err)))
	}
	lines = append(lines,
		theme.Muted.Render("Roles: comma-separated"),
		theme.Muted.Render(footer("tab next · enter next/create", popupHint(f.keys, keymap.SaveForm, "create"), "esc cancel")))
	return theme.Box("New user", strings.Join(lines, "\n"), w, len(lines)+2, true)
}
