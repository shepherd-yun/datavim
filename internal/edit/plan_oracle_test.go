package edit

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

func TestPlanOracleRowID(t *testing.T) {
	ref := db.TableRef{Namespace: db.Namespace{Schema: "HR"}, Name: "MixedCase"}
	cols := []db.ColumnInfo{{Name: "ID", DatabaseType: "NUMBER"}, {Name: "Name", DatabaseType: "NCHAR"}}
	rows := [][]db.Value{{{Text: "1"}, {Text: "a"}}, {{Text: "2"}, {Text: "b"}}}
	tb := New(cols, rows, []string{"AAAR3sAAEAAAACXAAA", "AAAR3sAAEAAAACXAAB"})
	_ = tb.SetCell(0, 1, db.Value{Text: "it's"})
	tb.Delete(1)
	tb.InsertAfter(1)
	stmts, warnings, err := Plan(tb, Target{Dialect: sqlx.Oracle, Table: ref, Key: Key{Kind: RowID}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`DELETE FROM "HR"."MixedCase" WHERE ROWID = CHARTOROWID('AAAR3sAAEAAAACXAAB')`,
		`UPDATE "HR"."MixedCase" SET "Name" = 'it''s' WHERE ROWID = CHARTOROWID('AAAR3sAAEAAAACXAAA')`,
		`INSERT INTO "HR"."MixedCase" ("ID") VALUES (DEFAULT)`,
	}
	if diff := cmp.Diff(want, stmts); diff != "" {
		t.Fatalf("stmts (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"MixedCase has no primary key or unique NOT NULL index: rows are matched by their ROWID"}, warnings); diff != "" {
		t.Fatalf("warnings (-want +got):\n%s", diff)
	}
}
