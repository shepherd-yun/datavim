package edit

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

var usersRef = db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "users"}

var idKey = Key{Kind: PrimaryKey, Columns: []string{"id"}}

func TestChooseKey(t *testing.T) {
	ab := []db.Column{{Name: "a", PrimaryKey: true}, {Name: "b", PrimaryKey: true}}
	plain := []db.Column{{Name: "email", Nullable: true}, {Name: "code"}}
	for _, c := range []struct {
		name string
		d    sqlx.Dialect
		cols []db.Column
		idx  []db.Index
		view bool
		want Key
	}{
		{"primary key column", sqlx.MySQL, []db.Column{{Name: "id", PrimaryKey: true}, {Name: "n"}}, nil, false, Key{PrimaryKey, []string{"id"}}},
		{"primary index order", sqlx.Postgres, ab, []db.Index{{Name: "pk", Columns: []string{"b", "a"}, Unique: true, Primary: true}}, false, Key{PrimaryKey, []string{"b", "a"}}},
		{"unique NOT NULL index", sqlx.MySQL, plain, []db.Index{
			{Name: "email_key", Columns: []string{"email"}, Unique: true},
			{Name: "code_key", Columns: []string{"code"}, Unique: true},
		}, false, Key{UniqueKey, []string{"code"}}},
		{"expression index is skipped", sqlx.MySQL, plain, []db.Index{{Name: "x", Columns: []string{"lower(code)"}, Unique: true}}, false, Key{Kind: AllColumns}},
		{"postgres table", sqlx.Postgres, plain, nil, false, Key{Kind: RowID}},
		{"postgres view", sqlx.Postgres, plain, nil, true, Key{}},
		{"mysql without key", sqlx.MySQL, plain, nil, true, Key{Kind: AllColumns}},
	} {
		if got := ChooseKey(c.d, c.cols, c.idx, c.view); !cmp.Equal(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestPlanMySQL(t *testing.T) {
	tb := New(cols(), page(), nil)
	_ = tb.SetCell(0, 1, db.Value{Text: `it's \ 李`})
	_ = tb.SetCell(0, 0, db.Value{Text: "10"}) // the key itself changes: the row is found by its old value
	tb.Delete(1)
	at := tb.InsertAfter(1)
	_ = tb.SetCell(at, 1, db.Value{Text: "new"})
	tb.InsertAfter(at) // every column left to its default
	stmts, warnings, err := Plan(tb, Target{Dialect: sqlx.MySQL, Table: usersRef, Key: idKey})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"DELETE FROM `app`.`users` WHERE `id` = 2",
		"UPDATE `app`.`users` SET `id` = 10, `name` = 'it''s \\\\ 李' WHERE `id` = 1",
		"INSERT INTO `app`.`users` (`name`) VALUES ('new')",
		"INSERT INTO `app`.`users` () VALUES ()",
	}
	if diff := cmp.Diff(want, stmts); diff != "" {
		t.Fatalf("statements (-want +got):\n%s", diff)
	}
	if len(warnings) != 0 {
		t.Fatalf("a primary key needs no warning: %v", warnings)
	}
}

func TestPlanPostgres(t *testing.T) {
	tb := New(cols(), page(), nil)
	_ = tb.SetCell(1, 1, db.Value{Text: `a\b`})
	tb.Delete(0)
	tb.InsertAfter(-1)
	stmts, _, err := Plan(tb, Target{Dialect: sqlx.Postgres, Table: usersRef, Key: idKey})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`DELETE FROM "public"."users" WHERE "id" = 1`,
		`UPDATE "public"."users" SET "name" = 'a\b' WHERE "id" = 2`,
		`INSERT INTO "public"."users" DEFAULT VALUES`,
	}
	if diff := cmp.Diff(want, stmts); diff != "" {
		t.Fatalf("statements (-want +got):\n%s", diff)
	}
}

func TestPlanWithoutKey(t *testing.T) {
	wide := []db.ColumnInfo{
		{Name: "id", DatabaseType: "BIGINT"}, {Name: "price", DatabaseType: "FLOAT"},
		{Name: "doc", DatabaseType: "JSON"}, {Name: "name", DatabaseType: "VARCHAR"},
	}
	rows := [][]db.Value{{{Text: "1"}, {Text: "1.5"}, {Text: "{}"}, {Null: true}}}
	tb := New(wide, rows, nil)
	_ = tb.SetCell(0, 3, db.Value{Text: "x"})
	stmts, warnings, err := Plan(tb, Target{Dialect: sqlx.MySQL, Table: usersRef, Key: Key{Kind: AllColumns}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "UPDATE `app`.`users` SET `name` = 'x' WHERE `id` = 1 AND `name` IS NULL LIMIT 1"; stmts[0] != want {
		t.Fatalf("got  %s\nwant %s", stmts[0], want)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "matched by all their values") {
		t.Fatalf("warnings = %v", warnings)
	}

	tb = New(cols(), page(), []string{"(0,1)", "(0,2)"})
	tb.Delete(0)
	stmts, warnings, err = Plan(tb, Target{Dialect: sqlx.Postgres, Table: usersRef, Key: Key{Kind: RowID}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `DELETE FROM "public"."users" WHERE ctid = '(0,1)'`; stmts[0] != want {
		t.Fatalf("got  %s\nwant %s", stmts[0], want)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "ctid") {
		t.Fatalf("warnings = %v", warnings)
	}

	tb = New(cols(), page(), nil)
	tb.Delete(0)
	if _, _, err := Plan(tb, Target{Dialect: sqlx.Postgres, Table: usersRef}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}
	tb.Reset()
	tb.InsertAfter(-1)
	if stmts, warnings, err := Plan(tb, Target{Dialect: sqlx.Postgres, Table: usersRef}); err != nil || len(stmts) != 1 || len(warnings) != 0 {
		t.Fatalf("new rows need no key: %v %v %v", stmts, warnings, err)
	}
}

func TestPlanNeedsKeyColumnsInTheResult(t *testing.T) {
	tb := New(cols(), page(), nil)
	tb.Delete(0)
	_, _, err := Plan(tb, Target{Dialect: sqlx.MySQL, Table: usersRef, Key: Key{Kind: PrimaryKey, Columns: []string{"uuid"}}})
	if err == nil || !strings.Contains(err.Error(), "uuid") {
		t.Fatalf("err = %v", err)
	}
}
