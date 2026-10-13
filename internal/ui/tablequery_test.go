package ui

import (
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

func TestTableQuerySQL(t *testing.T) {
	ref := db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "users"}
	for _, c := range []struct {
		d    sqlx.Dialect
		q    tableQuery
		want string
	}{
		{sqlx.MySQL, tableQuery{}, "SELECT * FROM `app`.`users`"},
		{sqlx.MySQL, tableQuery{key: []string{"id"}}, "SELECT * FROM `app`.`users` ORDER BY `id`"},
		{sqlx.MySQL, tableQuery{where: "n > 1", sort: "name", desc: true, key: []string{"id"}},
			"SELECT * FROM `app`.`users` WHERE (n > 1\n) ORDER BY `name` DESC, `id`"},
		{sqlx.MySQL, tableQuery{sort: "id", key: []string{"id"}}, "SELECT * FROM `app`.`users` ORDER BY `id`"},
		{sqlx.Postgres, tableQuery{rowID: true, sort: "name"}, `SELECT ctid::text, * FROM "public"."users" ORDER BY "name"`},
	} {
		if got := c.q.sql(c.d, ref); got != c.want {
			t.Errorf("%+v:\n got %q\nwant %q", c.q, got, c.want)
		}
	}
}

func TestTableQueryCheck(t *testing.T) {
	ref := db.TableRef{Name: "users"}
	for where, ok := range map[string]bool{
		"":                          true,
		"name = 'a;b' AND id > 1":   true,
		"id = 1 -- note":            true,
		"1=1; DELETE FROM users":    false,
		"1) ; DROP TABLE users; --": false,
	} {
		if err := (tableQuery{where: where}).check(sqlx.MySQL, ref); (err == nil) != ok {
			t.Errorf("%q: err = %v", where, err)
		}
	}
}

func TestValueFilter(t *testing.T) {
	name := db.ColumnInfo{Name: "name", DatabaseType: "VARCHAR"}
	if got := valueFilter(sqlx.MySQL, name, db.Value{Text: "it's"}); got != "`name` = 'it''s'" {
		t.Errorf("value: %s", got)
	}
	if got := valueFilter(sqlx.Postgres, name, db.Value{Null: true}); got != `"name" IS NULL` {
		t.Errorf("null: %s", got)
	}
	if got := andFilter("", "a = 1"); got != "a = 1" {
		t.Errorf("first: %s", got)
	}
	if got := andFilter("a = 1 OR b = 2", "c = 3"); got != "(a = 1 OR b = 2) AND c = 3" {
		t.Errorf("and: %s", got)
	}
}
