package edit

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

func TestPlanKeepsBlobLiteral(t *testing.T) {
	cols := []db.ColumnInfo{{Name: "k", DatabaseType: "BLOB"}, {Name: "a", DatabaseType: "TEXT"}, {Name: "t", DatabaseType: "BLOB"}}
	rows := [][]db.Value{
		{{Text: "x'00FF'", Blob: true}, {Text: "old"}, {Text: "x'00ff'"}},
	}
	tb := New(cols, rows, nil)
	_ = tb.SetCell(0, 1, db.Value{Text: "new"})
	at := tb.InsertAfter(0)
	_ = tb.SetCell(at, 0, db.Value{Text: "x'01'", Blob: true})
	stmts, _, err := Plan(tb, Target{Dialect: sqlx.SQLite, Table: db.TableRef{Name: "t"}, Key: Key{Kind: PrimaryKey, Columns: []string{"k"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`UPDATE "t" SET "a" = 'new' WHERE "k" = x'00FF'`,
		`INSERT INTO "t" ("k") VALUES (x'01')`,
	}
	if diff := cmp.Diff(want, stmts); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestSetCellTellsBlobFromText(t *testing.T) {
	cols := []db.ColumnInfo{{Name: "a", DatabaseType: "BLOB"}}
	tb := New(cols, [][]db.Value{{{Text: "x'00'", Blob: true}}}, nil)
	_ = tb.SetCell(0, 0, db.Value{Text: "x'00'"})
	if tb.Row(0).State != Modified {
		t.Fatal("text equal to a BLOB's literal counts as no change")
	}
}
