package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
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
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// esObjKind is a kind of object kept as JSON that a tab shows, edits in
// $EDITOR and deletes (spec §7.3, §8.2, §8.3).
type esObjKind int

const (
	objComposable esObjKind = iota
	objLegacy
	objComponent
	objPolicy
	objPipeline
	objRole
)

// String is the kind in an object's key; the template ones are E2's.
func (k esObjKind) String() string {
	return [...]string{"composable", "legacy", "component", "ilm", "pipeline", "role"}[k]
}

func (k esObjKind) label() string {
	return [...]string{"composable template", "legacy template", "component template", "ILM policy", "ingest pipeline", "role"}[k]
}

// template is the template kind of a template object.
func (k esObjKind) template() (es.TemplateKind, bool) {
	switch k {
	case objComposable:
		return es.TemplateComposable, true
	case objLegacy:
		return es.TemplateLegacy, true
	case objComponent:
		return es.TemplateComponent, true
	}
	return 0, false
}

// path is the REST path PUT and DELETE take; a role's depends on api.
func (k esObjKind) path(name string, api es.SecurityAPI) string {
	if tk, ok := k.template(); ok {
		return es.TemplatePath(tk, name)
	}
	switch k {
	case objPolicy:
		return "_ilm/policy/" + url.PathEscape(name)
	case objPipeline:
		return "_ingest/pipeline/" + url.PathEscape(name)
	}
	return es.RolePath(api, name)
}

func objKey(k esObjKind, name string) string { return k.String() + "/" + name }

// templateKey is a template's tree key.
func templateKey(t es.Template) string {
	return objKey([...]esObjKind{objComposable, objLegacy, objComponent}[t.Kind], t.Name)
}

// parseObjKey splits an object's key, "<kind>/<name>".
func parseObjKey(key string) (esObjKind, string) {
	kind, name, _ := strings.Cut(key, "/")
	for k := objComposable; k <= objRole; k++ {
		if k.String() == kind {
			return k, name
		}
	}
	return objComposable, name
}

// esObjectTabKey keeps E2's prefix: the tabs of templates kept their keys.
func esObjectTabKey(conn, key string) string { return "estemplate\x00" + conn + "\x00" + key }

// esObject is what an object's tab shows.
type esObject struct {
	body     json.RawMessage
	reserved bool // a built-in role: not edited or deleted
}

func loadObject(ctx context.Context, d es.Doer, kind esObjKind, name string, api es.SecurityAPI) (esObject, error) {
	notFound := errors.New(kind.label() + " " + name + " not found")
	if tk, ok := kind.template(); ok {
		ts, err := es.Templates(ctx, d)
		if err != nil {
			return esObject{}, err
		}
		if i := slices.IndexFunc(ts, func(t es.Template) bool { return t.Kind == tk && t.Name == name }); i >= 0 {
			return esObject{body: ts[i].Body}, nil
		}
		return esObject{}, notFound
	}
	switch kind {
	case objPolicy:
		ps, err := es.Policies(ctx, d)
		if err != nil {
			return esObject{}, err
		}
		if i := slices.IndexFunc(ps, func(p es.Policy) bool { return p.Name == name }); i >= 0 {
			return esObject{body: ps[i].Body}, nil
		}
	case objPipeline:
		ps, err := es.Pipelines(ctx, d)
		if err != nil {
			return esObject{}, err
		}
		if i := slices.IndexFunc(ps, func(p es.Pipeline) bool { return p.Name == name }); i >= 0 {
			return esObject{body: ps[i].Body}, nil
		}
	case objRole:
		rs, err := es.Roles(ctx, d, api)
		if err != nil {
			return esObject{}, err
		}
		if i := slices.IndexFunc(rs, func(r es.Role) bool { return r.Name == name }); i >= 0 {
			return esObject{body: rs[i].Body, reserved: rs[i].Reserved}, nil
		}
	}
	return esObject{}, notFound
}

func putObject(ctx context.Context, d es.Doer, kind esObjKind, name string, api es.SecurityAPI, body json.RawMessage) error {
	if tk, ok := kind.template(); ok {
		return es.PutTemplate(ctx, d, tk, name, body)
	}
	switch kind {
	case objPolicy:
		return es.PutPolicy(ctx, d, name, body)
	case objPipeline:
		return es.PutPipeline(ctx, d, name, body)
	}
	return es.PutRole(ctx, d, api, name, body)
}

func deleteObject(ctx context.Context, d es.Doer, kind esObjKind, name string, api es.SecurityAPI) error {
	if tk, ok := kind.template(); ok {
		return es.DeleteTemplate(ctx, d, tk, name)
	}
	switch kind {
	case objPolicy:
		return es.DeletePolicy(ctx, d, name)
	case objPipeline:
		return es.DeletePipeline(ctx, d, name)
	}
	return es.DeleteRole(ctx, d, api, name)
}

