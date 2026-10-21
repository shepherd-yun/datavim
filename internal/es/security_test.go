package es

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

var (
	esInfo = Info{Distribution: "elasticsearch", Version: "8.15.0", Major: 8, Minor: 15}
	osInfo = Info{Distribution: "opensearch", Version: "2.17.0", Major: 2, Minor: 17}
)

func TestSecurity(t *testing.T) {
	st := func(s int, body string) *fakeDoer {
		return &fakeDoer{fn: func(Request) (Response, error) { return Response{Status: s, Body: []byte(body)}, nil }}
	}
	cases := []struct {
		name string
		info Info
		d    *fakeDoer
		want SecurityAPI
		err  bool
	}{
		{"es off", esInfo, st(200, `{"features":{"security":{"available":true,"enabled":false}}}`), SecurityNone, false},
		{"es on", esInfo, st(200, `{"features":{"security":{"available":true,"enabled":true}}}`), SecurityES, false},
		{"es no xpack", esInfo, st(400, `{"error":"no handler found for uri [/_xpack] and method [GET]"}`), SecurityNone, false},
		{"os 200", osInfo, st(200, `{"user_name":"admin"}`), SecurityOpenSearch, false},
		{"os 403", osInfo, st(403, `{"status":"FORBIDDEN","message":"no permissions"}`), SecurityOpenSearch, false},
		{"os 401", osInfo, st(401, `Unauthorized`), SecurityOpenSearch, false},
		{"os none", osInfo, st(400, `{"error":"no handler found for uri [/_plugins/_security/api/account] and method [GET]"}`), SecurityNone, false},
		{"es 500", esInfo, st(500, `{"error":{"type":"x","reason":"boom"},"status":500}`), SecurityNone, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Security(ctx, tc.d, tc.info)
			if got != tc.want || (err != nil) != tc.err {
				t.Errorf("got %v %v", got, err)
			}
			path := tc.d.requests()[0].Path
			if tc.info.OpenSearch() && path != "_plugins/_security/api/account" || !tc.info.OpenSearch() && path != "_xpack?categories=features" {
				t.Errorf("path %s", path)
			}
		})
	}
}

