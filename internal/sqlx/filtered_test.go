package sqlx

import (
	"errors"
	"testing"
)

func TestFiltered(t *testing.T) {
	cols := []string{"id", "name"}
	for _, tc := range []struct {
		name    string
		stmt    string
		merge   bool
		want    string
		ordered bool
	}{
		{
			name: "merge without WHERE", stmt: "select * from users", merge: true,
			want: "select * from users\nWHERE (a = 1\n)", ordered: true,
		},
		{
			name: "merge before ORDER BY", stmt: "select * from users order by name limit 5", merge: true,
			want: "select * from users WHERE (a = 1\n) order by name limit 5", ordered: true,
		},
		{
			name: "merge into WHERE", stmt: "select * from users where id > 3 or name = 'x' order by name", merge: true,
			want: "select * from users where (id > 3 or name = 'x'\n) AND (a = 1\n) order by name", ordered: true,
		},
		{
			name: "where ends in a comment", stmt: "select * from users where id > 3 -- note\norder by id", merge: true,
			want: "select * from users where (id > 3 -- note\n) AND (a = 1\n) order by id", ordered: true,
		},
		{
			name: "trailing semicolon", stmt: "select * from users;", merge: true,
			want: "select * from users\nWHERE (a = 1\n)", ordered: true,
		},
		{
			name: "wrap", stmt: "select count(*) n from users",
			want: "SELECT * FROM (\nselect count(*) n from users\n) q WHERE (a = 1\n)", ordered: true,
		},
		{
			name: "wrap moves ORDER BY out", stmt: "select u.id, o.name from users u join o on o.uid = u.id order by name desc, 1",
			want: "SELECT * FROM (\nselect u.id, o.name from users u join o on o.uid = u.id\n) q WHERE (a = 1\n)\norder by name desc, 1", ordered: true,
		},
		{
			name: "wrap keeps ORDER BY with LIMIT inside", stmt: "select * from a join b order by a.x limit 3",
			want: "SELECT * FROM (\nselect * from a join b order by a.x limit 3\n) q WHERE (a = 1\n)", ordered: true,
		},
		{
			name: "wrap cannot move a qualified ORDER BY", stmt: "select * from a join b order by a.x",
			want: "SELECT * FROM (\nselect * from a join b order by a.x\n) q WHERE (a = 1\n)", ordered: false,
		},
		{
			name: "wrap a WITH query", stmt: "with x as (select 1 id) select * from x",
			want: "SELECT * FROM (\nwith x as (select 1 id) select * from x\n) q WHERE (a = 1\n)", ordered: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ordered, err := Filtered(MySQL, tc.stmt, "a = 1", tc.merge, cols)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want || ordered != tc.ordered {
				t.Fatalf("got %q ordered=%v\nwant %q ordered=%v", got, ordered, tc.want, tc.ordered)
			}
		})
	}
}

func TestFilteredEmptyAndNotSelect(t *testing.T) {
	if got, _, _ := Filtered(MySQL, "select * from users;", "", true, nil); got != "select * from users" {
		t.Fatalf("no condition: %q", got)
	}
	for _, s := range []string{"show tables", "explain select 1", "update t set a = 1"} {
		if _, _, err := Filtered(MySQL, s, "a = 1", false, nil); !errors.Is(err, ErrNotSelect) {
			t.Errorf("%s: err = %v", s, err)
		}
	}
}

func TestHasOrderBy(t *testing.T) {
	for sql, want := range map[string]bool{
		"select * from t order by id":                         true,
		"select * from (select top 1 * from t order by id) q": false,
		"select 'order by' from t":                            false,
		"select * from t -- order by x":                       false,
	} {
		if got := HasOrderBy(sql, SQLServer); got != want {
			t.Errorf("HasOrderBy(%q) = %v", sql, got)
		}
	}
}
