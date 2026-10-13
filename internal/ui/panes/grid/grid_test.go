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

func TestScrollColumns(t *testing.T) {
	m := New(rs(), 40)
	m.SetSize(14, 10) // id(2) + name(5) fit; email(7) does not
	m.ScrollColumns(1)
	if !strings.HasPrefix(view(m, true)[0], "name") {
		t.Fatalf("zl scrolls one column: %q", view(m, true)[0])
	}
	if col, _, _ := m.Cell(); col.Name != "name" {
		t.Fatalf("cursor pulled into view, got %s", col.Name)
	}
	m.ScrollColumns(1)
	if col, _, _ := m.Cell(); col.Name != "email" {
		t.Fatalf("cursor follows, got %s", col.Name)
	}
	m.ScrollColumns(-5)
	if !strings.HasPrefix(view(m, true)[0], "id") {
		t.Fatalf("clamped at the first column: %q", view(m, true)[0])
	}
	if col, _, _ := m.Cell(); col.Name != "name" {
		t.Fatalf("cursor kept inside the view, got %s", col.Name)
	}
	m.ScrollColumns(99)
	if col, _, _ := m.Cell(); col.Name != "note" {
		t.Fatalf("clamped at the last column, got %s", col.Name)
	}
	New(nil, 40).ScrollColumns(1) // must not panic
}

func TestScrollHalf(t *testing.T) {
	r := &db.ResultSet{}
	row := []db.Value{}
	for _, name := range []string{"c0", "c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9"} {
		r.Columns = append(r.Columns, db.ColumnInfo{Name: name})
		row = append(row, db.Value{Text: "aaaa"})
	}
	r.Rows = [][]db.Value{row}
	m := New(r, 40)
	m.SetSize(30, 10) // each column is 4 wide plus a 3-wide separator: half a view is two columns
	m.ScrollHalf(1)
	if !strings.HasPrefix(view(m, true)[0], "c2") {
		t.Fatalf("zL scrolls half a view: %q", view(m, true)[0])
	}
	if col, _, _ := m.Cell(); col.Name != "c2" {
		t.Fatalf("cursor pulled into view, got %s", col.Name)
	}
	m.ScrollHalf(1)
	m.ScrollHalf(-1)
	if !strings.HasPrefix(view(m, true)[0], "c2") {
		t.Fatalf("zH scrolls back half a view: %q", view(m, true)[0])
	}
	m.ScrollHalf(-1)
	m.ScrollHalf(-1)
	if !strings.HasPrefix(view(m, true)[0], "c0") {
		t.Fatalf("clamped at the first column: %q", view(m, true)[0])
	}
	for range 10 {
		m.ScrollHalf(1)
	}
	if col, _, _ := m.Cell(); col.Name != "c9" {
		t.Fatalf("clamped at the last column, got %s", col.Name)
	}

	wide := New(rs(), 40)
	wide.SetSize(3, 10) // narrower than any column: still moves one column
	wide.ScrollHalf(1)
	if col, _, _ := wide.Cell(); col.Name != "name" {
		t.Fatalf("at least one column, got %s", col.Name)
	}
	New(nil, 40).ScrollHalf(1) // must not panic
}

func TestFind(t *testing.T) {
	m := New(rs(), 40)
	m.SetSize(80, 10)
	if !m.Find("BOB", true) {
		t.Fatal("case-insensitive match")
	}
	if _, v, _ := m.Cell(); v.Text != "bob" {
		t.Fatalf("at %q", v.Text)
	}
	if !m.Find("a", true) { // wraps to row 0: "alice"
		t.Fatal("wraps around")
	}
	if _, v, _ := m.Cell(); v.Text != "alice" {
		t.Fatalf("at %q", v.Text)
	}
	// cursor is on alice (0,1); searching backwards wraps to the last match, a@x.com (0,2)
	if !m.Find("a", false) {
		t.Fatal("backward search")
	}
	if _, v, _ := m.Cell(); v.Text != "a@x.com" {
		t.Fatalf("backward at %q", v.Text)
	}
	if m.Find("NULL", true) {
		t.Fatal("NULL cells never match")
	}
	if m.Find("", true) || New(nil, 40).Find("x", true) {
		t.Fatal("empty query or grid")
	}
}

func TestRowAndReplaceResult(t *testing.T) {
	m := New(rs(), 40)
	m.SetSize(80, 10)
	m.Move(1, 1)
	row, ok := m.Row()
	if !ok || row[1].Text != "李雷" {
		t.Fatalf("Row = %v, %v", row, ok)
	}
	bigger := rs()
	bigger.Rows = append(bigger.Rows, []db.Value{{Text: "4"}, {Text: "dan"}, {Null: true}, {Null: true}})
	m.ReplaceResult(bigger)
	if _, v, _ := m.Cell(); v.Text != "李雷" {
		t.Fatalf("cursor kept, at %q", v.Text)
	}
	if m.Result() != bigger || m.RowCount() != 4 {
		t.Fatal("data replaced")
	}
	m.Bottom()
	m.ReplaceResult(rs())
	if _, v, _ := m.Cell(); v.Text != "bob" {
		t.Fatalf("cursor clamped to the last row, at %q", v.Text)
	}
}

func TestSelectColumn(t *testing.T) {
	m := New(&db.ResultSet{
		Columns: []db.ColumnInfo{{Name: "id"}, {Name: "name"}, {Name: "Email"}},
		Rows:    [][]db.Value{{{Text: "1"}, {Text: "a"}, {Text: "a@x"}}},
	}, 40)
	m.SetSize(20, 5)
	if !m.SelectColumn("email") {
		t.Fatal("matches ignoring case")
	}
	if col, _, _ := m.Cell(); col.Name != "Email" {
		t.Fatalf("cursor on %q", col.Name)
	}
	if m.SelectColumn("nope") || New(nil, 40).SelectColumn("id") {
		t.Fatal("unknown column or no data")
	}
}
