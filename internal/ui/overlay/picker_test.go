package overlay

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestPickerFiltersAndPicks(t *testing.T) {
	items := []string{"select * from users", "update orders set x = 1", "SELECT name FROM Users"}
	pick := func(i int) tea.Msg { return okMsg{i} }
	p := NewPicker("SQL history", items, pick)
	if v := ansi.Strip(p.View(80, 20)); !strings.Contains(v, "update orders") {
		t.Fatalf("view:\n%s", v)
	}
	o, msg := send(t, p, "u", "s", "e", "r", "s", "down", "enter")
	if o != nil || msg != (okMsg{2}) {
		t.Fatalf("o=%v msg=%v", o, msg)
	}
	o, msg = send(t, NewPicker("x", items, pick), "z", "z", "enter")
	if o == nil || msg != nil {
		t.Fatal("enter with no matches does nothing")
	}
	if !strings.Contains(ansi.Strip(o.View(80, 20)), "no matches") {
		t.Fatal("empty state shown")
	}
	if o, _ := send(t, NewPicker("x", items, pick), "esc"); o != nil {
		t.Fatal("esc closes")
	}
	if o, msg := send(t, NewPicker("x", items, pick), "ctrl+n", "ctrl+n", "ctrl+n", "ctrl+p", "enter"); o != nil || msg != (okMsg{1}) {
		t.Fatalf("navigation clamps: %v", msg)
	}
	if o, msg := send(t, NewPicker("x", items, pick), "ctrl+j", "ctrl+j", "ctrl+k", "enter"); o != nil || msg != (okMsg{1}) {
		t.Fatalf("ctrl+j/k move like ctrl+n/p: %v", msg)
	}
}

func TestPickerTinySize(_ *testing.T) {
	p := NewPicker("x", []string{"a", "b"}, func(int) tea.Msg { return nil })
	for _, s := range [][2]int{{0, 0}, {5, 2}, {20, 4}} {
		_ = p.View(s[0], s[1])
	}
}