func TestUsers(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET _security/user": ok(`{
		"elastic":{"username":"elastic","roles":["superuser"],"full_name":null,"email":null,"metadata":{"_reserved":true},"enabled":true},
		"bob":{"username":"bob","roles":["viewer","editor"],"full_name":"Bob B","email":"bob@x","metadata":{"team":"a"},"enabled":false}
	}`)}}
	got, err := Users(ctx, d, SecurityES)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	b, e := got[0], got[1]
	if b.Name != "bob" || !reflect.DeepEqual(b.Roles, []string{"viewer", "editor"}) || b.FullName != "Bob B" || b.Email != "bob@x" || b.Enabled || b.Reserved || string(b.Metadata) != `{"team":"a"}` {
		t.Errorf("bob = %+v", b)
	}
	if e.Name != "elastic" || !e.Reserved || !e.Enabled {
		t.Errorf("elastic = %+v", e)
	}

	d = &fakeDoer{table: map[string]Response{"GET _plugins/_security/api/internalusers": ok(`{
		"admin":{"hash":"","reserved":true,"hidden":false,"backend_roles":["admin"],"attributes":{},"opendistro_security_roles":[],"static":false},
		"kirk":{"hash":"","reserved":false,"hidden":false,"backend_roles":["captain"],"opendistro_security_roles":["maintenance_staff"],"static":false},
		"ghost":{"hash":"","reserved":false,"hidden":true,"backend_roles":[],"opendistro_security_roles":[]}
	}`)}}
	got, err = Users(ctx, d, SecurityOpenSearch)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "admin" || !got[0].Reserved || !got[0].Enabled ||
		got[1].Name != "kirk" || !reflect.DeepEqual(got[1].Roles, []string{"maintenance_staff"}) || !reflect.DeepEqual(got[1].BackendRoles, []string{"captain"}) {
		t.Errorf("opensearch = %+v", got)
	}
}

func reqLine(r Request) string { return r.Method + " " + r.Path }

func TestCreateUser(t *testing.T) {
	d := &fakeDoer{fn: func(Request) (Response, error) { return ok(`{}`), nil }}
	u := User{Name: "bob", Roles: []string{"viewer"}, BackendRoles: []string{"be"}, FullName: "Bob", Email: "b@x"}
	if err := CreateUser(ctx, d, SecurityES, u, "pw123456"); err != nil {
		t.Fatal(err)
	}
	if err := CreateUser(ctx, d, SecurityOpenSearch, u, "pw123456"); err != nil {
		t.Fatal(err)
	}
	r := d.requests()
	if reqLine(r[0]) != "PUT _security/user/bob" || !sameJSON(r[0].Body, []byte(`{"password":"pw123456","roles":["viewer"],"full_name":"Bob","email":"b@x"}`)) {
		t.Errorf("es: %s %s", reqLine(r[0]), r[0].Body)
	}
	if reqLine(r[1]) != "PUT _plugins/_security/api/internalusers/bob" || !sameJSON(r[1].Body, []byte(`{"password":"pw123456","opendistro_security_roles":["viewer"],"backend_roles":["be"]}`)) {
		t.Errorf("opensearch: %s %s", reqLine(r[1]), r[1].Body)
	}
}

func TestUpdateUser(t *testing.T) {
	d := &fakeDoer{fn: func(Request) (Response, error) { return ok(`{}`), nil }}
	u := User{Name: "bob", Roles: []string{"a", "c"}, BackendRoles: []string{"be"}, FullName: "Bob", Email: "b@x", Metadata: json.RawMessage(`{"team":"a"}`)}
	if err := UpdateUser(ctx, d, SecurityES, u); err != nil {
		t.Fatal(err)
	}
	if err := UpdateUser(ctx, d, SecurityOpenSearch, u); err != nil {
		t.Fatal(err)
	}
	r := d.requests()
	if reqLine(r[0]) != "PUT _security/user/bob" || strings.Contains(string(r[0].Body), "password") ||
		!sameJSON(r[0].Body, []byte(`{"roles":["a","c"],"full_name":"Bob","email":"b@x","metadata":{"team":"a"}}`)) {
		t.Errorf("es: %s %s", reqLine(r[0]), r[0].Body)
	}
	if reqLine(r[1]) != "PATCH _plugins/_security/api/internalusers/bob" ||
		!sameJSON(r[1].Body, []byte(`[{"op":"add","path":"/opendistro_security_roles","value":["a","c"]},{"op":"add","path":"/backend_roles","value":["be"]}]`)) {
		t.Errorf("opensearch: %s %s", reqLine(r[1]), r[1].Body)
	}
}

func TestChangePassword(t *testing.T) {
	d := &fakeDoer{fn: func(Request) (Response, error) { return ok(`{}`), nil }}
	if err := ChangePassword(ctx, d, SecurityES, "bob", "newpw1"); err != nil {
		t.Fatal(err)
	}
	if err := ChangePassword(ctx, d, SecurityOpenSearch, "bob", "newpw1"); err != nil {
		t.Fatal(err)
	}
	r := d.requests()
	if reqLine(r[0]) != "POST _security/user/bob/_password" || !sameJSON(r[0].Body, []byte(`{"password":"newpw1"}`)) {
		t.Errorf("es: %s %s", reqLine(r[0]), r[0].Body)
	}
	if reqLine(r[1]) != "PATCH _plugins/_security/api/internalusers/bob" || !sameJSON(r[1].Body, []byte(`[{"op":"add","path":"/password","value":"newpw1"}]`)) {
		t.Errorf("opensearch: %s %s", reqLine(r[1]), r[1].Body)
	}
}

func TestSetUserEnabled(t *testing.T) {
	d := &fakeDoer{fn: func(Request) (Response, error) { return ok(`{}`), nil }}
	if err := SetUserEnabled(ctx, d, SecurityES, "bob", false); err != nil {
		t.Fatal(err)
	}
	if err := SetUserEnabled(ctx, d, SecurityES, "bob", true); err != nil {
		t.Fatal(err)
	}
	r := d.requests()
	if reqLine(r[0]) != "PUT _security/user/bob/_disable" || reqLine(r[1]) != "PUT _security/user/bob/_enable" {
		t.Errorf("reqs %v %v", reqLine(r[0]), reqLine(r[1]))
	}
}

func TestSetUserEnabledOpenSearch(t *testing.T) {
	d := &fakeDoer{}
	if err := SetUserEnabled(ctx, d, SecurityOpenSearch, "bob", false); !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v", err)
	}
	if len(d.requests()) != 0 {
		t.Error("sent a request")
	}
}

func TestDeleteUserAndRole(t *testing.T) {
	d := &fakeDoer{fn: func(Request) (Response, error) { return ok(`{}`), nil }}
	_ = DeleteUser(ctx, d, SecurityES, "bob")
	_ = DeleteUser(ctx, d, SecurityOpenSearch, "bob")
	_ = DeleteRole(ctx, d, SecurityES, "r")
	_ = DeleteRole(ctx, d, SecurityOpenSearch, "r")
	_ = PutRole(ctx, d, SecurityES, "r", json.RawMessage(`{}`))
	_ = PutRole(ctx, d, SecurityOpenSearch, "r", json.RawMessage(`{}`))
	var got []string
	for _, r := range d.requests() {
		got = append(got, reqLine(r))
	}
	want := []string{
		"DELETE _security/user/bob", "DELETE _plugins/_security/api/internalusers/bob",
		"DELETE _security/role/r", "DELETE _plugins/_security/api/roles/r",
		"PUT _security/role/r", "PUT _plugins/_security/api/roles/r",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

func TestRoles(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET _security/role": ok(`{
		"superuser":{"cluster":["all"],"indices":[{"names":["*"],"privileges":["all"]}],"applications":[],"run_as":["*"],"metadata":{"_reserved":true},"transient_metadata":{"enabled":true}},
		"reader":{"cluster":["monitor"],"indices":[{"names":["a"],"privileges":["read"]},{"names":["b"],"privileges":["read"]}],"metadata":{"owner":"x"},"transient_metadata":{"enabled":true}}
	}`)}}
	got, err := Roles(ctx, d, SecurityES)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "reader" || got[0].Indices != 2 || !reflect.DeepEqual(got[0].Cluster, []string{"monitor"}) || got[0].Reserved ||
		got[1].Name != "superuser" || !got[1].Reserved {
		t.Fatalf("es: %+v", got)
	}
	for _, r := range got {
		if strings.Contains(string(r.Body), "transient_metadata") || strings.Contains(string(r.Body), "_reserved") {
			t.Errorf("body %s", r.Body)
		}
	}
	if !strings.Contains(string(got[0].Body), `"owner"`) {
		t.Errorf("metadata dropped: %s", got[0].Body)
	}

	d = &fakeDoer{table: map[string]Response{"GET _plugins/_security/api/roles": ok(`{
		"all_access":{"reserved":true,"hidden":false,"static":true,"cluster_permissions":["*"],"index_permissions":[{"index_patterns":["*"],"allowed_actions":["*"]}],"tenant_permissions":[]},
		"hid":{"reserved":false,"hidden":true,"cluster_permissions":[]},
		"mine":{"reserved":false,"hidden":false,"static":false,"cluster_permissions":["cluster_monitor"],"index_permissions":[]}
	}`)}}
	got, err = Roles(ctx, d, SecurityOpenSearch)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "all_access" || !got[0].Reserved || got[0].Indices != 1 || got[1].Name != "mine" || got[1].Reserved {
		t.Fatalf("opensearch: %+v", got)
	}
	for _, r := range got {
		if b := string(r.Body); strings.Contains(b, "reserved") || strings.Contains(b, "hidden") || strings.Contains(b, "static") {
			t.Errorf("body %s", b)
		}
	}
}
