package overlay

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
)

func TestConnFormSubmit(t *testing.T) {
	f := NewConnForm()
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
	f := NewConnForm()
	o, msg := send(t, f, "ctrl+s")
	if o == nil || msg != nil || !strings.Contains(ansi.Strip(o.View(80, 30)), "name is required") {
		t.Fatal("empty name is rejected")
	}
	o, msg = send(t, NewConnForm(), "x", "tab", "tab", "tab", "a", "b", "ctrl+s")
	if o == nil || msg != nil || !strings.Contains(ansi.Strip(o.View(80, 30)), "port") {
		t.Fatal("non-numeric port is rejected")
	}
	if o, _ := send(t, NewConnForm(), "esc"); o != nil {
		t.Fatal("esc cancels")
	}
}

func TestConnFormMasksPassword(t *testing.T) {
	f := NewConnForm()
	send(t, f, "tab", "tab", "tab", "tab", "tab", "s", "e", "c", "r", "e", "t")
	if strings.Contains(ansi.Strip(f.View(80, 30)), "secret") {
		t.Fatal("password must be masked")
	}
}
