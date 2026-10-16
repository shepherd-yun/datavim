package overlay

import (
	"maps"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
)

func TestConnFormSubmit(t *testing.T) {
	f := NewConnForm(keymap.Default())
	keys := []string{
		"p", "g", "tab", // name
		"space", "tab", // driver -> postgres
		"tab",                     // host keeps localhost
		"6", "5", "4", "3", "tab", // port
		"a", "p", "p", "tab", // user
		"s", "3", "tab", // password
		"s", "h", "o", "p", "tab", // database
		"b", "a", "s", "t", "i", "o", "n", "tab", // ssh host
		"l", "l", "tab", // env -> prod
		"right", "enter", // save -> no, submit
	}
	o, msg := send(t, f, keys...)
	if o != nil {
		t.Fatalf("form still open:\n%s", ansi.Strip(f.View(80, 30)))
	}
	got, ok := msg.(ConnFormMsg)
	if !ok {
		t.Fatalf("msg = %#v", msg)
	}
	want := config.Connection{Name: "pg", Driver: "postgres", Host: "localhost", Port: 6543, User: "app", Database: "shop", Env: config.EnvProd}
	if got.Conn.Name != want.Name || got.Conn.Driver != want.Driver || got.Conn.Host != want.Host ||
		got.Conn.Port != want.Port || got.Conn.User != want.User || got.Conn.Database != want.Database || got.Conn.Env != want.Env {
		t.Fatalf("conn = %+v", got.Conn)
	}
	if got.Conn.SSH != "bastion" {
		t.Fatalf("ssh = %q", got.Conn.SSH)
	}
	if got.Password != "s3" || got.Save {
		t.Fatalf("password=%q save=%v", got.Password, got.Save)
	}
}

func TestConnFormValidation(t *testing.T) {
	f := NewConnForm(keymap.Default())
	o, msg := send(t, f, "ctrl+s")
	if o == nil || msg != nil || !strings.Contains(ansi.Strip(o.View(80, 30)), "name is required") {
		t.Fatal("empty name is rejected")
	}
	o, msg = send(t, NewConnForm(keymap.Default()), "x", "tab", "tab", "tab", "a", "b", "ctrl+s")
	if o == nil || msg != nil || !strings.Contains(ansi.Strip(o.View(80, 30)), "port") {
		t.Fatal("non-numeric port is rejected")
	}
	if o, _ := send(t, NewConnForm(keymap.Default()), "esc"); o != nil {
		t.Fatal("esc cancels")
	}
}

func TestConnFormMasksPassword(t *testing.T) {
	f := NewConnForm(keymap.Default())
	send(t, f, "tab", "tab", "tab", "tab", "tab", "s", "e", "c", "r", "e", "t")
	if strings.Contains(ansi.Strip(f.View(80, 30)), "secret") {
		t.Fatal("password must be masked")
	}
}

func TestEditConnForm(t *testing.T) {
	orig := config.Connection{
		Name: "pg", Driver: "postgres", Host: "db", Port: 6543, User: "app",
		Database: "shop", SSH: "bastion", Env: config.EnvProd,
	}
	f := NewEditConnForm(keymap.Default(), orig)
	v := ansi.Strip(f.View(80, 30))
	for _, want := range []string{"Edit connection", "pg", "postgres", "db", "6543", "app", "shop", "bastion", "prod", "empty keeps"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q:\n%s", want, v)
		}
	}
	// name: append "2"; leave the rest; save
	_, msg := send(t, f, "2", "ctrl+s")
	got, ok := msg.(ConnFormMsg)
	if !ok {
		t.Fatalf("msg = %#v", msg)
	}
	if got.Original != "pg" || got.Conn.Name != "pg2" || got.Conn.Driver != "postgres" || got.Conn.Port != 6543 ||
		got.Conn.SSH != "bastion" || got.Conn.Env != config.EnvProd || got.Password != "" {
		t.Fatalf("got = %+v", got)
	}
	if _, msg := send(t, NewConnForm(keymap.Default()), "x", "ctrl+s"); msg.(ConnFormMsg).Original != "" {
		t.Fatal("a new connection has no original name")
	}
}

func TestConnFormSaveKeyFollowsTheKeymap(t *testing.T) {
	if v := ansi.Strip(NewConnForm(keymap.Default()).View(100, 40)); !strings.Contains(v, "<ctrl+s> save") {
		t.Fatalf("footer:\n%s", v)
	}
	k, err := keymap.Custom("", map[keymap.Context]map[string]string{keymap.Popup: {"<ctrl+s>": "", "<ctrl+w>": "form.save"}})
	if err != nil {
		t.Fatal(err)
	}
	f := NewConnForm(k)
	if v := ansi.Strip(f.View(100, 40)); !strings.Contains(v, "<ctrl+w> save") || strings.Contains(v, "ctrl+s") {
		t.Fatalf("footer:\n%s", v)
	}
	if o, msg := send(t, f, "x", "ctrl+s"); o == nil || msg != nil {
		t.Fatal("unbound ctrl+s does nothing")
	}
	if _, msg := send(t, f, "ctrl+w"); msg.(ConnFormMsg).Conn.Name != "x" {
		t.Fatalf("ctrl+w saves: %v", msg)
	}
}

