package overlay

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// ConnFormMsg carries a connection from the form.
type ConnFormMsg struct {
	Conn     config.Connection
	Password string // empty when editing means: keep the current password
	Save     bool
	Original string // the edited connection's name; empty for a new one
}

type formField struct {
	label   string
	input   field
	choices []string
	choice  int
	secret  bool
}

const (
	ffName = iota
	ffDriver
	ffHost
	ffPort
	ffUser
	ffPassword
	ffDatabase
	ffSSH
	ffEnv
	ffSave
)

// ConnForm is the <space>n new-connection form.
type ConnForm struct {
	fields   []formField
	cur      int
	err      string
	original string // editing this connection; empty for a new one
}

// NewConnForm builds an empty form.
func NewConnForm() *ConnForm {
	return &ConnForm{fields: []formField{
		ffName:     {label: "Name"},
		ffDriver:   {label: "Driver", choices: []string{"mysql", "postgres"}},
		ffHost:     {label: "Host", input: newField("localhost")},
		ffPort:     {label: "Port"},
		ffUser:     {label: "User"},
		ffPassword: {label: "Password", secret: true},
		ffDatabase: {label: "Database"},
		ffSSH:      {label: "SSH host"},
		ffEnv:      {label: "Env", choices: []string{"dev", "staging", "prod"}},
		ffSave:     {label: "Save password", choices: []string{"yes", "no"}},
	}}
}

// NewEditConnForm builds the form filled in with c, for <e> in the tree.
func NewEditConnForm(c config.Connection) *ConnForm {
	f := NewConnForm()
	f.original = c.Name
	set := func(i int, v string) { f.fields[i].input = newField(v) }
	set(ffName, c.Name)
	set(ffHost, c.Host)
	if c.Port != 0 {
		set(ffPort, strconv.Itoa(c.Port))
	}
	set(ffUser, c.User)
	set(ffDatabase, c.Database)
	set(ffSSH, c.SSH)
	choose := func(i int, v string) {
		for j, ch := range f.fields[i].choices {
			if ch == v {
				f.fields[i].choice = j
			}
		}
	}
	choose(ffDriver, c.Driver)
	choose(ffEnv, string(c.Env))
	return f
}

func (f *ConnForm) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	fd := &f.fields[f.cur]
	switch key := msg.String(); key {
	case "esc":
		return nil, nil
	case "tab", "down":
		f.cur = (f.cur + 1) % len(f.fields)
	case "shift+tab", "up":
		f.cur = (f.cur + len(f.fields) - 1) % len(f.fields)
	case "ctrl+s":
		return f.submit()
	case "enter":
		if f.cur == len(f.fields)-1 {
			return f.submit()
		}
		f.cur++
	default:
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

// Paste types text into the current field; choice fields ignore it.
func (f *ConnForm) Paste(text string) {
	if fd := &f.fields[f.cur]; fd.choices == nil {
		fd.input.paste(text)
	}
}

func (f *ConnForm) text(i int) string { return strings.TrimSpace(f.fields[i].input.String()) }

func (f *ConnForm) value(i int) string { return f.fields[i].choices[f.fields[i].choice] }

func (f *ConnForm) submit() (Overlay, tea.Cmd) {
	port := 0
	if p := f.text(ffPort); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			f.err = fmt.Sprintf("port must be a number between 1 and 65535, got %q", p)
			return f, nil
		}
		port = n
	}
	c, err := config.Normalize(config.Connection{
		Name: f.text(ffName), Driver: f.value(ffDriver), Host: f.text(ffHost), Port: port,
		User: f.text(ffUser), Database: f.text(ffDatabase), SSH: f.text(ffSSH), Env: config.Env(f.value(ffEnv)),
	})
	if err != nil {
		f.err = err.Error()
		return f, nil
	}
	return nil, emit(ConnFormMsg{
		Conn: c, Password: f.fields[ffPassword].input.String(), Save: f.value(ffSave) == "yes", Original: f.original,
	})
}

func (f *ConnForm) View(width, _ int) string {
	w := max(min(width-4, 60), 30)
	lines := make([]string, 0, len(f.fields)+4)
	for i, fd := range f.fields {
		var v string
		switch {
		case fd.choices != nil:
			v = "‹ " + fd.choices[fd.choice] + " ›"
		case fd.secret:
			v = strings.Repeat("•", fd.input.Len())
		default:
			v = textutil.Sanitize(fd.input.String())
		}
		label := fmt.Sprintf("%-14s", fd.label)
		if i == f.cur {
			if fd.choices == nil {
				v += "█"
			}
			label = theme.ActiveTab.Render(label)
		}
		lines = append(lines, label+v)
	}
	lines = append(lines, "")
	if f.err != "" {
		lines = append(lines, theme.Error.Render(textutil.Sanitize(f.err)))
	}
	title := "New connection"
	if f.original != "" {
		title = "Edit connection"
		lines = append(lines, theme.Muted.Render("Password: empty keeps the current one"))
	}
	lines = append(lines, theme.Muted.Render("SSH host: a Host alias from ~/.ssh/config (optional)"))
	lines = append(lines, theme.Muted.Render("tab next · enter next/save · ctrl+s save · esc cancel"))
	return theme.Box(title, strings.Join(lines, "\n"), w, len(lines)+2, true)
}