// esOpenObjectMsg opens an object's tab; api is the role API for a role.
type esOpenObjectMsg struct {
	conn, key string
	api       es.SecurityAPI
}

// esTreeReloadMsg reads conn's tree again.
type esTreeReloadMsg struct{ conn string }

// esObjectLoadedMsg is an object read for its tab.
type esObjectLoadedMsg struct {
	tabID int
	conn  string
	obj   esObject
	err   error
}

func (m esObjectLoadedMsg) TabID() int               { return m.tabID }
func (m esObjectLoadedMsg) esMsg()                   {}
func (m esObjectLoadedMsg) failure() (string, error) { return m.conn, m.err }

// esObjectPutMsg is a confirmed change of an object.
type esObjectPutMsg struct {
	tabID int
	body  json.RawMessage
}

func (m esObjectPutMsg) TabID() int { return m.tabID }

// esObjectSavedMsg is an object change's outcome.
type esObjectSavedMsg struct {
	tabID int
	conn  string
	err   error
}

func (m esObjectSavedMsg) TabID() int               { return m.tabID }
func (m esObjectSavedMsg) esMsg()                   {}
func (m esObjectSavedMsg) failure() (string, error) { return m.conn, m.err }

// esObjectDeleteMsg is a confirmed delete of an object.
type esObjectDeleteMsg struct {
	conn, key string
	api       es.SecurityAPI
}

// esObjectDeletedMsg is an object delete's outcome.
type esObjectDeletedMsg struct {
	conn, key string
	err       error
}

func (m esObjectDeletedMsg) failure() (string, error) { return m.conn, m.err }

// esObjectTab shows one object as JSON.
type esObjectTab struct {
	id       int
	backend  Backend
	keys     *keymap.Keymap
	settings config.Settings
	conn     string
	key      string
	kind     esObjKind
	name     string
	api      es.SecurityAPI // a role's

	obj      esObject
	loaded   bool
	err      string
	grid     *grid.Model
	editPath string

	status  string
	statusE bool

	width, height int
}

// openESObject opens (or shows) the tab of the object under msg.key.
func (m *Model) openESObject(msg esOpenObjectMsg) tea.Cmd {
	return m.showTab(esObjectTabKey(msg.conn, msg.key), func(id int) tab {
		kind, name := parseObjKey(msg.key)
		return &esObjectTab{
			id: id, backend: m.backend, keys: m.keys, settings: m.settings, conn: msg.conn, key: msg.key, kind: kind, name: name, api: msg.api,
			grid: grid.New(nil, max(m.settings.MaxColumnWidth, 200)),
		}
	})
}

func (t *esObjectTab) ID() int                        { return t.id }
func (t *esObjectTab) Title() string                  { return t.name + " · " + t.kind.label() }
func (t *esObjectTab) Key() string                    { return esObjectTabKey(t.conn, t.key) }
func (t *esObjectTab) Context() keymap.Context        { return keymap.ESObject }
func (t *esObjectTab) Target() (string, db.TableRef)  { return t.conn, db.TableRef{} }
func (t *esObjectTab) SetSize(w, h int)               { t.width, t.height = max(w, 0), max(h, 0) }
func (t *esObjectTab) Init() tea.Cmd                  { return t.load() }
func (t *esObjectTab) retryAfterPassword() tea.Cmd    { return t.load() }
func (t *esObjectTab) activeGrid() *grid.Model        { return t.grid }
func (t *esObjectTab) Find(q string, fwd bool) bool   { return t.grid.Find(q, fwd) }
func (t *esObjectTab) setStatus(s string, isErr bool) { t.status, t.statusE = s, isErr }

func (t *esObjectTab) exportSource() exportSrc {
	return exportSrc{rs: t.grid.Result(), name: t.conn + "-" + t.name, conn: t.conn}
}

func (t *esObjectTab) load() tea.Cmd {
	id, b, conn, kind, name, api := t.id, t.backend, t.conn, t.kind, t.name, t.api
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		out := esObjectLoadedMsg{tabID: id, conn: conn}
		out.err = withES(ctx, b, conn, func(d es.Doer) error {
			var err error
			out.obj, err = loadObject(ctx, d, kind, name, api)
			return err
		})
		return out
	}
}

// pretty is the object's body indented by two.
func (t *esObjectTab) pretty() string {
	var b bytes.Buffer
	if json.Indent(&b, t.obj.body, "", "  ") != nil {
		return string(t.obj.body)
	}
	return b.String()
}

// yankText copies the whole object.
func (t *esObjectTab) yankText(a keymap.Action) (text, what string, ok bool) {
	if a != keymap.YankRow || !t.loaded {
		return "", "", false
	}
	return t.pretty(), "the " + t.kind.label(), true
}

