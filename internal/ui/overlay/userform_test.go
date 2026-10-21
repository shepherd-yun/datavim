package overlay

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
)

func fillUser(t *testing.T, f *UserForm, name, pw, again, roles string) {
	t.Helper()
	typeIn(t, f, name)
	send(t, f, "tab")
	typeIn(t, f, pw)
	send(t, f, "tab")
	typeIn(t, f, again)
	send(t, f, "tab")
	typeIn(t, f, roles)
}

func TestUserForm(t *testing.T) {
	f := NewUserForm(keymap.Default(), es.SecurityES)
	fillUser(t, f, "bob", "secret1", "secret1", "viewer, editor")
	send(t, f, "tab")
	typeIn(t, f, "Bob B")
	send(t, f, "tab")
	typeIn(t, f, "bob@x")
	if v := ansi.Strip(f.View(100, 40)); strings.Contains(v, "secret1") || !strings.Contains(v, "Full name") || strings.Contains(v, "Backend roles") {
		t.Errorf("view:\n%s", v)
	}
	_, msg := send(t, f, "ctrl+s")
	got, ok := msg.(UserFormMsg)
	if !ok {
		t.Fatalf("msg %#v\n%s", msg, ansi.Strip(f.View(100, 40)))
	}
	if got.Name != "bob" || got.Password != "secret1" || got.Roles != "viewer, editor" || got.FullName != "Bob B" || got.Email != "bob@x" {
		t.Errorf("got %+v", got)
	}

	f = NewUserForm(keymap.Default(), es.SecurityOpenSearch)
	if v := ansi.Strip(f.View(100, 40)); !strings.Contains(v, "Backend roles") || strings.Contains(v, "Email") {
		t.Errorf("opensearch view:\n%s", v)
	}

	for _, tc := range []struct{ name, pw, again, err string }{
		{"", "secret1", "secret1", "username is required"},
		{"a b", "secret1", "secret1", "spaces"},
		{"bob", "abc", "abc", "at least 6"},
		{"bob", "secret1", "secret2", "passwords differ"},
	} {
		f := NewUserForm(keymap.Default(), es.SecurityES)
		fillUser(t, f, tc.name, tc.pw, tc.again, "")
		if o, msg := send(t, f, "ctrl+s"); o == nil || msg != nil || !strings.Contains(ansi.Strip(f.View(100, 40)), tc.err) {
			t.Errorf("%+v: %s", tc, ansi.Strip(f.View(100, 40)))
		}
	}
}
