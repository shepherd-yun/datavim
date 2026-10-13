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
		{
			sqlx.MySQL,
			tableQuery{where: "n > 1", sort: "name", desc: true, key: []string{"id"}},
			"SELECT * FROM `app`.`users` WHERE (n > 1\n) ORDER BY `name` DESC, `id`",
		},
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
	if got := compareFilter(sqlx.MySQL, name, "=", db.Value{Text: "it's"}); got != "`name` = 'it''s'" {
		t.Errorf("value: %s", got)
	}
	if got := compareFilter(sqlx.Postgres, name, "=", db.Value{Null: true}); got != `"name" IS NULL` {
		t.Errorf("null: %s", got)
	}
	if got := andFilter("", "a = 1"); got != "a = 1" {
		t.Errorf("first: %s", got)
	}
	if got := andFilter("a = 1 OR b = 2", "c = 3"); got != "(a = 1 OR b = 2) AND c = 3" {
		t.Errorf("and: %s", got)
	}
}

func TestFilterConditions(t *testing.T) {
	name := db.ColumnInfo{Name: "name", DatabaseType: "VARCHAR"}
	age := db.ColumnInfo{Name: "age", DatabaseType: "INT"}
	for i, c := range []struct{ got, want string }{
		{compareFilter(sqlx.MySQL, name, "<>", db.Value{Text: "bob"}), "`name` <> 'bob'"},
		{compareFilter(sqlx.Postgres, name, "<>", db.Value{Null: true}), `"name" IS NOT NULL`},
		{compareFilter(sqlx.Postgres, age, ">", db.Value{Text: "18"}), `"age" > 18`},
		{compareFilter(sqlx.MySQL, age, "<", db.Value{Text: "x"}), "`age` < 'x'"},
		{nullFilter(sqlx.MySQL, "name", false), "`name` IS NULL"},
		{nullFilter(sqlx.Postgres, "name", true), `"name" IS NOT NULL`},
		{containsFilter(sqlx.MySQL, "name", "bo"), "`name` LIKE '%bo%'"},
		{containsFilter(sqlx.Postgres, "age", "1"), `"age"::text ILIKE '%1%'`},
		// % _ \ are LIKE wildcards / the escape: they match themselves; MySQL also doubles \ in the literal
		{containsFilter(sqlx.MySQL, "name", `50%_a\b'c`), "`name` LIKE '%50\\\\%\\\\_a\\\\\\\\b''c%'"},
		{containsFilter(sqlx.Postgres, "name", `50%_a\b'c`), `"name"::text ILIKE '%50\%\_a\\b''c%'`},
	} {
		if c.got != c.want {
			t.Errorf("%d: got %s, want %s", i, c.got, c.want)
		}
	}
}

func TestContainsFilterIsOneCondition(t *testing.T) {
	ref := db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "users"}
	for _, d := range []sqlx.Dialect{sqlx.MySQL, sqlx.Postgres} {
		q := tableQuery{where: containsFilter(d, "name", `x'; DROP TABLE users; --\`)}
		if err := q.check(d, ref); err != nil {
			t.Errorf("%v: %v (%s)", d, err, q.where)
		}
	}
}

func TestLikeFilterKeepsWildcards(t *testing.T) {
	for i, c := range []struct{ got, want string }{
		{likeFilter(sqlx.MySQL, "name", "a%b_c"), "`name` LIKE 'a%b_c'"},
		{likeFilter(sqlx.Postgres, "name", "a%b_c"), `"name"::text ILIKE 'a%b_c'`},
		{likeFilter(sqlx.MySQL, "name", `it's`), "`name` LIKE 'it''s'"},
	} {
		if c.got != c.want {
			t.Errorf("%d: got %s, want %s", i, c.got, c.want)
		}
	}
}
