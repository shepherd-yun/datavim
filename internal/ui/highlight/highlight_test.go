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
