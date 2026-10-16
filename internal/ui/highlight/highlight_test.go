package highlight

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

func classAt(spans []Span, off int) Class {
	for _, s := range spans {
		if off >= s.Start && off < s.End {
			return s.Class
		}
	}
	return -1
}

func TestSQLClassifiesAndCoversTheText(t *testing.T) {
	src := "SELECT id, 'a' -- note\nFROM t WHERE n = 42 AND x"
	for _, d := range []sqlx.Dialect{sqlx.MySQL, sqlx.Postgres} {
		spans := SQL(src, d)
		at := 0
		for _, s := range spans {
			if s.Start != at || s.End <= s.Start {
				t.Fatalf("dialect %d: spans must cover the text in order: %+v", d, spans)
			}
			at = s.End
		}
		if at != len(src) {
			t.Fatalf("dialect %d: spans end at %d, want %d", d, at, len(src))
		}
		for _, tc := range []struct {
			text string
			want Class
		}{
			{"SELECT", Keyword}, {"'a'", String}, {"-- note", Comment}, {"FROM", Keyword}, {"42", Number}, {"AND", Keyword},
		} {
			if got := classAt(spans, strings.Index(src, tc.text)); got != tc.want {
				t.Errorf("dialect %d: %q is class %d, want %d", d, tc.text, got, tc.want)
			}
		}
	}
}

func TestSQLEdgeCases(t *testing.T) {
	if SQL("", sqlx.MySQL) != nil {
		t.Fatal("empty text has no spans")
	}
	for _, src := range []string{"\x00\x01 SELECT", "李雷 'unterminated", "$$ x"} {
		spans := SQL(src, sqlx.Postgres)
		if len(spans) == 0 || spans[0].Start != 0 || spans[len(spans)-1].End != len(src) {
			t.Errorf("%q: spans %+v do not cover the text", src, spans)
		}
	}
}

func classesOf(src string, spans []Span) []Class {
	out := make([]Class, len(src))
	for _, s := range spans {
		for i := s.Start; i < s.End; i++ {
			out[i] = s.Class
		}
	}
	return out
}

func TestHighlighterMatchesSQL(t *testing.T) {
	src := "-- top\nSELECT 'a;b', 1 FROM t;\n/* x; */ UPDATE t SET n = 2 WHERE id = 3;\n\nselect \"q;\" from u"
	for _, d := range []sqlx.Dialect{sqlx.MySQL, sqlx.Postgres} {
		got := NewHighlighter(d).Spans(src)
		want := SQL(src, d)
		if got, want := classesOf(src, got), classesOf(src, want); !equalClasses(got, want) {
			t.Fatalf("dialect %d: classes differ\n got %v\nwant %v", d, got, want)
		}
		at := 0
		for _, s := range NewHighlighter(d).Spans(src) {
			if s.Start != at || s.End <= s.Start {
				t.Fatalf("spans must cover the text in order")
			}
			at = s.End
		}
		if at != len(src) {
			t.Fatalf("spans end at %d", at)
		}
	}
	if NewHighlighter(sqlx.MySQL).Spans("") != nil {
		t.Fatal("empty text has no spans")
	}
}

func equalClasses(a, b []Class) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestHighlighterReusesUnchangedStatements(t *testing.T) {
	h := NewHighlighter(sqlx.MySQL)
	src := bigConsole(200)
	h.Spans(src)
	before := h.misses
	h.Spans(src + "SELECT 1")
	if n := h.misses - before; n > 2 {
		t.Fatalf("an edit at the end re-highlighted %d segments", n)
	}
}

func TestSQLConstantsAndBacktickIdentifiers(t *testing.T) {
	src := "SELECT `order`.`from`, `we``ird`, 'a`b' FROM `select` WHERE x IS NULL AND y = TRUE -- c`x"
	spans := SQL(src, sqlx.MySQL)
	for _, tc := range []struct {
		text string
		want Class
	}{
		{"`order`", Plain},
		{"order`", Plain},
		{"`from`", Plain},
		{"from`", Plain},
		{"`we``ird`", Plain},
		{"``ird", Plain},
		{"ird`", Plain},
		{"'a`b'", String},
		{"`b'", String},
		{"`select`", Plain},
		{"select`", Plain},
		{"FROM", Keyword},
		{"NULL", Keyword},
		{"TRUE", Keyword},
		{"c`x", Comment},
	} {
		i := strings.Index(src, tc.text)
		for off := i; off < i+len(tc.text); off++ {
			if got := classAt(spans, off); got != tc.want {
				t.Errorf("%q byte %d (%q) is class %d, want %d", tc.text, off-i, src[off], got, tc.want)
				break
			}
		}
	}
	if got := classAt(SQL("SELECT null", sqlx.Postgres), len("SELECT ")); got != Keyword {
		t.Errorf("PostgreSQL null is class %d", got)
	}
}

func TestHighlightTSQL(t *testing.T) {
	src := "SELECT TOP 5 * FROM [dbo].[order] WHERE name = N'x' -- c"
	spans := SQL(src, sqlx.SQLServer)
	for _, tc := range []struct {
		text string
		want Class
	}{
		{"SELECT", Keyword}, {"TOP", Keyword}, {"[dbo]", Plain}, {"order]", Plain}, {"N'x'", String}, {"-- c", Comment},
	} {
		if got := classAt(spans, strings.Index(src, tc.text)); got != tc.want {
			t.Errorf("%q is class %d, want %d (%+v)", tc.text, got, tc.want, spans)
		}
	}
}
