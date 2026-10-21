package ui

import (
	"context"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
)

// The Users and Roles sections of a cluster tab (spec §8.3). A password
// lives only in the messages that carry it to its request: never in a
// field of the tab, the status, a confirmation or the log.

// esUserWriteMsg is a confirmed change of a user.
type esUserWriteMsg struct {
	tabID    int
	op       string // create, roles, enable, disable, password, delete
	user     es.User
	password string
}

func (m esUserWriteMsg) TabID() int { return m.tabID }

// esUserDoneMsg is a user change's outcome.
type esUserDoneMsg struct {
	tabID    int
	conn, op string
	name     string
	err      error
}

func (m esUserDoneMsg) TabID() int               { return m.tabID }
func (m esUserDoneMsg) esMsg()                   {}
func (m esUserDoneMsg) failure() (string, error) { return m.conn, m.err }

// esUserRolesMsg is the roles typed for a user.
type esUserRolesMsg struct {
	tabID       int
	name, roles string
}

func (m esUserRolesMsg) TabID() int { return m.tabID }

// esPasswordMsg is a new password typed once (again is false) or twice.
type esPasswordMsg struct {
	tabID         int
	name          string
	first, second string
	again         bool
}

func (m esPasswordMsg) TabID() int { return m.tabID }

// esAskDeleteObjectMsg asks the model to delete an object.
type esAskDeleteObjectMsg struct {
	conn, key string
	api       es.SecurityAPI
}

func (t *esServerTab) usersResult() *db.ResultSet {
	os := t.security == es.SecurityOpenSearch
	cols := []string{"USER", "ROLES"}
	if os {
		cols = append(cols, "BACKEND ROLES")
	} else {
		cols = append(cols, "FULL NAME", "EMAIL", "ENABLED")
	}
	cols = append(cols, "RESERVED")
	rows := make([][]string, len(t.users))
	for i, u := range t.users {
		r := []string{u.Name, strings.Join(u.Roles, ", ")}
		if os {
			r = append(r, strings.Join(u.BackendRoles, ", "))
		} else {
			r = append(r, u.FullName, u.Email, map[bool]string{true: "yes", false: "no"}[u.Enabled])
		}
		rows[i] = append(r, star(u.Reserved))
	}
	return textResult(cols, rows)
}

func star(b bool) string {
	if b {
		return "★"
	}
	return ""
}

func (t *esServerTab) rolesResult() *db.ResultSet {
	rows := make([][]string, len(t.roles))
	for i, r := range t.roles {
		rows[i] = []string{r.Name, textTrunc(strings.Join(r.Cluster, ", "), 60), strconv.Itoa(r.Indices), star(r.Reserved)}
	}
	return textResult([]string{"ROLE", "CLUSTER", "INDEX PRIVILEGES", "RESERVED"}, rows)
}

func textTrunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// userMark dims the disabled users.
func (t *esServerTab) userMark(row, _ int) grid.Mark {
	if row < len(t.users) && !t.users[row].Enabled {
		return grid.MarkMuted
	}
	return grid.MarkNone
}

func (t *esServerTab) selectedUser() (es.User, bool) {
	r, _ := t.grids[secESUsers].Position()
	if r < 0 || r >= len(t.users) {
		return es.User{}, false
	}
	return t.users[r], true
}

func (t *esServerTab) selectedRole() (es.Role, bool) {
	r, _ := t.grids[secESRoles].Position()
	if r < 0 || r >= len(t.roles) {
		return es.Role{}, false
	}
	return t.roles[r], true
}

