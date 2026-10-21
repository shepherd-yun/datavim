package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// templateKey is a template's tree key, "<kind>/<name>".
func templateKey(t es.Template) string { return t.Kind.String() + "/" + t.Name }

// parseTemplateKey splits a template's tree key.
func parseTemplateKey(key string) (es.TemplateKind, string) {
	kind, name, _ := strings.Cut(key, "/")
	for _, k := range []es.TemplateKind{es.TemplateComposable, es.TemplateLegacy, es.TemplateComponent} {
		if k.String() == kind {
			return k, name
		}
	}
	return es.TemplateComposable, name
}

func esTemplateTabKey(conn, key string) string { return "estemplate\x00" + conn + "\x00" + key }

// esOpenTemplateMsg opens a template's tab.
type esOpenTemplateMsg struct{ conn, key string }

// esTreeReloadMsg reads conn's tree again.
type esTreeReloadMsg struct{ conn string }

// esTemplateLoadedMsg is a template read for its tab.
type esTemplateLoadedMsg struct {
	tabID int
	conn  string
	tpl   es.Template
	err   error
}

func (m esTemplateLoadedMsg) TabID() int               { return m.tabID }
func (m esTemplateLoadedMsg) esMsg()                   {}
func (m esTemplateLoadedMsg) failure() (string, error) { return m.conn, m.err }

// esTemplatePutMsg is a confirmed template change.
type esTemplatePutMsg struct {
	tabID int
	body  json.RawMessage
}

func (m esTemplatePutMsg) TabID() int { return m.tabID }

// esTemplateSavedMsg is a template change's outcome.
type esTemplateSavedMsg struct {
	tabID int
	conn  string
	err   error
}

func (m esTemplateSavedMsg) TabID() int               { return m.tabID }
func (m esTemplateSavedMsg) esMsg()                   {}
func (m esTemplateSavedMsg) failure() (string, error) { return m.conn, m.err }

// esTemplateDeleteMsg is a confirmed template delete.
type esTemplateDeleteMsg struct{ conn, key string }

// esTemplateDeletedMsg is a template delete's outcome.
type esTemplateDeletedMsg struct {
	conn, key string
	err       error
}

func (m esTemplateDeletedMsg) failure() (string, error) { return m.conn, m.err }

// esTemplateTab shows one index template as JSON (spec §7.3).
type esTemplateTab struct {
	id       int
	backend  Backend
	keys     *keymap.Keymap
	settings config.Settings
	conn     string
	key      string
	kind     es.TemplateKind
	name     string

	tpl      es.Template
	loaded   bool
	err      string
	grid     *grid.Model
	editPath string

	status  string
	statusE bool

	width, height int
}

// openESTemplate opens (or shows) the tab of the template under key.
func (m *Model) openESTemplate(conn, key string) tea.Cmd {
	return m.showTab(esTemplateTabKey(conn, key), func(id int) tab {
		kind, name := parseTemplateKey(key)
		return &esTemplateTab{
			id: id, backend: m.backend, keys: m.keys, settings: m.settings, conn: conn, key: key, kind: kind, name: name,
			grid: grid.New(nil, max(m.settings.MaxColumnWidth, 200)),
		}
	})
}

func (t *esTemplateTab) ID() int                        { return t.id }
func (t *esTemplateTab) Title() string                  { return t.name + " · " + t.kind.String() + " template" }
func (t *esTemplateTab) Key() string                    { return esTemplateTabKey(t.conn, t.key) }
func (t *esTemplateTab) Context() keymap.Context        { return keymap.ESObject }
func (t *esTemplateTab) Target() (string, db.TableRef)  { return t.conn, db.TableRef{} }
func (t *esTemplateTab) SetSize(w, h int)               { t.width, t.height = max(w, 0), max(h, 0) }
func (t *esTemplateTab) Init() tea.Cmd                  { return t.load() }
func (t *esTemplateTab) retryAfterPassword() tea.Cmd    { return t.load() }
func (t *esTemplateTab) activeGrid() *grid.Model        { return t.grid }
func (t *esTemplateTab) Find(q string, fwd bool) bool   { return t.grid.Find(q, fwd) }
func (t *esTemplateTab) setStatus(s string, isErr bool) { t.status, t.statusE = s, isErr }

func (t *esTemplateTab) exportSource() exportSrc {
	return exportSrc{rs: t.grid.Result(), name: t.conn + "-" + t.name, conn: t.conn}
}

func (t *esTemplateTab) load() tea.Cmd {
	id, b, conn, kind, name := t.id, t.backend, t.conn, t.kind, t.name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esTemplateLoadedMsg{tabID: id, conn: conn}
		out.err = withES(ctx, b, conn, func(d es.Doer) error {
			ts, err := es.Templates(ctx, d)
			if err != nil {
				return err
			}
			i := slices.IndexFunc(ts, func(t es.Template) bool { return t.Kind == kind && t.Name == name })
			if i < 0 {
				return errors.New(kind.String() + " template " + name + " not found")
			}
			out.tpl = ts[i]
			return nil
		})
		return out
	}
}

// pretty is the template's body indented by two.
func (t *esTemplateTab) pretty() string {
	var b bytes.Buffer
	if json.Indent(&b, t.tpl.Body, "", "  ") != nil {
		return string(t.tpl.Body)
	}
	return b.String()
}

// yankText copies the whole template.
func (t *esTemplateTab) yankText(a keymap.Action) (text, what string, ok bool) {
	if a != keymap.YankRow || !t.loaded {
		return "", "", false
	}
	return t.pretty(), "the template", true
}

