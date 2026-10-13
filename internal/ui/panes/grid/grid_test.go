package grid

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func rs() *db.ResultSet {
	return &db.ResultSet{
		Columns: []db.ColumnInfo{{Name: "id"}, {Name: "name"}, {Name: "email"}, {Name: "note"}},
		Rows: [][]db.Value{
			{{Text: "1"}, {Text: "alice"}, {Text: "a@x.com"}, {Text: "hi"}},
			{{Text: "2"}, {Text: "李雷"}, {Text: ""}, {Null: true}},
			{{Text: "3"}, {Text: "bob"}, {Null: true}, {Text: "multi\nline\x1b[31m"}},
		},
	}
}

func view(m *Model, focused bool) []string {
	return strings.Split(ansi.Strip(m.View(focused)), "\n")
}

func TestViewRendersHeaderRowsAndNull(t *testing.T) {
	m := New(rs(), 40)
	m.SetSize(80, 10)
	lines := view(m, true)
	if !strings.HasPrefix(lines[0], "id │ name  │ email   │ note") {
		t.Fatalf("header = %q", lines[0])
	}
	if !strings.Contains(lines[3], "李雷") || !strings.Contains(lines[3], "NULL") {
		t.Fatalf("row 2 = %q", lines[3])
	}
	if !strings.Contains(lines[4], "multi↵line�[31m") {
		t.Fatalf("control characters must be neutralised: %q", lines[4])
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w != 80 {
			t.Errorf("line %q has width %d, want 80", l, w)
		}
	}
}

func TestColumnWidthIsCapped(t *testing.T) {
	m := New(&db.ResultSet{
		Columns: []db.ColumnInfo{{Name: "body"}},
		Rows:    [][]db.Value{{{Text: strings.Repeat("x", 500)}}},
	}, 10)
	m.SetSize(80, 5)
	if got := view(m, false)[2]; !strings.HasPrefix(got, "xxxxxxxxx…") {
		t.Fatalf("row = %q", got)
	}
}

func TestCursorAndHorizontalScroll(t *testing.T) {
	m := New(rs(), 40)
	m.SetSize(14, 10) // room for about two columns
	m.Move(0, 3)
	if col, _, _ := m.Cell(); col.Name != "note" {
		t.Fatalf("cell column = %s", col.Name)
	}
	if !strings.Contains(view(m, true)[0], "note") {
		t.Fatalf("cursor column must scroll into view: %q", view(m, true)[0])
	}
	m.LineStart()
	if !strings.HasPrefix(view(m, true)[0], "id") {
		t.Fatal("scrolls back to the first column")
	}
	m.Move(99, 0)
	if _, v, _ := m.Cell(); v.Text != "3" {
		t.Fatalf("clamped to last row, got %q", v.Text)
	}
	m.Top()
	m.LineEnd()
	if _, v, _ := m.Cell(); v.Text != "hi" {
		t.Fatalf("top/line end = %q", v.Text)
	}
}

func TestVerticalScroll(t *testing.T) {
	big := &db.ResultSet{Columns: []db.ColumnInfo{{Name: "n"}}}
	for _, s := range []string{"r0", "r1", "r2", "r3", "r4", "r5"} {
		big.Rows = append(big.Rows, []db.Value{{Text: s}})
	}
	m := New(big, 40)
	m.SetSize(10, 4) // header + rule + 2 rows
	m.Bottom()
	got := strings.Join(view(m, true), "|")
	if !strings.Contains(got, "r5") || strings.Contains(got, "r0") {
		t.Fatalf("view = %s", got)
	}
	m.HalfPage(-1)
	if _, v, _ := m.Cell(); v.Text != "r4" {
		t.Fatalf("half page up = %s", v.Text)
	}
}

func TestEmptyAndTinySizes(t *testing.T) {
	m := New(nil, 40)
	m.SetSize(20, 5)
	if !strings.Contains(ansi.Strip(m.View(true)), "no columns") {
		t.Fatal("nil result")
	}
	if _, _, ok := m.Cell(); ok {
		t.Fatal("no cell in empty grid")
	}
	m.SetResult(&db.ResultSet{Columns: []db.ColumnInfo{{Name: "a"}}})
	if !strings.Contains(ansi.Strip(m.View(true)), "0 rows") {
		t.Fatal("zero rows")
	}
	m.SetResult(rs())
	for _, size := range [][2]int{{0, 0}, {1, 1}, {3, 2}, {5, 1}} {
		m.SetSize(size[0], size[1])
		m.Move(1, 1)
		_ = m.View(true) // must not panic
	}
}