func TestConnFormSQLServerEncrypt(t *testing.T) {
	f := NewConnForm(keymap.Default())
	if v := ansi.Strip(f.View(80, 30)); strings.Contains(v, "Encrypt") || strings.Contains(v, "Trust cert") {
		t.Fatalf("mysql shows the SQL Server fields:\n%s", v)
	}
	keys := []string{
		"m", "s", "tab", // name
		"space", "space", "tab", // driver -> sqlserver
		"tab", "tab", "tab", "tab", // host, port, user, password
		"c", "r", "m", "tab", // database
		"right", "right", "right", "tab", // encrypt: default -> true -> false -> disable
		"right", // trust cert -> yes
		"ctrl+s",
	}
	o, msg := send(t, f, keys...)
	if o != nil {
		t.Fatalf("form still open:\n%s", ansi.Strip(f.View(80, 30)))
	}
	got := msg.(ConnFormMsg).Conn
	if got.Driver != "sqlserver" || got.Port != 1433 || got.Database != "crm" {
		t.Fatalf("conn = %+v", got)
	}
	want := map[string]string{"encrypt": "disable", "TrustServerCertificate": "true"}
	if !maps.Equal(got.Params, want) {
		t.Fatalf("params = %v, want %v", got.Params, want)
	}

	// default and no write nothing; other params survive an edit.
	orig := config.Connection{Name: "ms", Driver: "sqlserver", Params: map[string]string{"encrypt": "disable", "tlsmin": "1.0"}}
	f = NewEditConnForm(keymap.Default(), orig)
	if v := ansi.Strip(f.View(80, 30)); !strings.Contains(v, "disable") {
		t.Fatalf("edit form shows the encrypt setting:\n%s", v)
	}
	_, msg = send(t, f, "ctrl+s")
	if p := msg.(ConnFormMsg).Conn.Params; !maps.Equal(p, map[string]string{"encrypt": "disable", "tlsmin": "1.0"}) {
		t.Fatalf("edited params = %v", p)
	}
	f = NewEditConnForm(keymap.Default(), orig)
	f.cur = ffEncrypt
	_, msg = send(t, f, "right", "ctrl+s") // disable -> default
	if p := msg.(ConnFormMsg).Conn.Params; !maps.Equal(p, map[string]string{"tlsmin": "1.0"}) {
		t.Fatalf("default drops encrypt: %v", p)
	}
}

func TestConnFormSQLServerParamsIgnoreCase(t *testing.T) {
	orig := config.Connection{Name: "ms", Driver: "sqlserver", Params: map[string]string{"Encrypt": "DISABLE", "trustservercertificate": "true"}}
	_, msg := send(t, NewEditConnForm(keymap.Default(), orig), "ctrl+s")
	want := map[string]string{"encrypt": "disable", "TrustServerCertificate": "true"}
	if p := msg.(ConnFormMsg).Conn.Params; !maps.Equal(p, want) {
		t.Fatalf("params = %v, want %v", p, want)
	}
}

func TestEditConnFormKeepsHiddenSettings(t *testing.T) {
	off := false
	orig := config.Connection{Name: "pg", Driver: "postgres", ReadOnly: &off, Params: map[string]string{"sslmode": "disable"}}
	_, msg := send(t, NewEditConnForm(keymap.Default(), orig), "ctrl+s")
	got := msg.(ConnFormMsg).Conn
	if got.ReadOnly == nil || *got.ReadOnly || !maps.Equal(got.Params, orig.Params) {
		t.Fatalf("readonly %v, params %v", got.ReadOnly, got.Params)
	}
}

func TestConnFormOracleServiceLabel(t *testing.T) {
	f := NewConnForm(keymap.Default())
	o, msg := send(t, f, "o", "tab", "left", "left", "tab") // driver: mysql -> oracle (before redis)
	if o == nil || msg != nil {
		t.Fatal("form closed")
	}
	v := ansi.Strip(f.View(80, 30))
	if !strings.Contains(v, "Service") || strings.Contains(v, "Database") || !strings.Contains(v, "FREEPDB1") {
		t.Fatalf("oracle form:\n%s", v)
	}
	if strings.Contains(v, "Encrypt") {
		t.Fatalf("SQL Server fields on oracle:\n%s", v)
	}
	_, msg = send(t, f, "ctrl+s")
	if c := msg.(ConnFormMsg).Conn; c.Driver != "oracle" || c.Port != 1521 {
		t.Fatalf("conn = %+v", c)
	}
	if v := ansi.Strip(NewConnForm(keymap.Default()).View(80, 30)); !strings.Contains(v, "Database") || strings.Contains(v, "FREEPDB1") {
		t.Fatalf("mysql form:\n%s", v)
	}
}

func TestConnFormRedis(t *testing.T) {
	f := NewConnForm(keymap.Default())
	o, msg := send(t, f, "r", "tab", "left", "tab") // driver: mysql -> redis (last)
	if o == nil || msg != nil {
		t.Fatal("form closed")
	}
	v := ansi.Strip(f.View(80, 30))
	if !strings.Contains(v, "DB ") || strings.Contains(v, "Database") || strings.Contains(v, "Encrypt") {
		t.Fatalf("redis form:\n%s", v)
	}
	_, msg = send(t, f, "tab", "tab", "tab", "tab", "2", "ctrl+s") // port, user, password -> DB
	c := msg.(ConnFormMsg).Conn
	if c.Driver != "redis" || c.Port != 6379 || c.Database != "2" {
		t.Fatalf("conn = %+v", c)
	}
}
