package overlay

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/ui/keymap"
)

func TestSnapshotForm(t *testing.T) {
	f := NewSnapshotForm(keymap.Default(), "repo1", "snapshot-2026.10.22-153000")
	if v := ansi.Strip(f.View(100, 40)); !strings.Contains(v, "snapshot-2026.10.22-153000") || !strings.Contains(v, "repo1") {
		t.Errorf("view:\n%s", v)
	}
	send(t, f, "tab")
	typeIn(t, f, "orders, logs-* ,")
	_, msg := send(t, f, "ctrl+s")
	got, ok := msg.(SnapshotFormMsg)
	if !ok {
		t.Fatalf("msg = %#v\n%s", msg, ansi.Strip(f.View(100, 40)))
	}
	if got.Repo != "repo1" || got.Name != "snapshot-2026.10.22-153000" || !slices.Equal(got.Indices, []string{"orders", "logs-*"}) {
		t.Errorf("got %+v", got)
	}

	f = NewSnapshotForm(keymap.Default(), "repo1", "s")
	if _, msg := send(t, f, "enter", "enter"); msg.(SnapshotFormMsg).Indices != nil {
		t.Errorf("empty indices: %+v", msg)
	}
	for _, tc := range []struct{ name, err string }{
		{"", "name is required"},
		{"Snap", "lowercase"},
		{"a b", "may not contain"},
		{"a,b", "may not contain"},
		{"a*", "may not contain"},
	} {
		f := NewSnapshotForm(keymap.Default(), "repo1", "")
		typeIn(t, f, tc.name)
		if o, msg := send(t, f, "ctrl+s"); o == nil || msg != nil || !strings.Contains(ansi.Strip(f.View(100, 40)), tc.err) {
			t.Errorf("%q: %v", tc.name, ansi.Strip(f.View(100, 40)))
		}
	}
}