// handleSecurity is a key on Users or Roles.
func (t *esServerTab) handleSecurity(a keymap.Action) (tea.Cmd, bool) {
	cfg, _ := t.backend.Connection(t.conn)
	writes := []keymap.Action{keymap.InsertRow, keymap.EditCell, keymap.ToggleEnabled, keymap.ChangePassword, keymap.KillClient}
	if slices.Contains(writes, a) && cfg.IsReadOnly() {
		t.setStatus(readOnlyHint, true)
		return nil, true
	}
	if t.section == secESRoles {
		r, ok := t.selectedRole()
		switch a {
		case keymap.Open:
			if ok {
				msg := esOpenObjectMsg{conn: t.conn, key: objKey(objRole, r.Name), api: t.security}
				return func() tea.Msg { return msg }, true
			}
			return nil, true
		case keymap.KillClient:
			switch {
			case !ok:
			case r.Reserved:
				t.setStatus(r.Name+" is a reserved role", true)
			default:
				msg := esAskDeleteObjectMsg{conn: t.conn, key: objKey(objRole, r.Name), api: t.security}
				return func() tea.Msg { return msg }, true
			}
			return nil, true
		}
		return nil, false
	}
	if a == keymap.InsertRow {
		return openOverlay(overlay.NewUserForm(t.keys, t.security)), true
	}
	u, ok := t.selectedUser()
	if !slices.Contains(writes, a) || a == keymap.InsertRow {
		return nil, false
	}
	if !ok {
		return nil, true
	}
	if u.Reserved && a != keymap.ChangePassword {
		t.setStatus(u.Name+" is a reserved user", true)
		return nil, true
	}
	self := []string{}
	if cfg.User != "" && cfg.User == u.Name {
		self = append(self, "this is the user datavim is connected as")
	}
	id := t.id
	switch a {
	case keymap.EditCell:
		name := u.Name
		return openOverlay(overlay.NewPrompt("Roles of "+name, "Roles (comma-separated)", strings.Join(u.Roles, ", "), func(s string) tea.Msg {
			return esUserRolesMsg{tabID: id, name: name, roles: s}
		})), true
	case keymap.ToggleEnabled:
		if t.security == es.SecurityOpenSearch {
			t.setStatus("OpenSearch users cannot be disabled", true)
			return nil, true
		}
		op := "disable"
		if !u.Enabled {
			op = "enable"
		}
		path := es.UserPath(t.security, u.Name) + "/_" + op
		p := esPlan{Class: esx.Classify("PUT", path, nil), Count: -1, Method: "PUT", Path: path, Lines: append([]string{op + " user " + u.Name}, self...)}
		return confirmESCmd(cfg, p, esUserWriteMsg{tabID: id, op: op, user: u}, t.setStatus), true
	case keymap.ChangePassword:
		name := u.Name
		return openOverlay(overlay.NewSecret("New password for "+name, "Password", func(s string) tea.Msg {
			return esPasswordMsg{tabID: id, name: name, first: s}
		})), true
	case keymap.KillClient:
		path := es.UserPath(t.security, u.Name)
		p := esPlan{
			Class: esx.Classify("DELETE", path, nil), Count: -1, Method: "DELETE", Path: path, Expect: u.Name,
			Lines: append([]string{"delete user " + u.Name}, self...),
		}
		return confirmESCmd(cfg, p, esUserWriteMsg{tabID: id, op: "delete", user: u}, t.setStatus), true
	}
	return nil, false
}

// userFormed asks to create the user of the form; the confirmation shows
// no body: it would hold the password.
func (t *esServerTab) userFormed(m overlay.UserFormMsg) tea.Cmd {
	u := es.User{Name: m.Name, Roles: splitRoles(m.Roles), BackendRoles: splitRoles(m.BackendRoles), FullName: m.FullName, Email: m.Email}
	path := es.UserPath(t.security, u.Name)
	p := esPlan{
		Class: esx.Classify("PUT", path, nil), Count: -1, Method: "PUT", Path: path,
		Lines: []string{"create user " + u.Name + " with roles " + rolesText(u.Roles)},
	}
	cfg, _ := t.backend.Connection(t.conn)
	return confirmESCmd(cfg, p, esUserWriteMsg{tabID: t.id, op: "create", user: u, password: m.Password}, t.setStatus)
}

