package edit

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

func TestLocateSQLiteRowID(t *testing.T) {
	cols := []db.Column{{Name: "a", Nullable: true}}
	if got := ChooseKey(sqlx.SQLite, cols, nil, false); got.Kind != RowID {
		t.Fatalf("got %+v", got)
	}
	if got := ChooseKey(sqlx.SQLite, cols, nil, true); got.Kind != NoKey {
		t.Fatalf("view: got %+v", got)
	}
}

func TestLocateSQLiteShadowedRowID(t *testing.T) {
	cols := []db.Column{{Name: "a"}, {Name: "_ROWID_"}}
	if got := ChooseKey(sqlx.SQLite, cols, nil, false); got.Kind != NoKey {
		t.Fatalf("got %+v, want no key", got)
	}
	// a primary key does not depend on the row id
	cols[0].PrimaryKey = true
	if got := ChooseKey(sqlx.SQLite, cols, nil, false); got.Kind != PrimaryKey {
		t.Fatalf("got %+v, want the primary key", got)
	}
	// other engines may have a column of that name
	if got := ChooseKey(sqlx.Postgres, []db.Column{{Name: "_rowid_"}}, nil, false); got.Kind != RowID {
		t.Fatalf("postgres: got %+v", got)
	}
}

func TestSQLiteEmptyInsert(t *testing.T) {
	cols := []db.ColumnInfo{{Name: "a", DatabaseType: "ANY"}}
	tb := New(cols, nil, nil)
	tb.InsertAfter(-1)
	stmts, _, err := Plan(tb, Target{Dialect: sqlx.SQLite, Table: db.TableRef{Name: "t"}, Key: Key{Kind: RowID}})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{`INSERT INTO "t" DEFAULT VALUES`}, stmts); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestPlanSQLiteRowID(t *testing.T) {
	cols := []db.ColumnInfo{{Name: "a", DatabaseType: "TEXT"}}
	tb := New(cols, [][]db.Value{{{Text: "x"}}}, []string{"7"})
	_ = tb.SetCell(0, 0, db.Value{Text: "y"})
	stmts, warnings, err := Plan(tb, Target{Dialect: sqlx.SQLite, Table: db.TableRef{Name: "t"}, Key: Key{Kind: RowID}})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{`UPDATE "t" SET "a" = 'y' WHERE _rowid_ = '7'`}, stmts); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"t has no primary key or unique NOT NULL index: rows are matched by their rowid"}, warnings); diff != "" {
		t.Fatalf("warnings (-want +got):\n%s", diff)
	}
}
