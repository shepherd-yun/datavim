package sqlx

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestResultSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    Dialect
		stmt string
		cols []string
		want Source
		why  string
	}{
		{
			name: "star", stmt: "SELECT * FROM users", cols: []string{"id", "name"},
			want: Source{Table: "users", Columns: []string{"id", "name"}},
		},
		{
			name: "qualified table and alias star", stmt: "select u.* from app.users u where u.id > 3 order by name limit 10",
			cols: []string{"id", "name"}, want: Source{Qualifier: "app", Table: "users", Columns: []string{"id", "name"}},
		},
		{
			name: "column list with aliases", stmt: "SELECT id, u.name AS n, note x FROM users AS u",
			cols: []string{"id", "n", "x"}, want: Source{Table: "users", Columns: []string{"id", "name", "note"}},
		},
		{
			name: "quoted names", d: Postgres, stmt: `SELECT "Id" FROM "public"."Users"`,
			cols: []string{"Id"}, want: Source{Qualifier: "public", Table: "Users", Columns: []string{"Id"}},
		},
		{
			name: "star with extra column", stmt: "SELECT *, name AS n2 FROM users",
			cols: []string{"id", "name", "n2"}, why: "column name appears twice",
		},
		{
			name: "subquery in WHERE is fine", stmt: "SELECT * FROM users WHERE id IN (SELECT uid FROM orders)",
			cols: []string{"id"}, want: Source{Table: "users", Columns: []string{"id"}},
		},
		{
			name: "for update", stmt: "SELECT * FROM users FOR UPDATE", cols: []string{"id"},
			want: Source{Table: "users", Columns: []string{"id"}},
		},
		{
			name: "duplicate column", stmt: "SELECT id, name, name FROM users", cols: []string{"id", "name", "name"},
			why: "column name appears twice",
		},
		{name: "join", stmt: "SELECT * FROM users u JOIN orders o ON o.uid = u.id", cols: []string{"id"}, why: "JOIN"},
		{name: "comma join", stmt: "SELECT * FROM users, orders", cols: []string{"id"}, why: "JOIN"},
		{name: "union", stmt: "SELECT id FROM a UNION SELECT id FROM b", cols: []string{"id"}, why: "UNION"},
		{name: "group by", stmt: "SELECT name FROM users GROUP BY name", cols: []string{"name"}, why: "GROUP BY"},
		{name: "having", stmt: "SELECT name FROM users HAVING name > 'a'", cols: []string{"name"}, why: "HAVING"},
		{name: "distinct", stmt: "SELECT DISTINCT name FROM users", cols: []string{"name"}, why: "DISTINCT"},
		{name: "subquery in FROM", stmt: "SELECT * FROM (SELECT * FROM users) q", cols: []string{"id"}, why: "a subquery in FROM"},
		{
			name: "expression", stmt: "SELECT id, upper(name) FROM users", cols: []string{"id", "upper(name)"},
			why: "column 2 is an expression",
		},
		{name: "constant", stmt: "SELECT id, 1 FROM users", cols: []string{"id", "1"}, why: "column 2 is an expression"},
		{
			name: "window", stmt: "SELECT id, row_number() OVER () FROM users", cols: []string{"id", "row_number"},
			why: "column 2 is an expression",
		},
		{name: "with", stmt: "WITH x AS (SELECT 1) SELECT * FROM x", cols: []string{"1"}, why: "WITH"},
		{name: "no from", stmt: "SELECT 1", cols: []string{"1"}, why: "no FROM"},
		{name: "not a select", stmt: "SHOW TABLES", cols: []string{"Tables_in_app"}, why: "not a SELECT"},
		{
			name: "other table's column", stmt: "SELECT o.id FROM users u", cols: []string{"id"},
			why: "column 1 names another table",
		},
		{
			name: "column count differs", stmt: "SELECT id FROM users", cols: []string{"id", "name"},
			why: "the result does not match the select list",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, why := ResultSource(tc.d, tc.stmt, tc.cols)
			if why != tc.why {
				t.Fatalf("why = %q, want %q", why, tc.why)
			}
			if tc.why == "" {
				if diff := cmp.Diff(tc.want, got); diff != "" {
					t.Fatalf("source (-want +got):\n%s", diff)
				}
			}
		})
	}
}
