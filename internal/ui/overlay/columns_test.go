package overlay

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/db"
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
	return NewColumns("users", cols, current, func(name string, structure bool) tea.Msg {
		return colPick{name, structure}
	})
}

func TestColumnsShowsEveryColumn(t *testing.T) {
	v := ansi.Strip(testColumns("").View(100, 30))
	for _, want := range []string{"users", "PK", "id", "bigint", "NOT NULL", "display name", "score", "3 columns", "ctrl+s"} {
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
	if _, msg := send(t, testColumns(""), "down", "ctrl+s"); msg != (colPick{name: "name", structure: true}) {
		t.Fatalf("ctrl+s asks for the structure: %v", msg)
	}
	o, msg = send(t, testColumns(""), "z", "z", "enter")
	if o == nil || msg != nil {
		t.Fatal("enter with no matches does nothing")
	}
	if !strings.Contains(ansi.Strip(o.View(100, 30)), "no matches") {
		t.Fatal("empty state shown")
	}
	if _, msg := send(t, testColumns(""), "z", "z", "ctrl+s"); msg != (colPick{structure: true}) {
		t.Fatalf("ctrl+s opens the structure even with no match: %v", msg)
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
