package overlay

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
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
	key     string // a param row's params key
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
	ffSASL    // Kafka only
	ffTLS     // Kafka and Elasticsearch
	ffAuth    // Elasticsearch only
	ffSSH
	ffEnv
	ffReadOnly
	ffSave
	ffAddParam // "key=value" adds a param row
	// the param rows follow, one per params key the fields above do not set
)

// ConnForm is the <space>n new-connection form.
type ConnForm struct {
	keys     *keymap.Keymap
	fields   []formField
	cur      int
	err      string
	original string // editing this connection; empty for a new one
	// kept from the edited connection: the keys the fields above set
	params map[string]string
}

// oracleDefaultService is what an empty Service connects to (Oracle
// Database Free's pluggable database).
const oracleDefaultService = "FREEPDB1"

// encryptChoices are go-mssqldb's encrypt values; default leaves it out.
var encryptChoices = []string{"default", "true", "false", "disable"}

// saslChoices are kafka's params.sasl; none leaves it out.
var saslChoices = []string{"none", "plain", "scram-sha-256", "scram-sha-512", "oauth", "aws-msk-iam"}

// kafkaTLSChoices map to params.tls and params.tls_skip_verify.
var kafkaTLSChoices = []string{"off", "on", "on, skip verify"}

// esAuthChoices map to elasticsearch's params.auth: basic leaves it out.
var esAuthChoices = []string{"basic", "api key"}

// readOnlyChoices set readonly; auto leaves it out, so env decides.
var readOnlyChoices = []string{"auto", "yes", "no"}

// NewConnForm builds an empty form.
func NewConnForm(keys *keymap.Keymap) *ConnForm {
	return &ConnForm{keys: keys, fields: []formField{
		ffName:     {label: "Name"},
		ffDriver:   {label: "Driver", choices: []string{"mysql", "postgres", "sqlserver", "oracle", "redis", "kafka", "elasticsearch"}},
		ffHost:     {label: "Host", input: newField("localhost")},
		ffPort:     {label: "Port"},
		ffUser:     {label: "User"},
		ffPassword: {label: "Password", secret: true},
		ffDatabase: {label: "Database"},
		ffEncrypt:  {label: "Encrypt", choices: encryptChoices},
		ffTrust:    {label: "Trust cert", choices: []string{"no", "yes"}},
		ffSASL:     {label: "SASL", choices: saslChoices},
		ffTLS:      {label: "TLS", choices: kafkaTLSChoices},
		ffAuth:     {label: "Auth", choices: esAuthChoices},
		ffSSH:      {label: "SSH host"},
		ffEnv:      {label: "Env", choices: []string{"dev", "staging", "prod"}},
		ffReadOnly: {label: "Read-only", choices: readOnlyChoices},
		ffSave:     {label: "Save password", choices: []string{"yes", "no"}},
		ffAddParam: {label: "Add param"},
	}}
}