func (t *esObjectTab) Handle(a keymap.Action) tea.Cmd {
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
		if t.obj.reserved {
			t.setStatus(t.name+" is a reserved "+t.kind.label(), true)
			return nil
		}
		return writeTempEdit(t.id, t.pretty()+"\n", "datavim-"+t.kind.String()+"-*.json")
	}
	cmd, _ := gridAction(t.grid, a)
	return cmd
}

func (t *esObjectTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case esObjectLoadedMsg:
		if m.err != nil {
			t.err = esReason(m.err)
			return nil
		}
		t.err, t.loaded, t.obj = "", true, m.obj
		lines := strings.Split(t.pretty(), "\n")
		rows := make([][]string, len(lines))
		for i, l := range lines {
			rows[i] = []string{l}
		}
		t.grid.ReplaceResult(textResult([]string{t.kind.label() + " " + t.name}, rows))
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
	case esObjectPutMsg:
		return t.put(m.body)
	case esObjectSavedMsg:
		if m.err != nil {
			t.setStatus(esReason(m.err), true)
			return nil
		}
		t.setStatus("updated "+t.kind.label()+" "+t.name, false)
		conn := t.conn
		if t.kind == objRole {
			return tea.Batch(t.load(), func() tea.Msg { return esServerReloadMsg{conn: conn} })
		}
		return tea.Batch(t.load(), func() tea.Msg { return esTreeReloadMsg{conn: conn} })
	}
	return nil
}

// edited asks to put the object written in $EDITOR.
func (t *esObjectTab) edited(text string) tea.Cmd {
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.setStatus("not valid JSON: "+err.Error(), true)
		return nil
	}
	var old any
	_ = json.Unmarshal(t.obj.body, &old)
	body, _ := json.Marshal(v)
	if was, _ := json.Marshal(old); bytes.Equal(body, was) {
		t.setStatus("no changes", false)
		return nil
	}
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(text)) == nil {
		body = compact.Bytes() // the keys in the order written
	}
	path := t.kind.path(t.name, t.api)
	p := esPlan{
		Class: esx.Classify("PUT", path, body), Count: -1, Method: "PUT", Path: path, Body: []byte(text),
		Lines: []string{"update " + t.kind.label() + " " + t.name},
	}
	cfg, _ := t.backend.Connection(t.conn)
	return confirmESCmd(cfg, p, esObjectPutMsg{tabID: t.id, body: body}, t.setStatus)
}

func (t *esObjectTab) put(body json.RawMessage) tea.Cmd {
	id, b, conn, kind, name, api := t.id, t.backend, t.conn, t.kind, t.name, t.api
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		err := withES(ctx, b, conn, func(d es.Doer) error { return putObject(ctx, d, kind, name, api, body) })
		return esObjectSavedMsg{tabID: id, conn: conn, err: err}
	}
}

func (t *esObjectTab) View(focused bool) string {
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

// askDeleteObject is dd on an object: dangerous, confirmed by its name.
func (m *Model) askDeleteObject(conn, key string, api es.SecurityAPI) tea.Cmd {
	kind, name := parseObjKey(key)
	path := kind.path(name, api)
	p := esPlan{
		Class: esx.Classify("DELETE", path, nil), Count: -1, Method: "DELETE", Path: path, Expect: name,
		Lines: []string{"delete " + kind.label() + " " + name},
	}
	return m.confirmES(conn, p, esObjectDeleteMsg{conn: conn, key: key, api: api})
}

func (m *Model) esObjectDelete(msg esObjectDeleteMsg) tea.Cmd {
	b := m.backend
	kind, name := parseObjKey(msg.key)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		err := withES(ctx, b, msg.conn, func(d es.Doer) error { return deleteObject(ctx, d, kind, name, msg.api) })
		return esObjectDeletedMsg{conn: msg.conn, key: msg.key, err: err}
	}
}

func (m *Model) esObjectDeleted(msg esObjectDeletedMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(esReason(msg.err), true)
		return nil
	}
	kind, name := parseObjKey(msg.key)
	m.setStatus("deleted "+kind.label()+" "+name, false)
	var cmds []tea.Cmd
	if i := slices.IndexFunc(m.tabs, func(t tab) bool { return t.Key() == esObjectTabKey(msg.conn, msg.key) }); i >= 0 {
		cmds = append(cmds, m.closeTab(i))
	}
	if kind == objRole {
		conn := msg.conn
		cmds = append(cmds, func() tea.Msg { return esServerReloadMsg{conn: conn} })
	} else {
		cmds = append(cmds, m.reloadESTree(msg.conn))
	}
	return tea.Batch(cmds...)
}
