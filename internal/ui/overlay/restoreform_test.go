package overlay

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/ui/keymap"
)

func TestRestoreForm(t *testing.T) {
	f := NewRestoreForm(keymap.Default(), "s1", []string{"orders", "users", "carts", "logs"})
	v := ansi.Strip(f.View(100, 40))
	for _, want := range []string{"(.+)", "restored-$1", "orders → restored-orders", "users → restored-users", "carts → restored-carts", "… and 1 more"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	_, msg := send(t, f, "ctrl+s")
	got, ok := msg.(RestoreFormMsg)
	if !ok {
		t.Fatalf("msg %#v", msg)
	}
	if got.Indices != "" || got.RenamePattern != "(.+)" || got.RenameReplacement != "restored-$1" || got.IncludeAliases {
		t.Errorf("defaults %+v", got)
	}

	f = NewRestoreForm(keymap.Default(), "s1", []string{"orders"})
	send(t, f, "tab")
	for range 4 {
		send(t, f, "backspace")
	}
	typeIn(t, f, "(")
	if o, msg := send(t, f, "ctrl+s"); o == nil || msg != nil || !strings.Contains(ansi.Strip(f.View(100, 40)), "rename pattern") {
		t.Errorf("bad pattern accepted: %v", ansi.Strip(f.View(100, 40)))
	}

	f = NewRestoreForm(keymap.Default(), "s1", []string{"orders", "users"})
	typeIn(t, f, "users")
	send(t, f, "tab")
	for range 4 {
		send(t, f, "backspace")
	}
	send(t, f, "tab", "tab", "space")
	if v := ansi.Strip(f.View(100, 40)); !strings.Contains(v, "users → users") || strings.Contains(v, "orders →") {
		t.Errorf("preview:\n%s", v)
	}
	_, msg = send(t, f, "ctrl+s")
	if got := msg.(RestoreFormMsg); got.Indices != "users" || got.RenamePattern != "" || !got.IncludeAliases {
		t.Errorf("got %+v", got)
	}
}
