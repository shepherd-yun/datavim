package sqlx

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func texts(stmts []Statement) []string {
	out := []string{}
	for _, s := range stmts {
		out = append(out, s.Text)
	}
	return out
}

func TestSplit(t *testing.T) {
	cases := []struct {
		name string
		d    Dialect
		src  string
		want []string
	}{
		{"basic", MySQL, "select 1; select 2", []string{"select 1", "select 2"}},
		{"trailing delimiter and blanks", Postgres, "select 1;\n\n  ;\n", []string{"select 1"}},
		{"semicolon in string", MySQL, "select ';' as a; select 2;", []string{"select ';' as a", "select 2"}},
		{"mysql backslash escape", MySQL, `select 'it\'s; ok'; select 2`, []string{`select 'it\'s; ok'`, "select 2"}},
		{"pg doubled quote", Postgres, "select 'it''s; ok'; select 2", []string{"select 'it''s; ok'", "select 2"}},
		{"pg backslash is literal", Postgres, `select 'a\'; select 2`, []string{`select 'a\'`, "select 2"}},
		{"pg E string", Postgres, `select E'a\';b'; select 2`, []string{`select E'a\';b'`, "select 2"}},
		{
			"pg dollar quotes", Postgres, "select $$a;b$$; select $f$x;$f$; select 3",
			[]string{"select $$a;b$$", "select $f$x;$f$", "select 3"},
		},
		{"pg positional param is not a dollar quote", Postgres, "select $1; select 2", []string{"select $1", "select 2"}},
		{"line comment", Postgres, "select 1 -- ; x\n; select 2", []string{"select 1 -- ; x", "select 2"}},
		{"mysql -- needs a space", MySQL, "select 1--1; select 2", []string{"select 1--1", "select 2"}},
		{"mysql hash comment", MySQL, "select 1 # ; x\n; select 2", []string{"select 1 # ; x", "select 2"}},
		{"block comment", MySQL, "/* a; b */ select 1;", []string{"/* a; b */ select 1"}},
		{"pg nested block comment", Postgres, "/* a /* b; */ c; */ select 1", []string{"/* a /* b; */ c; */ select 1"}},
		{"comment-only statements are dropped", Postgres, "-- note\n;\nselect 1;\n-- trailing", []string{"select 1"}},
		{"backtick identifier", MySQL, "select `a;b` from t; select 2", []string{"select `a;b` from t", "select 2"}},
		{"pg quoted identifier", Postgres, `select "a;b" from t; select 2`, []string{`select "a;b" from t`, "select 2"}},
		{"mysql double-quoted string", MySQL, `select "a;\"b"; select 2`, []string{`select "a;\"b"`, "select 2"}},
		{"unterminated string runs to the end", MySQL, "select 'abc; select 2", []string{"select 'abc; select 2"}},
		{
			"delimiter blocks", MySQL,
			"DELIMITER //\nCREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END//\ndelimiter ;\nselect 3;",
			[]string{"CREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END", "select 3"},
		},
		{
			"delimiter is only a directive at statement start", MySQL, "select delimiter from t; select 2",
			[]string{"select delimiter from t", "select 2"},
		},
		{"cjk", Postgres, "select '李雷;'; select 2", []string{"select '李雷;'", "select 2"}},
		{"empty", MySQL, "", []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if diff := cmp.Diff(c.want, texts(Split(c.src, c.d))); diff != "" {
				t.Fatalf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestSplitOffsets(t *testing.T) {
	src := "  select 1 ;\n select 2"
	got := Split(src, MySQL)
	want := []Statement{{Text: "select 1", Start: 2, End: 10}, {Text: "select 2", Start: 14, End: 22}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	for _, s := range got {
		if src[s.Start:s.End] != s.Text {
			t.Errorf("offsets of %q point at %q", s.Text, src[s.Start:s.End])
		}
	}
}

func TestAt(t *testing.T) {
	src := "select 1;\n\nselect 2;\nselect 3"
	stmts := Split(src, MySQL)
	cases := map[int]string{
		0:            "select 1",
		8:            "select 1", // on the ;
		10:           "select 1", // blank line after it
		11:           "select 2",
		len(src):     "select 3",
		len(src) + 5: "select 3",
	}
	for off, want := range cases {
		got, ok := At(stmts, off)
		if !ok || got.Text != want {
			t.Errorf("At(%d) = %q, %v; want %q", off, got.Text, ok, want)
		}
	}
	if _, ok := At(nil, 0); ok {
		t.Error("At on no statements")
	}
}

func TestDialectOf(t *testing.T) {
	if DialectOf("postgres") != Postgres || DialectOf("mysql") != MySQL {
		t.Fatal("DialectOf")
	}
}
