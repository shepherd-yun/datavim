package overlay

import (
	"maps"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
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
		"l", "l", "tab", // read-only: auto -> yes -> no
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
	if got.Conn.ReadOnly == nil || *got.Conn.ReadOnly {
		t.Fatalf("readonly = %v, want false", got.Conn.ReadOnly)
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
	orig := config.Connection{Name: "pg", Driver: "postgres", Params: map[string]string{"sslmode": "disable"}}
	_, msg := send(t, NewEditConnForm(keymap.Default(), orig), "ctrl+s")
	if got := msg.(ConnFormMsg).Conn; !maps.Equal(got.Params, orig.Params) {
		t.Fatalf("params %v", got.Params)
	}
}

func TestConnFormReadOnly(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		name string
		ro   *bool
		env  config.Env
		keys []string
		show string
		want *bool
	}{
		{"auto stays unset", nil, config.EnvDev, nil, "‹ auto › (off)", nil},
		{"auto follows prod", nil, config.EnvProd, nil, "‹ auto › (on)", nil},
		{"explicit off is kept", &off, config.EnvProd, nil, "‹ no ›", &off},
		{"explicit on is kept", &on, config.EnvDev, nil, "‹ yes ›", &on},
		{"off back to auto", &off, config.EnvDev, []string{"right"}, "‹ auto ›", nil},
		{"auto to on", nil, config.EnvDev, []string{"right"}, "‹ yes ›", &on},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := NewEditConnForm(keymap.Default(), config.Connection{Name: "pg", Driver: "postgres", Env: tc.env, ReadOnly: tc.ro})
			f.cur = ffReadOnly
			send(t, f, tc.keys...)
			if v := ansi.Strip(f.View(80, 30)); !strings.Contains(v, "Read-only") || !strings.Contains(v, tc.show) {
				t.Fatalf("want %q:\n%s", tc.show, v)
			}
			_, msg := send(t, f, "ctrl+s")
			got := msg.(ConnFormMsg).Conn.ReadOnly
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("readonly = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConnFormOracleServiceLabel(t *testing.T) {
	f := NewConnForm(keymap.Default())
	o, msg := send(t, f, "o", "tab", "left", "left", "left", "left", "tab") // driver: mysql -> oracle (before redis, kafka, elasticsearch)
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
	o, msg := send(t, f, "r", "tab", "left", "left", "left", "tab") // driver: mysql -> redis (before kafka, elasticsearch)
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

func TestConnFormKafka(t *testing.T) {
	f := NewConnForm(keymap.Default())
	if v := ansi.Strip(f.View(80, 30)); strings.Contains(v, "SASL") || strings.Contains(v, "Brokers") {
		t.Fatalf("mysql shows the kafka fields:\n%s", v)
	}
	o, msg := send(t, f, "k", "tab", "left", "left", "tab") // driver: mysql -> kafka (before elasticsearch)
	if o == nil || msg != nil {
		t.Fatal("form closed")
	}
	v := ansi.Strip(f.View(80, 30))
	if !strings.Contains(v, "Brokers") || strings.Contains(v, "Database") || strings.Contains(v, "Encrypt") ||
		!strings.Contains(v, "SASL") || !strings.Contains(v, "TLS") {
		t.Fatalf("kafka form:\n%s", v)
	}
	// host, port, user, password -> SASL (Database is hidden)
	_, msg = send(t, f, "tab", "tab", "a", "p", "p", "tab", "tab",
		"left", "tab", // SASL: none -> scram-sha-512 (last)
		"left", // TLS: off -> on, skip verify (last)
		"ctrl+s")
	c := msg.(ConnFormMsg).Conn
	want := map[string]string{"sasl": "scram-sha-512", "tls": "true", "tls_skip_verify": "true"}
	if c.Driver != "kafka" || c.Port != 9092 || c.User != "app" || !maps.Equal(c.Params, want) {
		t.Fatalf("conn = %+v", c)
	}

	orig := config.Connection{
		Name: "k", Driver: "kafka", User: "app",
		Params: map[string]string{"sasl": "plain", "tls": "true", "client_id": "me"},
	}
	f = NewEditConnForm(keymap.Default(), orig)
	if v := ansi.Strip(f.View(80, 30)); !strings.Contains(v, "‹ plain ›") || !strings.Contains(v, "‹ on ›") {
		t.Fatalf("edit form shows the kafka settings:\n%s", v)
	}
	_, msg = send(t, f, "ctrl+s")
	if p := msg.(ConnFormMsg).Conn.Params; !maps.Equal(p, orig.Params) {
		t.Fatalf("edited params = %v", p)
	}
	f = NewEditConnForm(keymap.Default(), orig)
	f.cur = ffSASL
	send(t, f, "right") // plain -> scram-sha-256
	f.cur = ffTLS
	_, msg = send(t, f, "left", "ctrl+s") // on -> off
	if p := msg.(ConnFormMsg).Conn.Params; !maps.Equal(p, map[string]string{"sasl": "scram-sha-256", "client_id": "me"}) {
		t.Fatalf("edited params = %v", p)
	}
	// kafka params stay off other drivers
	f = NewEditConnForm(keymap.Default(), config.Connection{Name: "m", Driver: "mysql"})
	_, msg = send(t, f, "ctrl+s")
	if p := msg.(ConnFormMsg).Conn.Params; p != nil {
		t.Fatalf("mysql params = %v", p)
	}
}

func TestConnFormES(t *testing.T) {
	f := NewConnForm(keymap.Default())
	o, msg := send(t, f, "e", "tab", "left", "tab") // driver: mysql -> elasticsearch (last)
	if o == nil || msg != nil {
		t.Fatal("form closed")
	}
	v := ansi.Strip(f.View(80, 30))
	if !strings.Contains(v, "URLs") || strings.Contains(v, "Database") || strings.Contains(v, "SASL") ||
		!strings.Contains(v, "TLS") || !strings.Contains(v, "Auth") {
		t.Fatalf("elasticsearch form:\n%s", v)
	}
	// host, port, user, password -> TLS (Database is hidden)
	_, msg = send(t, f, "tab", "tab", "tab", "tab",
		"left", "tab", // TLS: off -> on, skip verify (last)
		"right", // Auth: basic -> api key
		"ctrl+s")
	c := msg.(ConnFormMsg).Conn
	want := map[string]string{"tls": "true", "tls_skip_verify": "true", "auth": "apikey"}
	if c.Driver != "elasticsearch" || c.Port != 9200 || !maps.Equal(c.Params, want) {
		t.Fatalf("conn = %+v", c)
	}

	orig := config.Connection{Name: "e", Driver: "elasticsearch", Params: map[string]string{"auth": "apikey", "page_size": "50"}}
	f = NewEditConnForm(keymap.Default(), orig)
	if v := ansi.Strip(f.View(80, 30)); !strings.Contains(v, "‹ api key ›") || !strings.Contains(v, "‹ off ›") {
		t.Fatalf("edit form shows the elasticsearch settings:\n%s", v)
	}
	f.cur = ffAuth
	_, msg = send(t, f, "left", "ctrl+s") // api key -> basic
	if p := msg.(ConnFormMsg).Conn.Params; !maps.Equal(p, map[string]string{"page_size": "50"}) {
		t.Fatalf("edited params = %v", p)
	}
}
