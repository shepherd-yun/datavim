package overlay

import (
	"fmt"
	"maps"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
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
	ffEncrypt // SQL Server only
	ffTrust   // SQL Server only
	ffSSH
	ffEnv
	ffSave
)

// ConnForm is the <space>n new-connection form.
type ConnForm struct {
	keys     *keymap.Keymap
	fields   []formField
	cur      int
	err      string
	original string // editing this connection; empty for a new one
	// kept from the edited connection: the form does not show them
	params   map[string]string
	readOnly *bool
}

// oracleDefaultService is what an empty Service connects to (Oracle
// Database Free's pluggable database).
const oracleDefaultService = "FREEPDB1"

// encryptChoices are go-mssqldb's encrypt values; default leaves it out.
var encryptChoices = []string{"default", "true", "false", "disable"}

// NewConnForm builds an empty form.
func NewConnForm(keys *keymap.Keymap) *ConnForm {
	return &ConnForm{keys: keys, fields: []formField{
		ffName:     {label: "Name"},
		ffDriver:   {label: "Driver", choices: []string{"mysql", "postgres", "sqlserver", "oracle", "redis"}},
		ffHost:     {label: "Host", input: newField("localhost")},
		ffPort:     {label: "Port"},
		ffUser:     {label: "User"},
		ffPassword: {label: "Password", secret: true},
		ffDatabase: {label: "Database"},
		ffEncrypt:  {label: "Encrypt", choices: encryptChoices},
		ffTrust:    {label: "Trust cert", choices: []string{"no", "yes"}},
		ffSSH:      {label: "SSH host"},
		ffEnv:      {label: "Env", choices: []string{"dev", "staging", "prod"}},
		ffSave:     {label: "Save password", choices: []string{"yes", "no"}},
	}}
}

// NewEditConnForm builds the form filled in with c, for <e> in the tree.
func NewEditConnForm(keys *keymap.Keymap, c config.Connection) *ConnForm {
	f := NewConnForm(keys)
	f.original = c.Name
	f.params, f.readOnly = maps.Clone(c.Params), c.ReadOnly
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
	choose(ffEncrypt, strings.ToLower(param(c.Params, "encrypt")))
	if strings.EqualFold(param(c.Params, "TrustServerCertificate"), "true") {
		choose(ffTrust, "yes")
	}
	return f
}

// param looks key up in any case.
func param(p map[string]string, key string) string {
	for k, v := range p {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// shown reports whether field i applies to the chosen driver.
func (f *ConnForm) shown(i int) bool {
	if i == ffEncrypt || i == ffTrust {
		return f.value(ffDriver) == "sqlserver"
	}
	return true
}

// step moves the cursor by dir to the next shown field, wrapping around.
func (f *ConnForm) step(dir int) {
	for {
		f.cur = (f.cur + dir + len(f.fields)) % len(f.fields)
		if f.shown(f.cur) {
			return
		}
	}
}

func (f *ConnForm) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	fd := &f.fields[f.cur]
	if f.keys.Triggers(keymap.Popup, keymap.SaveForm, msg.String()) {
		return f.submit()
	}
	switch key := msg.String(); key {
	case "esc":
		return nil, nil
	case "tab", "down":
		f.step(1)
	case "shift+tab", "up":
		f.step(-1)
	case "enter":
		if f.cur == len(f.fields)-1 {
			return f.submit()
		}
		f.step(1)
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
		ReadOnly: f.readOnly, Params: f.submitParams(),
	})
	if err != nil {
		f.err = err.Error()
		return f, nil
	}
	return nil, emit(ConnFormMsg{
		Conn: c, Password: f.fields[ffPassword].input.String(), Save: f.value(ffSave) == "yes", Original: f.original,
	})
}

// submitParams are the edited connection's params with the SQL Server
// fields applied: default and no remove their key.
func (f *ConnForm) submitParams() map[string]string {
	p := maps.Clone(f.params)
	if p == nil {
		p = map[string]string{}
	}
	// go-mssqldb reads keys in any case and refuses one given twice
	maps.DeleteFunc(p, func(k, _ string) bool {
		return strings.EqualFold(k, "encrypt") || strings.EqualFold(k, "TrustServerCertificate")
	})
	if f.value(ffDriver) == "sqlserver" {
		if e := f.value(ffEncrypt); e != "default" {
			p["encrypt"] = e
		}
		if f.value(ffTrust) == "yes" {
			p["TrustServerCertificate"] = "true"
		}
	}
	if len(p) == 0 {
		return nil
	}
	return p
}

func (f *ConnForm) View(width, _ int) string {
	w := max(min(width-4, 60), 30)
	lines := make([]string, 0, len(f.fields)+4)
	for i, fd := range f.fields {
		if !f.shown(i) {
			continue
		}
		var v string
		name := fd.label
		oracleService := i == ffDatabase && f.value(ffDriver) == "oracle"
		if oracleService {
			name = "Service" // Oracle connects to a service; the field keeps the name
		}
		if i == ffDatabase && f.value(ffDriver) == "redis" {
			name = "DB" // a db number; empty is 0
		}
		label := fmt.Sprintf("%-14s", name)
		switch {
		case oracleService && i != f.cur && fd.input.Len() == 0:
			v = theme.Muted.Render(oracleDefaultService)
		case fd.choices != nil:
			v = "‹ " + fd.choices[fd.choice] + " ›"
		case i == f.cur && fd.secret:
			v = fd.input.ViewMasked(w-16, '•')
		case i == f.cur:
			v = fd.input.View(w - 16)
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
	title := "New connection"
	if f.original != "" {
		title = "Edit connection"
		lines = append(lines, theme.Muted.Render("Password: empty keeps the current one"))
	}
	lines = append(lines, theme.Muted.Render("SSH host: a Host alias from ~/.ssh/config (optional)"))
	lines = append(lines, theme.Muted.Render(footer("tab next · enter next/save", popupHint(f.keys, keymap.SaveForm, "save"), "esc cancel")))
	return theme.Box(title, strings.Join(lines, "\n"), w, len(lines)+2, true)
}
