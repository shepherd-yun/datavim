package sqlx

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestFollowing(t *testing.T) {
	where := []string{"AND", "OR", "GROUP BY", "ORDER BY", "LIMIT"}
	ops := []string{"=", "<>", "IN", "LIKE", "IS NULL", "IS NOT NULL", "BETWEEN"}
	afterTable := []string{"WHERE", "JOIN", "LEFT JOIN", "INNER JOIN", "GROUP BY", "ORDER BY", "LIMIT"}
	for _, tc := range []struct {
		src   string
		want  Next
		alias bool // afterTable plus AS
	}{
		{src: "SELECT ", want: Next{Words: []string{"*", "DISTINCT"}, Names: true}},
		{src: "select ", want: Next{Words: []string{"*", "DISTINCT"}, Names: true, Lower: true}},
		{src: "SELECT DISTINCT ", want: Next{Names: true}},
		{src: "SELECT * ", want: Next{Words: []string{"FROM"}}},
		{src: "SELECT id ", want: Next{Words: []string{"FROM", "AS"}}},
		{src: "SELECT count(*) ", want: Next{Words: []string{"FROM", "AS"}}},
		{src: "SELECT id AS x ", want: Next{Words: []string{"FROM"}}},
		{src: "SELECT id, ", want: Next{Names: true}},
		{src: "SELECT * FROM ", want: Next{Names: true}},
		{src: "SELECT * FROM users ", want: Next{Words: afterTable}, alias: true},
		{src: "SELECT * FROM app.users ", want: Next{Words: afterTable}, alias: true},
		{src: "SELECT * FROM users u ", want: Next{Words: afterTable}},
		{src: "SELECT * FROM a, b ", want: Next{Words: afterTable}, alias: true},
		{src: "SELECT * FROM users u LEFT ", want: Next{Words: []string{"JOIN", "OUTER JOIN"}}},
		{src: "SELECT * FROM users u JOIN orders ", want: Next{Words: []string{"ON", "USING", "AS"}}},
		{src: "SELECT * FROM users u JOIN orders o ", want: Next{Words: []string{"ON", "USING"}}},
		{src: "SELECT * FROM users u JOIN orders o ON ", want: Next{Names: true}},
		{src: "SELECT * FROM users u JOIN orders o ON o.uid ", want: Next{Words: ops}},
		{src: "SELECT * FROM users u JOIN orders o ON o.uid = ", want: Next{Names: true}},
		{
			src:  "SELECT * FROM users u JOIN orders o ON o.uid = u.id ",
			want: Next{Words: []string{"AND", "OR", "WHERE", "JOIN", "LEFT JOIN", "GROUP BY", "ORDER BY", "LIMIT"}},
		},
		{src: "SELECT * FROM users WHERE ", want: Next{Names: true}},
		{src: "SELECT * FROM users WHERE id ", want: Next{Words: ops}},
		{src: "SELECT * FROM users WHERE id = ", want: Next{}},
		{src: "SELECT * FROM users WHERE id = 1 ", want: Next{Words: where}},
		{src: "SELECT * FROM users WHERE name = 'x' ", want: Next{Words: where}},
		{src: "SELECT * FROM users WHERE id IN (1, 2) ", want: Next{Words: where}},
		{src: "SELECT * FROM users WHERE id = 1 AND ", want: Next{Names: true}},
		{src: "SELECT * FROM users WHERE id = 1 AND name ", want: Next{Words: ops}},
		{src: "SELECT * FROM users WHERE id = 1 AND name IS NOT NULL ", want: Next{Words: where}},
		{src: "SELECT * FROM users WHERE name IS ", want: Next{Words: []string{"NULL", "NOT NULL"}}},
		{src: "SELECT * FROM users WHERE name IS NOT ", want: Next{Words: []string{"NULL"}}},
		{src: "SELECT * FROM users WHERE id IN (", want: Next{}},
		{src: "SELECT * FROM users WHERE id IN (SELECT ", want: Next{Words: []string{"*", "DISTINCT"}, Names: true}},
		{src: "SELECT * FROM users WHERE id IN (SELECT uid ", want: Next{Words: []string{"FROM", "AS"}}},
		{src: "SELECT * FROM users ORDER ", want: Next{Words: []string{"BY"}}},
		{src: "SELECT * FROM users ORDER BY ", want: Next{Names: true}},
		{src: "SELECT * FROM users ORDER BY id ", want: Next{Words: []string{"ASC", "DESC", "LIMIT"}}},
		{src: "SELECT * FROM users ORDER BY id DESC ", want: Next{Words: []string{"LIMIT"}}},
		{src: "SELECT * FROM users GROUP BY id ", want: Next{Words: []string{"HAVING", "ORDER BY", "LIMIT"}}},
		{src: "SELECT * FROM users LIMIT 10 ", want: Next{Words: []string{"OFFSET"}}},
		{src: "SELECT * FROM users LIMIT ", want: Next{}},
		{src: "INSERT ", want: Next{Words: []string{"INTO"}}},
		{src: "INSERT INTO ", want: Next{Names: true}},
		{src: "INSERT INTO users ", want: Next{Words: []string{"VALUES", "SELECT"}}},
		{src: "INSERT INTO users (id, name) ", want: Next{Words: []string{"VALUES", "SELECT"}}},
		{src: "DELETE ", want: Next{Words: []string{"FROM"}}},
		{src: "DELETE FROM users ", want: Next{Words: []string{"WHERE"}}},
		{src: "UPDATE ", want: Next{Names: true}},
		{src: "UPDATE users ", want: Next{Words: []string{"SET"}}},
		{src: "UPDATE users SET ", want: Next{Names: true}},
		{src: "UPDATE users SET name ", want: Next{Words: []string{"="}}},
		{src: "UPDATE users SET name = 'x' ", want: Next{Words: []string{"WHERE"}}},
		{src: "SELECT 1; SELECT ", want: Next{Words: []string{"*", "DISTINCT"}, Names: true}},
		{src: " ", want: Next{}},
		{src: "SELECT 'a ", want: Next{}},
		{src: "SELECT * -- note ", want: Next{}},
		{src: "SELECT", want: Next{}}, // a word in progress
	} {
		want := tc.want
		if tc.alias {
			want.Words = append(append([]string(nil), want.Words...), "AS")
		}
		got := Following(tc.src, len(tc.src), MySQL)
		if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("%q (-want +got):\n%s", tc.src, diff)
		}
	}
}