func splitRoles(s string) []string {
	var out []string
	for r := range strings.SplitSeq(s, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

func rolesText(r []string) string {
	if len(r) == 0 {
		return "none"
	}
	return strings.Join(r, ", ")
}

func (t *esServerTab) userRoles(m esUserRolesMsg) tea.Cmd {
	i := slices.IndexFunc(t.users, func(u es.User) bool { return u.Name == m.name })
	if i < 0 {
		return nil
	}
	u := t.users[i]
	old := rolesText(u.Roles)
	u.Roles = splitRoles(m.roles)
	path := es.UserPath(t.security, u.Name)
	method := "PUT"
	if t.security == es.SecurityOpenSearch {
		method = "PATCH"
	}
	p := esPlan{
		Class: esx.Classify(method, path, nil), Count: -1, Method: method, Path: path,
		Lines: []string{"roles of " + u.Name + ": " + old + " → " + rolesText(u.Roles)},
	}
	cfg, _ := t.backend.Connection(t.conn)
	return confirmESCmd(cfg, p, esUserWriteMsg{tabID: t.id, op: "roles", user: u}, t.setStatus)
}

// password asks for the password again, then to change it.
func (t *esServerTab) password(m esPasswordMsg) tea.Cmd {
	if !m.again {
		first, id, name := m.first, t.id, m.name
		return openOverlay(overlay.NewSecret("Again", "The new password for "+name+" again", func(s string) tea.Msg {
			return esPasswordMsg{tabID: id, name: name, first: first, second: s, again: true}
		}))
	}
	if err := overlay.CheckPassword(m.first, m.second); err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	path := es.UserPath(t.security, m.name)
	method := "PATCH"
	if t.security == es.SecurityES {
		path, method = path+"/_password", "POST"
	}
	p := esPlan{
		Class: esx.Classify(method, path, nil), Count: -1, Method: method, Path: path,
		Lines: []string{"change the password of " + m.name},
	}
	cfg, _ := t.backend.Connection(t.conn)
	return confirmESCmd(cfg, p, esUserWriteMsg{tabID: t.id, op: "password", user: es.User{Name: m.name}, password: m.first}, t.setStatus)
}

func (t *esServerTab) userWrite(m esUserWriteMsg) tea.Cmd {
	id, b, conn, api := t.id, t.backend, t.conn, t.security
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		err := withES(ctx, b, conn, func(d es.Doer) error {
			switch m.op {
			case "create":
				return es.CreateUser(ctx, d, api, m.user, m.password)
			case "roles":
				return es.UpdateUser(ctx, d, api, m.user)
			case "enable", "disable":
				return es.SetUserEnabled(ctx, d, api, m.user.Name, m.op == "enable")
			case "password":
				return es.ChangePassword(ctx, d, api, m.user.Name, m.password)
			}
			return es.DeleteUser(ctx, d, api, m.user.Name)
		})
		return esUserDoneMsg{tabID: id, conn: conn, op: m.op, name: m.user.Name, err: err}
	}
}

func (t *esServerTab) userDone(m esUserDoneMsg) tea.Cmd {
	if m.err != nil {
		t.setStatus(esReason(m.err), true)
		return nil
	}
	done := map[string]string{
		"create": "created user ", "roles": "updated roles of ", "enable": "enabled ", "disable": "disabled ",
		"password": "changed the password of ", "delete": "deleted user ",
	}[m.op]
	t.setStatus(done+m.name, false)
	status, isErr := t.status, t.statusE
	cmd := t.load()
	t.status, t.statusE = status, isErr
	return cmd
}

// securityUpdate handles the Users and Roles messages.
func (t *esServerTab) securityUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case esUserRolesMsg:
		return t.userRoles(m), true
	case esPasswordMsg:
		return t.password(m), true
	case esUserWriteMsg:
		return t.userWrite(m), true
	case esUserDoneMsg:
		return t.userDone(m), true
	}
	return nil, false
}