func (t *esTemplateTab) Handle(a keymap.Action) tea.Cmd {
	t.status = ""
	switch a {
	case keymap.Refresh:
		return t.load()
	case keymap.ExternalEdit:
		if cfg, _ := t.backend.Connection(t.conn); cfg.IsReadOnly() {
			t.setStatus(readOnlyHint, true)
			return nil
		}
		if !t.loaded {
			return nil
		}
		return writeTempEdit(t.id, t.pretty()+"\n", "datavim-template-*.json")
	}
	cmd, _ := gridAction(t.grid, a)
	return cmd
}

func (t *esTemplateTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case esTemplateLoadedMsg:
		if m.err != nil {
			t.err = esReason(m.err)
			return nil
		}
		t.err, t.loaded, t.tpl = "", true, m.tpl
		lines := strings.Split(t.pretty(), "\n")
		rows := make([][]string, len(lines))
		for i, l := range lines {
			rows[i] = []string{l}
		}
		t.grid.ReplaceResult(textResult([]string{t.kind.String() + " template " + t.name}, rows))
	case editFileMsg:
		if m.err != nil {
			t.setStatus("cannot create temp file: "+m.err.Error(), true)
			return nil
		}
		t.editPath = m.path
		argv := append(slices.Clone(t.settings.EditorCommand(os.Getenv)), m.path)
		id, path := t.id, m.path
		return tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg { //nolint:gosec // the user's own editor
			return editDoneMsg{tabID: id, path: path, err: err}
		})
	case editDoneMsg:
		return readEditedFile(t.id, m)
	case editTextMsg:
		if m.err != nil {
			t.setStatus("editor: "+m.err.Error(), true)
			return nil
		}
		return t.edited(m.text)
	case esTemplatePutMsg:
		return t.put(m.body)
	case esTemplateSavedMsg:
		if m.err != nil {
			t.setStatus(esReason(m.err), true)
			return nil
		}
		t.setStatus("updated "+t.kind.String()+" template "+t.name, false)
		conn := t.conn
		return tea.Batch(t.load(), func() tea.Msg { return esTreeReloadMsg{conn: conn} })
	}
	return nil
}

// edited asks to put the template written in $EDITOR.
func (t *esTemplateTab) edited(text string) tea.Cmd {
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.setStatus("not valid JSON: "+err.Error(), true)
		return nil
	}
	var old any
	_ = json.Unmarshal(t.tpl.Body, &old)
	body, _ := json.Marshal(v)
	if was, _ := json.Marshal(old); bytes.Equal(body, was) {
		t.setStatus("no changes", false)
		return nil
	}
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(text)) == nil {
		body = compact.Bytes() // the keys in the order written
	}
	path := es.TemplatePath(t.kind, t.name)
	p := esPlan{
		Class: esx.Classify("PUT", path, body), Count: -1, Method: "PUT", Path: path, Body: []byte(text),
		Lines: []string{"update " + t.kind.String() + " template " + t.name},
	}
	cfg, _ := t.backend.Connection(t.conn)
	return confirmESCmd(cfg, p, esTemplatePutMsg{tabID: t.id, body: body}, t.setStatus)
}

func (t *esTemplateTab) put(body json.RawMessage) tea.Cmd {
	id, b, conn, kind, name := t.id, t.backend, t.conn, t.kind, t.name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		err := withES(ctx, b, conn, func(d es.Doer) error { return es.PutTemplate(ctx, d, kind, name, body) })
		return esTemplateSavedMsg{tabID: id, conn: conn, err: err}
	}
}

func (t *esTemplateTab) View(focused bool) string {
	var lines []string
	if t.status != "" {
		st := theme.Muted
		if t.statusE {
			st = theme.Error
		}
		lines = append(lines, st.Render(textutil.Sanitize(t.status)))
	}
	switch {
	case t.err != "":
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.err)))
	case !t.loaded:
		lines = append(lines, theme.Muted.Render("loading…"))
	}
	for i, l := range lines {
		lines[i] = textutil.Fit(l, t.width)
	}
	if t.loaded {
		t.grid.SetSize(t.width, max(t.height-len(lines), 0))
		lines = append(lines, t.grid.View(focused))
	}
	return strings.Join(lines, "\n")
}

// askDeleteTemplate is dd on a template: confirmed by its name.
func (m *Model) askDeleteTemplate(n *tree.Node) tea.Cmd {
	kind, name := parseTemplateKey(n.Key)
	path := es.TemplatePath(kind, name)
	p := esPlan{
		Class: esx.Classify("DELETE", path, nil), Count: -1, Method: "DELETE", Path: path, Expect: name,
		Lines: []string{"delete " + kind.String() + " template " + name},
	}
	return m.confirmES(n.Conn, p, esTemplateDeleteMsg{conn: n.Conn, key: n.Key})
}

func (m *Model) esTemplateDelete(msg esTemplateDeleteMsg) tea.Cmd {
	b := m.backend
	kind, name := parseTemplateKey(msg.key)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		err := withES(ctx, b, msg.conn, func(d es.Doer) error { return es.DeleteTemplate(ctx, d, kind, name) })
		return esTemplateDeletedMsg{conn: msg.conn, key: msg.key, err: err}
	}
}

func (m *Model) esTemplateDeleted(msg esTemplateDeletedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(esReason(msg.err), true)
		return nil
	}
	kind, name := parseTemplateKey(msg.key)
	m.setStatus("deleted "+kind.String()+" template "+name, false)
	var cmds []tea.Cmd
	if i := slices.IndexFunc(m.tabs, func(t tab) bool { return t.Key() == esTemplateTabKey(msg.conn, msg.key) }); i >= 0 {
		cmds = append(cmds, m.closeTab(i))
	}
	return tea.Batch(append(cmds, m.reloadESTree(msg.conn))...)
}
