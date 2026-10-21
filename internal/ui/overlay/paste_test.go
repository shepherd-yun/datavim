package overlay

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/ui/keymap"
)

// paste delivers a terminal paste (bracketed paste) to o.
func paste(t *testing.T, o Overlay, text string) {
	t.Helper()
	p, ok := o.(Paster)
	if !ok {
		t.Fatalf("%T does not accept pasted text", o)
	}
	p.Paste(text)
}

func TestPasteIntoConnForm(t *testing.T) {
	f := NewConnForm(keymap.Default())
	paste(t, f, "pg")
	send(t, f, "tab")
	paste(t, f, "postgres") // a choice field ignores pasted text
	send(t, f, "tab", "ctrl+u")
	paste(t, f, "db.example.com\n")
	send(t, f, "tab", "tab")
	paste(t, f, "app")
	send(t, f, "tab")
	paste(t, f, "p@ss w0rd!#\r\n") // password managers often add a newline
	_, msg := send(t, f, "ctrl+s")
	got, ok := msg.(ConnFormMsg)
	if !ok {
		t.Fatalf("msg = %#v", msg)
	}
	if got.Conn.Name != "pg" || got.Conn.Driver != "mysql" || got.Conn.Host != "db.example.com" || got.Conn.User != "app" {
		t.Fatalf("conn = %+v", got.Conn)
	}
	if got.Password != "p@ss w0rd!#" {
		t.Fatalf("password = %q", got.Password)
	}
}

func TestPasteIntoPasswordAndSecret(t *testing.T) {
	p := NewPassword("local")
	paste(t, p, "s3cr3t\n")
	if _, msg := send(t, p, "enter"); msg.(PasswordSubmittedMsg).Password != "s3cr3t" {
		t.Fatalf("msg = %#v", msg)
	}
	type got struct{ v string }
	s := NewSecret("SSH key", "Passphrase:", func(v string) tea.Msg { return got{v} })
	paste(t, s, "pa\tss\n")
	if _, msg := send(t, s, "enter"); msg != (got{"pass"}) {
		t.Fatalf("control characters are dropped: %#v", msg)
	}
}

func TestPasteIntoConfirmPickerAndCmdLine(t *testing.T) {
	c := NewConfirm("t", nil, "users", okMsg{1})
	paste(t, c, "users")
	if _, msg := send(t, c, "enter"); msg != (okMsg{1}) {
		t.Fatalf("confirm: %#v", msg)
	}

	items := []string{"select * from users", "update orders set x = 1"}
	p := NewPicker("SQL history", items, func(i int) tea.Msg { return okMsg{i} })
	paste(t, p, "orders")
	if _, msg := send(t, p, "enter"); msg != (okMsg{1}) {
		t.Fatalf("picker filters on pasted text: %#v", msg)
	}

	cl := NewCmdLine()
	paste(t, cl, "set noro")
	if _, msg := send(t, cl, "enter"); msg != (CommandMsg{Text: "set noro"}) {
		t.Fatalf("cmdline: %#v", msg)
	}

	pr := NewPrompt("Export", "File:", "a", func(s string) tea.Msg { return okMsg{len(s)} })
	paste(t, pr, "bc")
	if _, msg := send(t, pr, "enter"); msg != (okMsg{3}) {
		t.Fatalf("prompt appends: %#v", msg)
	}
}
