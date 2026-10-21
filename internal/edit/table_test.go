package edit

import (
	"errors"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
)

func cols() []db.ColumnInfo {
	return []db.ColumnInfo{{Name: "id", DatabaseType: "BIGINT"}, {Name: "name", DatabaseType: "VARCHAR"}}
}

func page() [][]db.Value {
	return [][]db.Value{
		{{Text: "1"}, {Text: "alice"}},
		{{Text: "2"}, {Null: true}},
	}
}

func TestSetCellStagesAndUnstages(t *testing.T) {
	tb := New(cols(), page(), nil)
	if err := tb.SetCell(0, 1, db.Value{Text: "ann"}); err != nil {
		t.Fatal(err)
	}
	r := tb.Row(0)
	if r.State != Modified || !r.Set[1] || r.Set[0] || r.Values[1].Text != "ann" || r.Orig[1].Text != "alice" {
		t.Fatalf("row = %+v", r)
	}
	if tb.Changes() != 1 {
		t.Fatalf("changes = %d", tb.Changes())
	}
	_ = tb.SetCell(0, 1, db.Value{Text: "alice"})
	if r := tb.Row(0); r.State != Unchanged || r.Set[1] || tb.Changes() != 0 {
		t.Fatalf("setting the server value back unstages the cell: %+v", r)
	}
	_ = tb.SetCell(1, 1, db.Value{Text: ""})
	if r := tb.Row(1); r.State != Modified || !r.Orig[1].Null {
		t.Fatalf("'' is a change from NULL: %+v", r)
	}
	if got := tb.Result().Rows[1][1]; got != (db.Value{Text: ""}) {
		t.Fatalf("Result shows the staged value: %+v", got)
	}
}

func TestSetCellWithoutChangeIsNotAStep(t *testing.T) {
	tb := New(cols(), page(), nil)
	_ = tb.SetCell(0, 1, db.Value{Text: "alice"})
	if _, ok := tb.Undo(); ok {
		t.Fatal("nothing changed, nothing to undo")
	}
}

func TestInsertDeleteAndUndo(t *testing.T) {
	tb := New(cols(), page(), nil)
	at := tb.InsertAfter(0)
	if at != 1 || tb.Len() != 3 || tb.Row(1).State != Inserted || tb.Row(1).Orig != nil {
		t.Fatalf("at %d, len %d, row %+v", at, tb.Len(), tb.Row(1))
	}
	if v := tb.Row(1).Values; !v[0].Null || !v[1].Null || tb.Row(1).Set[0] || tb.Row(1).Set[1] {
		t.Fatalf("a new row starts with every cell at its default: %+v", tb.Row(1))
	}
	_ = tb.SetCell(1, 1, db.Value{Text: "new"})
	_ = tb.SetCell(1, 0, db.Value{Null: true})
	if r := tb.Row(1); !r.Set[0] || !r.Set[1] || r.State != Inserted {
		t.Fatalf("an explicit NULL is not the default: %+v", r)
	}
	tb.Delete(2) // the server's second row
	if tb.Row(2).State != Deleted {
		t.Fatalf("row 2 = %+v", tb.Row(2))
	}
	if err := tb.SetCell(2, 1, db.Value{Text: "x"}); !errors.Is(err, ErrDeleted) {
		t.Fatalf("editing a deleted row: %v", err)
	}
	tb.Delete(2) // already marked: nothing happens, no undo step
	if tb.Changes() != 2 {
		t.Fatalf("changes = %d", tb.Changes())
	}
	tb.Delete(1) // a new row is simply dropped
	if tb.Len() != 2 || tb.Changes() != 1 {
		t.Fatalf("len %d changes %d", tb.Len(), tb.Changes())
	}

	steps := []struct{ len, changes, row int }{
		{3, 2, 1}, // the new row is back
		{3, 1, 2}, // row 2 is no longer deleted
		{3, 1, 1}, // the explicit NULL is gone
		{3, 1, 1}, // "new" is gone; the row is still new
		{2, 0, 1}, // the new row is gone
	}
	for i, s := range steps {
		row, ok := tb.Undo()
		if !ok || tb.Len() != s.len || tb.Changes() != s.changes || row != s.row {
			t.Fatalf("undo %d: ok %v len %d changes %d row %d", i+1, ok, tb.Len(), tb.Changes(), row)
		}
		if i == 0 && tb.Row(1).Values[1].Text != "new" {
			t.Fatalf("undo brings the row back with its values: %+v", tb.Row(1))
		}
	}
	if _, ok := tb.Undo(); ok {
		t.Fatal("nothing left to undo")
	}
}

func TestInsertIntoEmptyPage(t *testing.T) {
	tb := New(cols(), nil, nil)
	if at := tb.InsertAfter(-1); at != 0 || tb.Len() != 1 {
		t.Fatalf("at %d len %d", at, tb.Len())
	}
	if rs := tb.Result(); len(rs.Columns) != 2 || len(rs.Rows) != 1 {
		t.Fatalf("result = %+v", rs)
	}
}

func TestResetKeepsRowIDs(t *testing.T) {
	tb := New(cols(), page(), []string{"(0,1)", "(0,2)"})
	if tb.Row(1).ID != "(0,2)" {
		t.Fatalf("id = %q", tb.Row(1).ID)
	}
	_ = tb.SetCell(0, 1, db.Value{Text: "x"})
	tb.InsertAfter(1)
	tb.Reset()
	if tb.Changes() != 0 || tb.Len() != 2 || tb.Row(0).Values[1].Text != "alice" || tb.Row(0).ID != "(0,1)" {
		t.Fatalf("reset: %+v", tb.Row(0))
	}
	if _, ok := tb.Undo(); ok {
		t.Fatal("reset clears the undo history")
	}
}

func TestUndoIsBounded(t *testing.T) {
	tb := New(cols(), page(), nil)
	for i := range maxUndo + 5 {
		_ = tb.SetCell(0, 1, db.Value{Text: string(rune('a' + i%26))})
	}
	n := 0
	for {
		if _, ok := tb.Undo(); !ok {
			break
		}
		n++
	}
	if n != maxUndo {
		t.Fatalf("undo steps = %d, want %d", n, maxUndo)
	}
}

func TestTableAppend(t *testing.T) {
	tb := New(cols(), [][]db.Value{
		{{Text: "1"}, {Text: "a"}}, {{Text: "2"}, {Text: "b"}}, {{Text: "3"}, {Text: "c"}},
	}, nil)
	_ = tb.SetCell(0, 1, db.Value{Text: "z"})
	tb.Append([][]db.Value{{{Text: "4"}, {Text: "d"}}, {{Text: "5"}, {Text: "e"}}}, nil)
	if tb.Len() != 5 || tb.Changes() != 1 {
		t.Fatalf("len = %d, changes = %d", tb.Len(), tb.Changes())
	}
	if r := tb.Row(3); r.State != Unchanged || r.Values[1].Text != "d" || r.Orig[1].Text != "d" {
		t.Fatalf("appended row = %+v", r)
	}
	if at, ok := tb.Undo(); !ok || at != 0 || tb.Row(0).Values[1].Text != "a" {
		t.Fatalf("undo = %d %v, row 0 = %+v", at, ok, tb.Row(0))
	}
	_ = tb.SetCell(4, 1, db.Value{Text: "q"})
	tb.Reset()
	if tb.Len() != 5 || tb.Changes() != 0 || tb.Row(4).Values[1].Text != "e" {
		t.Fatalf("reset: len = %d, changes = %d, row 4 = %+v", tb.Len(), tb.Changes(), tb.Row(4))
	}
}
