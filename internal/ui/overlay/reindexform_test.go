package overlay

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/ui/keymap"
)

func TestReindexForm(t *testing.T) {
	f := NewReindexForm(keymap.Default(), "orders")
	_, msg := send(t, f, "ctrl+s")
	if got, ok := msg.(ReindexFormMsg); !ok || got != (ReindexFormMsg{Dest: "orders-reindex", OpType: "index", Conflicts: "abort"}) {
		t.Fatalf("default = %#v", msg)
	}
	f = NewReindexForm(keymap.Default(), "orders")
	for range 20 {
		send(t, f, "backspace")
	}
	typeIn(t, f, "orders-v2")
	send(t, f, "tab")
	typeIn(t, f, "status:paid")
	send(t, f, "tab", "right", "tab", "l")
	_, msg = send(t, f, "ctrl+s")
	if got, ok := msg.(ReindexFormMsg); !ok || got != (ReindexFormMsg{Dest: "orders-v2", Query: "status:paid", OpType: "create", Conflicts: "proceed"}) {
		t.Fatalf("msg = %#v", msg)
	}
	for _, tc := range []struct{ dest, query, err string }{
		{"orders", "", "is the source"},
		{"logs-*", "", "may not contain"},
		{"Orders2", "", "lowercase"},
		{"x", "{bad", "query"},
	} {
		f := NewReindexForm(keymap.Default(), "orders")
		for range 20 {
			send(t, f, "backspace")
		}
		typeIn(t, f, tc.dest)
		send(t, f, "tab")
		typeIn(t, f, tc.query)
		if o, msg := send(t, f, "ctrl+s"); o == nil || msg != nil || !strings.Contains(ansi.Strip(f.View(120, 40)), tc.err) {
			t.Errorf("%+v: %s", tc, ansi.Strip(f.View(120, 40)))
		}
	}
}
