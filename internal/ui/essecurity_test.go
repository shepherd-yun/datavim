package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

func fakeSecurityES(f *fakeES) {
	f.security = es.SecurityES
	f.users = map[string]string{
		"elastic": `{"username":"elastic","roles":["superuser"],"full_name":null,"email":null,"metadata":{"_reserved":true},"enabled":true}`,
		"bob":     `{"username":"bob","roles":["viewer"],"full_name":"Bob B","email":"bob@x","metadata":{},"enabled":true}`,
		"carol":   `{"username":"carol","roles":[],"full_name":null,"email":null,"metadata":{},"enabled":false}`,
	}
	f.roles = map[string]string{
		"admin":     `{"cluster":["all"],"indices":[{"names":["*"],"privileges":["all"]}],"metadata":{},"transient_metadata":{"enabled":true}}`,
		"superuser": `{"cluster":["all"],"indices":[],"metadata":{"_reserved":true}}`,
	}
}

func fakeSecurityOS(f *fakeES) {
	f.info = es.Info{ClusterName: "os", Version: "2.17.0", Distribution: "opensearch", Major: 2, Minor: 17}
	f.security = es.SecurityOpenSearch
	f.users = map[string]string{
		"admin": `{"hash":"","reserved":true,"hidden":false,"backend_roles":["admin"],"opendistro_security_roles":[]}`,
		"bob":   `{"hash":"","reserved":false,"hidden":false,"backend_roles":["dev"],"opendistro_security_roles":["readall"]}`,
	}
	f.roles = map[string]string{
		"admin": `{"reserved":false,"hidden":false,"static":false,"cluster_permissions":["cluster_all"],"index_permissions":[]}`,
	}
}

// openServerSection opens the cluster tab on section s.
func openServerSection(t *testing.T, m *Model, s eserverSection) *esServerTab {
	t.Helper()
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindConn })
	press(t, m, "space", "i")
	tab, ok := m.activeTab().(*esServerTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	if s != secESOverview {
		drain(t, m, tab.setSection(s))
	}
	return tab
}

// selectUser puts the cursor on name's row of the Users section.
func selectUser(t *testing.T, m *Model, tab *esServerTab, name string) {
	t.Helper()
	i := slices.IndexFunc(tab.users, func(u es.User) bool { return u.Name == name })
	if i < 0 {
		t.Fatalf("no user %s in %+v", name, tab.users)
	}
	pressAll(t, m, "g", "g")
	for range i {
		pressAll(t, m, "j")
	}
}

func TestESSecurityHidden(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	tab := openServerSection(t, m, secESSettings)
	if tab.sectionShown(secESUsers) || tab.sectionShown(secESRoles) {
		t.Fatal("users shown without security")
	}
	pressAll(t, m, "]")
	if tab.section != secESSettings {
		t.Errorf("] went to %v", tab.section)
	}
	if strings.Contains(screen(m), "Users") {
		t.Errorf("screen:\n%s", screen(m))
	}
	id := tab.id
	drain(t, m, func() tea.Msg { return esServerTickMsg{tabID: id, gen: tab.gen} })
	drain(t, m, func() tea.Msg { return esServerTickMsg{tabID: id, gen: tab.gen} })
	if n := len(sentContaining(f, "_xpack")); n != 1 {
		t.Errorf("_xpack sent %d times", n)
	}
}

func TestESUsers(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeSecurityES(f)
	tab := openServerSection(t, m, secESSettings)
	pressAll(t, m, "]")
	if tab.section != secESUsers {
		t.Fatalf("section %v", tab.section)
	}
	r := tab.activeGrid().Result()
	var cols []string
	for _, c := range r.Columns {
		cols = append(cols, c.Name)
	}
	if !slices.Equal(cols, []string{"USER", "ROLES", "FULL NAME", "EMAIL", "ENABLED", "RESERVED"}) {
		t.Errorf("cols %v", cols)
	}
	if r.Rows[0][0].Text != "bob" || r.Rows[0][4].Text != "yes" || r.Rows[1][4].Text != "no" || r.Rows[2][5].Text != "★" {
		t.Errorf("rows %+v", r.Rows)
	}

	m2, _, f2 := newESModel(t, config.Connection{})
	fakeSecurityOS(f2)
	tab2 := openServerSection(t, m2, secESUsers)
	cols = nil
	for _, c := range tab2.activeGrid().Result().Columns {
		cols = append(cols, c.Name)
	}
	if !slices.Equal(cols, []string{"USER", "ROLES", "BACKEND ROLES", "RESERVED"}) {
		t.Errorf("opensearch cols %v", cols)
	}
}