// NewEditConnForm builds the form filled in with c, for <e> in the tree.
func NewEditConnForm(keys *keymap.Keymap, c config.Connection) *ConnForm {
	f := NewConnForm(keys)
	f.original = c.Name
	f.params = map[string]string{}
	for _, k := range slices.Sorted(maps.Keys(c.Params)) {
		if formParam(c.Driver, k) {
			f.params[k] = c.Params[k]
		} else {
			f.fields = append(f.fields, formField{label: k, key: k, input: newField(c.Params[k])})
		}
	}
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
	switch {
	case c.ReadOnly == nil:
	case *c.ReadOnly:
		choose(ffReadOnly, "yes")
	default:
		choose(ffReadOnly, "no")
	}
	choose(ffEncrypt, strings.ToLower(param(c.Params, "encrypt")))
	if strings.EqualFold(param(c.Params, "TrustServerCertificate"), "true") {
		choose(ffTrust, "yes")
	}
	if c.Driver == "kafka" {
		choose(ffSASL, c.Params["sasl"])
	}
	if c.Driver == "kafka" || c.Driver == "elasticsearch" {
		switch {
		case c.Params["tls"] != "true":
		case c.Params["tls_skip_verify"] == "true":
			choose(ffTLS, kafkaTLSChoices[2])
		default:
			choose(ffTLS, kafkaTLSChoices[1])
		}
	}
	if c.Driver == "elasticsearch" && c.Params["auth"] == "apikey" {
		choose(ffAuth, "api key")
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

// formParam reports whether a field of the form sets params key k for driver.
func formParam(driver, k string) bool {
	switch driver {
	case "sqlserver":
		return strings.EqualFold(k, "encrypt") || strings.EqualFold(k, "TrustServerCertificate")
	case "kafka":
		return k == "sasl" || k == "tls" || k == "tls_skip_verify"
	case "elasticsearch":
		return k == "auth" || k == "tls" || k == "tls_skip_verify"
	}
	return false
}

// order is the fields' display order: the param rows and Add param go
// between Read-only and Save password.
func (f *ConnForm) order() []int {
	o := make([]int, 0, len(f.fields))
	for i := range ffSave {
		o = append(o, i)
	}
	for i := ffAddParam + 1; i < len(f.fields); i++ {
		o = append(o, i)
	}
	return append(o, ffAddParam, ffSave)
}

// shown reports whether field i applies to the chosen driver.
func (f *ConnForm) shown(i int) bool {
	switch i {
	case ffEncrypt, ffTrust:
		return f.value(ffDriver) == "sqlserver"
	case ffSASL:
		return f.value(ffDriver) == "kafka"
	case ffTLS:
		return f.value(ffDriver) == "kafka" || f.value(ffDriver) == "elasticsearch"
	case ffAuth:
		return f.value(ffDriver) == "elasticsearch"
	case ffDatabase:
		return f.value(ffDriver) != "kafka" && f.value(ffDriver) != "elasticsearch"
	}
	return true
}

// step moves the cursor by dir to the next shown field, wrapping around.
func (f *ConnForm) step(dir int) {
	o := f.order()
	at := slices.Index(o, f.cur)
	for {
		at = (at + dir + len(o)) % len(o)
		if f.shown(o[at]) {
			f.cur = o[at]
			return
		}
	}
}

// addParam turns Add param's "key=value" into a param row; an empty
// field is a no-op.
func (f *ConnForm) addParam() error {
	text := f.text(ffAddParam)
	if text == "" {
		return nil
	}
	k, v, ok := strings.Cut(text, "=")
	k, v = strings.TrimSpace(k), strings.TrimSpace(v)
	switch {
	case !ok || k == "" || v == "" || strings.ContainsAny(k, " \t"):
		return fmt.Errorf("add param: want key=value, got %q", text)
	case formParam(f.value(ffDriver), k):
		return fmt.Errorf("add param: %s is set by the fields above", k)
	case slices.ContainsFunc(f.fields[ffAddParam+1:], func(fd formField) bool { return fd.key == k }):
		return fmt.Errorf("add param: %s is already listed", k)
	}
	f.fields = append(f.fields, formField{label: k, key: k, input: newField(v)})
	f.fields[ffAddParam].input = newField("")
	return nil
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
		switch f.cur {
		case ffSave:
			return f.submit()
		case ffAddParam:
			f.err = ""
			if err := f.addParam(); err != nil {
				f.err = err.Error()
			}
		default:
			f.step(1)
		}
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
	if err := f.addParam(); err != nil {
		f.err = err.Error()
		return f, nil
	}
	port := 0
	if p := f.text(ffPort); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			f.err = fmt.Sprintf("port must be a number between 1 and 65535, got %q", p)
			return f, nil
		}
		port = n
	}
	database := f.text(ffDatabase)
	if !f.shown(ffDatabase) {
		database = ""
	}
	c, err := config.Normalize(config.Connection{
		Name: f.text(ffName), Driver: f.value(ffDriver), Host: f.text(ffHost), Port: port,
		User: f.text(ffUser), Database: database, SSH: f.text(ffSSH), Env: config.Env(f.value(ffEnv)),
		ReadOnly: f.submitReadOnly(), Params: f.submitParams(),
	})
	if err != nil {
		f.err = err.Error()
		return f, nil
	}
	return nil, emit(ConnFormMsg{
		Conn: c, Password: f.fields[ffPassword].input.String(), Save: f.value(ffSave) == "yes", Original: f.original,
	})
}

