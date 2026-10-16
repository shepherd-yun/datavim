package overlay

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type pickedMsg int

func testMenu() *Menu {
	return NewMenu("users", []MenuItem{
		{Keys: []string{"enter"}, Label: "<enter>", Desc: "Open data"},
		{Keys: []string{"e"}, Label: "e", Desc: "Modify table"},
		{Keys: []string{"d", "d"}, Label: "dd", Desc: "Drop table"},
		{Keys: []string{"space", "c"}, Label: "<space>c", Desc: "Open console"},
		{Desc: "Unbound"},
	}, func(i int) tea.Msg { return pickedMsg(i) })
}

func TestMenuRunsAnItemByItsKeys(t *testing.T) {
	for _, tc := range []struct {
		keys []string
		want pickedMsg
	}{
		{[]string{"e"}, 1},
		{[]string{"d", "d"}, 2},
		{[]string{"space", "c"}, 3},
		{[]string{"d", "e"}, 1}, // a broken sequence starts over with its last key
		{[]string{"x", "e"}, 1}, // unbound keys are ignored
		{[]string{"enter"}, 0},
		{[]string{"j", "j", "j", "j", "enter"}, 4},
		{[]string{"down", "up", "enter"}, 0},
	} {
		o, msg := send(t, testMenu(), tc.keys...)
		if o != nil || msg != tc.want {
			t.Errorf("%v: open=%v msg=%v, want %v", tc.keys, o != nil, msg, tc.want)
		}
	}
}

func TestMenuEnterRunsTheHighlightedItem(t *testing.T) {
	// enter is the first item's own key, but it picks the cursor's item
	o, msg := send(t, testMenu(), "j", "enter")
	if o != nil || msg != pickedMsg(1) {
		t.Fatalf("open=%v msg=%v", o != nil, msg)
	}
}

func TestMenuWaitsForTheRestOfASequence(t *testing.T) {
	o, msg := send(t, testMenu(), "d")
	if o == nil || msg != nil {
		t.Fatalf("open=%v msg=%v", o != nil, msg)
	}
	if v := ansi.Strip(o.View(80, 24)); !strings.Contains(v, "d…") {
		t.Fatalf("the pending key is not shown:\n%s", v)
	}
	// esc drops the pending key first, then closes
	o, _ = send(t, o, "esc")
	if o == nil {
		t.Fatal("esc with a pending key closed the menu")
	}
	if o, msg = send(t, o, "esc"); o != nil || msg != nil {
		t.Fatalf("open=%v msg=%v", o != nil, msg)
	}
}

func TestMenuClosesWithoutPicking(t *testing.T) {
	for _, k := range []string{"esc", "q"} {
		if o, msg := send(t, testMenu(), k); o != nil || msg != nil {
			t.Errorf("%s: open=%v msg=%v", k, o != nil, msg)
		}
	}
}

func TestMenuView(t *testing.T) {
	v := ansi.Strip(testMenu().View(80, 24))
	for _, want := range []string{"users", "<enter>", "Open data", "dd", "Drop table", "Unbound", "esc"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q:\n%s", want, v)
		}
	}
}
