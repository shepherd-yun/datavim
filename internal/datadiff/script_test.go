package datadiff

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

var (
	bak     = db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users_bak"}
	noteful = Spec{Columns: []string{"id", "name", "note"}, Key: []string{"id"}}
	bakCols = []db.ColumnInfo{{Name: "id", DatabaseType: "BIGINT"}, {Name: "name", DatabaseType: "VARCHAR"}, {Name: "note", DatabaseType: "TEXT"}}
	diffs   = []Row{
		{Status: Changed, Source: r(v("2"), v("bob"), v("")), Target: r(v("2"), v("robert"), null)},
		{Status: Removed, Target: r(v("3"), v("it's"), null)},
		{Status: Added, Source: r(v("4"), v("dave"), null)},
	}
)

func all(int) bool { return true }

func TestScript(t *testing.T) {
	got, err := noteful.Script(sqlx.MySQL, bak, bakCols, diffs, all)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"DELETE FROM `app`.`users_bak` WHERE `id` = 3",
		"UPDATE `app`.`users_bak` SET `name` = 'bob', `note` = '' WHERE `id` = 2",
		"INSERT INTO `app`.`users_bak` (`id`, `name`, `note`) VALUES (4, 'dave', NULL)",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestScriptOnlyPicked(t *testing.T) {
	got, err := noteful.Script(sqlx.Postgres, db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "users_bak"},
		bakCols, diffs, func(i int) bool { return i == 2 })
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{`INSERT INTO "public"."users_bak" ("id", "name", "note") VALUES (4, 'dave', NULL)`}, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if got, err := noteful.Script(sqlx.MySQL, bak, bakCols, diffs, func(int) bool { return false }); err != nil || len(got) != 0 {
		t.Fatalf("nothing picked: %v, %v", got, err)
	}
}

func TestScriptNeedsTheComparedColumns(t *testing.T) {
	if _, err := noteful.Script(sqlx.MySQL, bak, bakCols[:2], diffs, all); err == nil {
		t.Fatal("want an error for result columns that are not the compared ones")
	}
}
