package sqlx

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// cursorAt removes the | marker and returns the text and its offset.
func cursorAt(s string) (string, int) {
	i := strings.Index(s, "|")
	return s[:i] + s[i+1:], i
}

func TestComplete(t *testing.T) {
	for _, tc := range []struct {
		src       string
		d         Dialect
		kind      CompleteKind
		prefix    string
		qualifier string
		tables    []TableUse // checked when not nil
	}{
		{"SELECT * FROM us|", MySQL, CompleteTable, "us", "", nil},
		{"SELECT * FROM |", MySQL, CompleteTable, "", "", nil},
		{"SELECT * FROM a, |", MySQL, CompleteTable, "", "", nil},
		{"SELECT * FROM users u JOIN or|", MySQL, CompleteTable, "or", "", nil},
		{"SELECT * FROM app.|", MySQL, CompleteTable, "", "app", nil},
		{"DELETE FROM |", Postgres, CompleteTable, "", "", nil},
		{"SELECT na| FROM users", MySQL, CompleteColumn, "na", "", []TableUse{{Name: "users"}}},
		{"SELECT u.| FROM users u", MySQL, CompleteColumn, "", "u", []TableUse{{Name: "users", Alias: "u"}}},
		{"SELECT * FROM users u WHERE u.na|", MySQL, CompleteColumn, "na", "u", nil},
		{"SELECT * FROM users WHERE |", MySQL, CompleteColumn, "", "", nil},
		{"SELECT * FROM users WHERE id = 1 AND na|", MySQL, CompleteColumn, "na", "", nil},
		{"UPDATE users SET na|", MySQL, CompleteColumn, "na", "", []TableUse{{Name: "users"}}},
		{"INSERT INTO users (na|", MySQL, CompleteColumn, "na", "", []TableUse{{Name: "users"}}},
		{
			"SELECT * FROM users AS u JOIN orders o ON o.user_id = u.|", MySQL, CompleteColumn, "", "u",
			[]TableUse{{Name: "users", Alias: "u"}, {Name: "orders", Alias: "o"}},
		},
		{
			`SELECT * FROM "MixedCase" m WHERE m.|`, Postgres, CompleteColumn, "", "m",
			[]TableUse{{Name: "MixedCase", Alias: "m"}},
		},
		{
			"SELECT * FROM `app`.`users` WHERE |", MySQL, CompleteColumn, "", "",
			[]TableUse{{Qualifier: "app", Name: "users"}},
		},
		{
			"SELECT * FROM public.users x, orders WHERE |", Postgres, CompleteColumn, "", "",
			[]TableUse{{Qualifier: "public", Name: "users", Alias: "x"}, {Name: "orders"}},
		},
		{"SELECT id |", MySQL, CompleteKeyword, "", "", nil},
		{"SELECT * |", MySQL, CompleteKeyword, "", "", nil},
		{"SELECT * FROM users |", MySQL, CompleteKeyword, "", "", nil},
		{"SEL|", MySQL, CompleteKeyword, "SEL", "", nil},
		{"|", MySQL, CompleteKeyword, "", "", nil},
		{"SELECT 1;\n|", MySQL, CompleteKeyword, "", "", nil},
		{"SELECT 1; SELECT * FROM o|", MySQL, CompleteTable, "o", "", []TableUse{{Name: "o"}}},
		{"SELECT 'us|", MySQL, CompleteNone, "", "", nil},
		{"SELECT 'a' |", MySQL, CompleteKeyword, "", "", nil},
		{"SELECT 1 -- us|", MySQL, CompleteNone, "", "", nil},
		{"SELECT 1 # us|", MySQL, CompleteNone, "", "", nil},
		{"SELECT /* us|", Postgres, CompleteNone, "", "", nil},
		{"SELECT $$ us|", Postgres, CompleteNone, "", "", nil},
		{`SELECT "Mix|`, Postgres, CompleteNone, "", "", nil},
	} {
		src, cur := cursorAt(tc.src)
		got := Complete(src, cur, tc.d)
		if got.Kind != tc.kind || got.Prefix != tc.prefix || got.Qualifier != tc.qualifier {
			t.Errorf("%q: kind=%d prefix=%q qualifier=%q, want %d %q %q", tc.src, got.Kind, got.Prefix, got.Qualifier, tc.kind, tc.prefix, tc.qualifier)
			continue
		}
		if tc.kind != CompleteNone && got.Start != cur-len(tc.prefix) {
			t.Errorf("%q: start = %d, want %d", tc.src, got.Start, cur-len(tc.prefix))
		}
		if tc.tables != nil {
			if diff := cmp.Diff(tc.tables, got.Tables); diff != "" {
				t.Errorf("%q: tables (-want +got):\n%s", tc.src, diff)
			}
		}
	}
}

func TestCompleteClampsCursor(t *testing.T) {
	if got := Complete("SELECT", 99, MySQL); got.Prefix != "SELECT" || got.Start != 0 {
		t.Fatalf("got %+v", got)
	}
	if got := Complete("SELECT", -1, MySQL); got.Prefix != "" || got.Start != 0 {
		t.Fatalf("got %+v", got)
	}
}
