package edit

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

func TestPlanSQLServerAllColumns(t *testing.T) {
	ref := db.TableRef{Namespace: db.Namespace{Database: "crm", Schema: "dbo"}, Name: "t"}
	cols := []db.ColumnInfo{
		{Name: "id", DatabaseType: "INT"},
		{Name: "name", DatabaseType: "NVARCHAR"},
		{Name: "doc", DatabaseType: "XML"},
		{Name: "body", DatabaseType: "NTEXT"},
	}
	rows := [][]db.Value{
		{{Text: "1"}, {Text: "a"}, {Text: "<x/>"}, {Text: "long"}},
		{{Text: "2"}, {Null: true}, {Null: true}, {Null: true}},
	}
	tb := New(cols, rows, nil)
	_ = tb.SetCell(0, 1, db.Value{Text: "张三"})
	tb.Delete(1)
	stmts, warnings, err := Plan(tb, Target{Dialect: sqlx.SQLServer, Table: ref, Key: Key{Kind: AllColumns}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"DELETE TOP (1) FROM [crm].[dbo].[t] WHERE [id] = 2 AND [name] IS NULL",
		"UPDATE TOP (1) [crm].[dbo].[t] SET [name] = N'张三' WHERE [id] = 1 AND [name] = N'a'",
	}
	if diff := cmp.Diff(want, stmts); diff != "" {
		t.Fatalf("stmts (-want +got):\n%s", diff)
	}
	wantWarn := []string{"t has no primary key or unique NOT NULL index: rows are matched by all their values, one row at most (TOP (1))"}
	if diff := cmp.Diff(wantWarn, warnings); diff != "" {
		t.Fatalf("warnings (-want +got):\n%s", diff)
	}

	// MySQL compares TEXT; only SQL Server leaves its legacy text types out.
	my := New([]db.ColumnInfo{{Name: "body", DatabaseType: "TEXT"}, {Name: "n", DatabaseType: "INT"}}, [][]db.Value{{{Text: "x"}, {Text: "1"}}}, nil)
	my.Delete(0)
	stmts, _, _ = Plan(my, Target{Dialect: sqlx.MySQL, Table: usersRef, Key: Key{Kind: AllColumns}})
	if want := "DELETE FROM `app`.`users` WHERE `body` = 'x' AND `n` = 1 LIMIT 1"; stmts[0] != want {
		t.Fatalf("mysql: %s", stmts[0])
	}
}
