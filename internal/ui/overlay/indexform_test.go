package overlay

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/ui/keymap"
)

func TestIndexForm(t *testing.T) {
	f := NewIndexForm(keymap.Default())
	typeIn(t, f, "orders-v2")
	send(t, f, "tab", "backspace")
	typeIn(t, f, "3")
	send(t, f, "tab", "backspace")
	typeIn(t, f, "0")
	send(t, f, "tab")
	typeIn(t, f, `{"properties":{"a":{"type":"keyword"}}}`)
	_, msg := send(t, f, "ctrl+s")
	got, ok := msg.(IndexFormMsg)
	if !ok {
		t.Fatalf("msg = %#v\n%s", msg, ansi.Strip(f.View(100, 40)))
	}
	n := got.Index
	if n.Name != "orders-v2" || n.Shards != 3 || n.Replicas != 0 || string(n.Mappings) != `{"properties":{"a":{"type":"keyword"}}}` || n.Settings != nil {
		t.Errorf("index = %+v", n)
	}
	for _, tc := range []struct{ name, err string }{
		{"Orders", "lowercase"},
		{"a b", "may not contain"},
		{"_x", "may not start"},
		{"", "name is required"},
	} {
		f := NewIndexForm(keymap.Default())
		typeIn(t, f, tc.name)
		if o, msg := send(t, f, "ctrl+s"); o == nil || msg != nil || !strings.Contains(ansi.Strip(f.View(100, 40)), tc.err) {
			t.Errorf("%q: %v", tc.name, ansi.Strip(f.View(100, 40)))
		}
	}
	f = NewIndexForm(keymap.Default())
	typeIn(t, f, "x")
	send(t, f, "tab", "tab", "tab")
	typeIn(t, f, "{bad")
	if _, msg := send(t, f, "ctrl+s"); msg != nil || !strings.Contains(ansi.Strip(f.View(100, 40)), "mappings") {
		t.Errorf("bad json accepted")
	}
}