func TestESUsersForbidden(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeSecurityES(f)
	f.routes["GET _security/user"] = func(es.Request) es.Response { return errResp(403, "security_exception", "no") }
	tab := openServerSection(t, m, secESUsers)
	if !strings.Contains(screen(m), "permission denied") || tab.err != "" {
		t.Errorf("err %q screen:\n%s", tab.err, screen(m))
	}
}

func TestESCreateUser(t *testing.T) {
	no := false
	m, _, f := newESModel(t, config.Connection{Env: config.EnvProd, ReadOnly: &no})
	fakeSecurityES(f)
	openServerSection(t, m, secESUsers)
	pressAll(t, m, "o")
	if _, ok := m.overlay.(*overlay.UserForm); !ok {
		t.Fatalf("overlay %T", m.overlay)
	}
	m.overlay = nil
	drain(t, m, func() tea.Msg {
		return overlay.UserFormMsg{Name: "dave", Password: "hunter22", Roles: "viewer, editor"}
	})
	v := confirmText(t, m)
	if !strings.Contains(v, "create user dave with roles viewer, editor") || strings.Contains(v, "hunter22") || strings.Contains(v, "password") {
		t.Fatalf("confirm:\n%s", v)
	}
	pressAll(t, m, "y")
	if b := f.lastBody("PUT _security/user/dave"); !strings.Contains(b, `"password":"hunter22"`) {
		t.Fatalf("body %q sent %v", b, f.sent())
	}
	if !strings.Contains(m.status+screen(m), "created user dave") {
		t.Errorf("status %q", m.status)
	}
}

func typeSecret(t *testing.T, m *Model, s string) {
	t.Helper()
	if _, ok := m.overlay.(*overlay.Secret); !ok {
		t.Fatalf("overlay %T", m.overlay)
	}
	typeText(t, m, s)
	pressAll(t, m, "enter")
}

func TestESChangePassword(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeSecurityES(f)
	tab := openServerSection(t, m, secESUsers)
	selectUser(t, m, tab, "bob")
	pressAll(t, m, "P")
	typeSecret(t, m, "n3w-pass")
	typeSecret(t, m, "n3w-pass")
	if v := confirmText(t, m); !strings.Contains(v, "change the password of bob") || strings.Contains(v, "n3w-pass") {
		t.Fatalf("confirm:\n%s", v)
	}
	pressAll(t, m, "y")
	if b := f.lastBody("POST _security/user/bob/_password"); b != `{"password":"n3w-pass"}` {
		t.Fatalf("body %q", b)
	}
	if !strings.Contains(tab.status, "changed the password of bob") {
		t.Errorf("status %q", tab.status)
	}
	if s := fmt.Sprintf("%+v", *tab) + m.status + screen(m); strings.Contains(s, "n3w-pass") {
		t.Error("the password stayed in the tab or on screen")
	}

	n := len(f.sent())
	pressAll(t, m, "P")
	typeSecret(t, m, "abcdef1")
	typeSecret(t, m, "abcdef2")
	if m.overlay != nil || tab.status != "passwords differ" || len(f.sent()) != n {
		t.Errorf("overlay %T status %q sent %v", m.overlay, tab.status, f.sent()[n:])
	}
}

func TestESToggleUser(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{User: "bob"})
	fakeSecurityES(f)
	tab := openServerSection(t, m, secESUsers)
	selectUser(t, m, tab, "bob")
	pressAll(t, m, "x")
	if v := confirmText(t, m); !strings.Contains(v, "disable user bob") || !strings.Contains(v, "this is the user datavim is connected as") {
		t.Fatalf("confirm:\n%s", v)
	}
	pressAll(t, m, "y")
	if !slices.Contains(f.sent(), "PUT _security/user/bob/_disable") {
		t.Fatalf("sent %v", f.sent())
	}
	selectUser(t, m, tab, "carol")
	pressAll(t, m, "x")
	if v := confirmText(t, m); !strings.Contains(v, "enable user carol") {
		t.Fatalf("confirm:\n%s", v)
	}
}

func TestESToggleUserOpenSearch(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeSecurityOS(f)
	tab := openServerSection(t, m, secESUsers)
	selectUser(t, m, tab, "bob")
	pressAll(t, m, "x")
	if m.overlay != nil || tab.status != "OpenSearch users cannot be disabled" {
		t.Errorf("overlay %T status %q", m.overlay, tab.status)
	}
}