// submitReadOnly is the readonly key: nil for auto.
func (f *ConnForm) submitReadOnly() *bool {
	if v := f.value(ffReadOnly); v != "auto" {
		ro := v == "yes"
		return &ro
	}
	return nil
}

// submitParams are the param rows (an empty one removes its key) with the
// SQL Server, Kafka and Elasticsearch fields applied: default, no, none
// and off remove their key.
func (f *ConnForm) submitParams() map[string]string {
	p := maps.Clone(f.params)
	if p == nil {
		p = map[string]string{}
	}
	for _, fd := range f.fields[ffAddParam+1:] {
		if v := strings.TrimSpace(fd.input.String()); v != "" {
			p[fd.key] = v
		}
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
	if d := f.value(ffDriver); d == "kafka" || d == "elasticsearch" {
		delete(p, "tls")
		delete(p, "tls_skip_verify")
		if t := f.fields[ffTLS].choice; t > 0 {
			p["tls"] = "true"
			if t == 2 {
				p["tls_skip_verify"] = "true"
			}
		}
	}
	if f.value(ffDriver) == "kafka" {
		delete(p, "sasl")
		if s := f.value(ffSASL); s != "none" {
			p["sasl"] = s
		}
	}
	if f.value(ffDriver) == "elasticsearch" {
		delete(p, "auth")
		if f.value(ffAuth) == "api key" {
			p["auth"] = "apikey"
		}
	}
	if len(p) == 0 {
		return nil
	}
	return p
}

func (f *ConnForm) View(width, _ int) string {
	w := max(min(width-4, 60), 30)
	lw := 14 // label width; a long param key widens it
	for _, fd := range f.fields[ffAddParam+1:] {
		lw = min(max(lw, len(fd.key)+2), w/2)
	}
	lines := make([]string, 0, len(f.fields)+5)
	for _, i := range f.order() {
		fd := f.fields[i]
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
		if i == ffHost && f.value(ffDriver) == "kafka" {
			name = "Brokers" // host[:port], comma-separated
		}
		if i == ffHost && f.value(ffDriver) == "elasticsearch" {
			name = "URLs" // [scheme://]host[:port], comma-separated
		}
		label := fmt.Sprintf("%-*s", lw, ansi.Truncate(textutil.Sanitize(name), lw-1, "…"))
		switch {
		case oracleService && i != f.cur && fd.input.Len() == 0:
			v = theme.Muted.Render(oracleDefaultService)
		case i == ffAddParam && i != f.cur && fd.input.Len() == 0:
			v = theme.Muted.Render("key=value")
		case i == ffReadOnly && f.value(ffReadOnly) == "auto":
			on := "off"
			if config.Env(f.value(ffEnv)) == config.EnvProd {
				on = "on"
			}
			v = "‹ auto ›" + theme.Muted.Render(" ("+on+")")
		case fd.choices != nil:
			v = "‹ " + fd.choices[fd.choice] + " ›"
		case i == f.cur && fd.secret:
			v = fd.input.ViewMasked(w-lw-2, '•')
		case i == f.cur:
			v = fd.input.View(w - lw - 2)
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
	lines = append(lines, theme.Muted.Render("Read-only: auto is on for prod"))
	lines = append(lines, theme.Muted.Render("Params: enter adds key=value · an empty value removes it"))
	lines = append(lines, theme.Muted.Render(footer("tab next · enter next/save", popupHint(f.keys, keymap.SaveForm, "save"), "esc cancel")))
	return theme.Box(title, strings.Join(lines, "\n"), w, len(lines)+2, true)
}
