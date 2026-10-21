package overlay

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
)

type colPick struct {
	name      string
	structure bool
}

func testColumns(current string) *Columns {
	def := "0"
	cols := []db.Column{
		{Name: "id", Type: "bigint", PrimaryKey: true},
		{Name: "name", Type: "text", Nullable: true, Comment: "display name"},
		{Name: "score", Type: "int", Default: &def, Comment: "ranking points"},
	}
	return NewColumns(keymap.Default(), "users", cols, current, func(name string, structure bool) tea.Msg {
		return colPick{name, structure}
	})
}

func TestColumnsShowsEveryColumn(t *testing.T) {
	v := ansi.Strip(testColumns("").View(100, 30))
	for _, want := range []string{"users", "PK", "id", "bigint", "NOT NULL", "display name", "score", "3 columns", "<ctrl+o> full structure"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}

func TestColumnsFiltersAndPicks(t *testing.T) {
	o, msg := send(t, testColumns(""), "s", "c", "enter")
	if o != nil || msg != (colPick{name: "score"}) {
		t.Fatalf("o=%v msg=%v", o, msg)
	}
	// the comment is searched too
	if _, msg := send(t, testColumns(""), "r", "a", "n", "k", "enter"); msg != (colPick{name: "score"}) {
		t.Fatalf("msg=%v", msg)
	}
	if _, msg := send(t, testColumns(""), "down", "ctrl+o"); msg != (colPick{name: "name", structure: true}) {
		t.Fatalf("ctrl+o asks for the structure: %v", msg)
	}
	o, msg = send(t, testColumns(""), "z", "z", "enter")
	if o == nil || msg != nil {
		t.Fatal("enter with no matches does nothing")
	}
	if !strings.Contains(ansi.Strip(o.View(100, 30)), "no matches") {
		t.Fatal("empty state shown")
	}
	if _, msg := send(t, testColumns(""), "z", "z", "ctrl+o"); msg != (colPick{structure: true}) {
		t.Fatalf("ctrl+o opens the structure even with no match: %v", msg)
	}
	if o, _ := send(t, testColumns(""), "esc"); o != nil {
		t.Fatal("esc closes")
	}
}

func TestColumnsStartsOnTheCurrentColumn(t *testing.T) {
	if _, msg := send(t, testColumns("name"), "enter"); msg != (colPick{name: "name"}) {
		t.Fatalf("msg=%v", msg)
	}
	if _, msg := send(t, testColumns("gone"), "enter"); msg != (colPick{name: "id"}) {
		t.Fatalf("an unknown current column starts at the top: %v", msg)
	}
}

func TestColumnsTinySize(_ *testing.T) {
	c := testColumns("")
	for _, s := range [][2]int{{0, 0}, {5, 2}, {20, 4}} {
		_ = c.View(s[0], s[1])
	}
}

func TestColumnsStructureKeyFollowsTheKeymap(t *testing.T) {
	k, err := keymap.Custom("", map[keymap.Context]map[string]string{keymap.Popup: {"<ctrl+o>": "", "<ctrl+t>": "structure.full"}})
	if err != nil {
		t.Fatal(err)
	}
	c := NewColumns(k, "users", []db.Column{{Name: "id"}}, "", func(name string, structure bool) tea.Msg {
		return colPick{name, structure}
	})
	if !strings.Contains(ansi.Strip(c.View(100, 30)), "<ctrl+t> full structure") {
		t.Fatalf("footer:\n%s", ansi.Strip(c.View(100, 30)))
	}
	if _, msg := send(t, c, "ctrl+t"); msg != (colPick{name: "id", structure: true}) {
		t.Fatalf("ctrl+t = %v", msg)
	}
	if o, msg := send(t, c, "ctrl+o"); o == nil || msg != nil {
		t.Fatal("unbound ctrl+o does nothing")
	}
}

func TestColumnsWithoutAStructureKey(t *testing.T) {
	k, err := keymap.Custom("", map[keymap.Context]map[string]string{keymap.Popup: {"<ctrl+o>": ""}})
	if err != nil {
		t.Fatal(err)
	}
	c := NewColumns(k, "users", []db.Column{{Name: "id"}}, "", nil)
	if v := ansi.Strip(c.View(100, 30)); strings.Contains(v, "structure") {
		t.Fatalf("no hint for an unbound key (not even the global <space>S):\n%s", v)
	}
}

func TestResultColumnsPickByIndex(t *testing.T) {
	cols := []db.Column{
		{Name: "id", Type: "bigint", PrimaryKey: true},
		{Name: "id", Type: "int4"},
		{Name: "total", Type: "numeric"},
	}
	type picked struct {
		i         int
		structure bool
	}
	c := NewResultColumns(keymap.Default(), "result", cols, []bool{true, false, false}, 1, func(i int, structure bool) tea.Msg {
		return picked{i, structure}
	})
	v := ansi.Strip(c.View(100, 30))
	if strings.Count(v, "NOT NULL") != 1 {
		t.Fatalf("only the catalog column shows its nullability:\n%s", v)
	}
	if _, msg := send(t, c, "enter"); msg != (picked{i: 1}) {
		t.Fatalf("starts on the current column: %v", msg)
	}
	c = NewResultColumns(keymap.Default(), "result", cols, nil, 0, func(i int, structure bool) tea.Msg {
		return picked{i, structure}
	})
	if _, msg := send(t, c, "t", "o", "enter"); msg != (picked{i: 2}) {
		t.Fatalf("filtered pick: %v", msg)
	}
	c = NewResultColumns(keymap.Default(), "result", cols, nil, 0, func(i int, structure bool) tea.Msg {
		return picked{i, structure}
	})
	if _, msg := send(t, c, "z", "z", "ctrl+o"); msg != (picked{i: -1, structure: true}) {
		t.Fatalf("no match: %v", msg)
	}
}