func TestESUserRolesAndDelete(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeSecurityOS(f)
	tab := openServerSection(t, m, secESUsers)
	selectUser(t, m, tab, "bob")
	pressAll(t, m, "i")
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatalf("overlay %T", m.overlay)
	}
	for range len("readall") {
		pressAll(t, m, "backspace")
	}
	typeText(t, m, "a, c")
	pressAll(t, m, "enter")
	if v := confirmText(t, m); !strings.Contains(v, "roles of bob: readall → a, c") {
		t.Fatalf("confirm:\n%s", v)
	}
	pressAll(t, m, "y")
	if b := f.lastBody("PATCH _plugins/_security/api/internalusers/bob"); !sameJSONText(b,
		`[{"op":"add","path":"/opendistro_security_roles","value":["a","c"]},{"op":"add","path":"/backend_roles","value":["dev"]}]`) {
		t.Fatalf("body %q", b)
	}
	selectUser(t, m, tab, "bob")
	pressAll(t, m, "d", "d")
	if v := confirmText(t, m); !strings.Contains(v, "delete user bob") {
		t.Fatalf("confirm:\n%s", v)
	}
	typeText(t, m, "bob")
	pressAll(t, m, "enter")
	if !slices.Contains(f.sent(), "DELETE _plugins/_security/api/internalusers/bob") {
		t.Fatalf("sent %v", f.sent())
	}
}

func TestESReservedUser(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeSecurityES(f)
	tab := openServerSection(t, m, secESUsers)
	selectUser(t, m, tab, "elastic")
	for _, k := range [][]string{{"i"}, {"x"}, {"d", "d"}} {
		pressAll(t, m, k...)
		if m.overlay != nil || tab.status != "elastic is a reserved user" {
			t.Errorf("%v: overlay %T status %q", k, m.overlay, tab.status)
		}
	}
	pressAll(t, m, "P")
	if _, ok := m.overlay.(*overlay.Secret); !ok {
		t.Errorf("P on a reserved user: overlay %T", m.overlay)
	}
}

func TestESRoles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeES)
		put   string
	}{
		{"es", fakeSecurityES, "PUT _security/role/admin"},
		{"opensearch", fakeSecurityOS, "PUT _plugins/_security/api/roles/admin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, f := newESModel(t, config.Connection{})
			tc.setup(f)
			tab := openServerSection(t, m, secESRoles)
			if r := tab.activeGrid().Result(); r.Rows[0][0].Text != "admin" {
				t.Fatalf("rows %+v", r.Rows)
			}
			pressAll(t, m, "g", "g", "enter")
			ot, ok := m.activeTab().(*esObjectTab)
			if !ok || ot.Title() != "admin · role" {
				t.Fatalf("active tab %T", m.activeTab())
			}
			press(t, m, "space", "v")
			templateEditorSaves(t, m, ot, `{"cluster":["monitor"]}`)
			pressAll(t, m, "y")
			if b := f.lastBody(tc.put); b != `{"cluster":["monitor"]}` {
				t.Errorf("body %q sent %v", b, f.sent())
			}
		})
	}
}

func TestESReservedRole(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	fakeSecurityES(f)
	tab := openServerSection(t, m, secESRoles)
	pressAll(t, m, "G", "d", "d")
	if m.overlay != nil || tab.status != "superuser is a reserved role" {
		t.Errorf("overlay %T status %q", m.overlay, tab.status)
	}
	pressAll(t, m, "g", "g", "d", "d")
	if v := confirmText(t, m); !strings.Contains(v, "delete role admin") {
		t.Fatalf("confirm:\n%s", v)
	}
	typeText(t, m, "admin")
	pressAll(t, m, "enter")
	if !slices.Contains(f.sent(), "DELETE _security/role/admin") {
		t.Fatalf("sent %v", f.sent())
	}
}

func TestESSecurityReadOnly(t *testing.T) {
	ro := true
	m, _, f := newESModel(t, config.Connection{ReadOnly: &ro})
	fakeSecurityES(f)
	tab := openServerSection(t, m, secESUsers)
	selectUser(t, m, tab, "bob")
	for _, k := range [][]string{{"o"}, {"i"}, {"x"}, {"P"}, {"d", "d"}} {
		tab.status = ""
		pressAll(t, m, k...)
		if m.overlay != nil || tab.status != readOnlyHint {
			t.Errorf("%v: overlay %T status %q", k, m.overlay, tab.status)
		}
	}
}
