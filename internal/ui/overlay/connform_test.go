package overlay

import (
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
