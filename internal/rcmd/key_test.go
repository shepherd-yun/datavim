package rcmd

import (
	"slices"
	"testing"
)

func TestEscape(t *testing.T) {
	for in, want := range map[string]string{
		"user:1":         "user:1",
		"\x1f\x8bab\xff": `\x1F\x8Bab\xFF`,
		"a\nb":           `a\nb`,
		"a\tb":           `a\tb`,
		"中文":             "中文",
		`a\b`:            `a\\b`,
		"":               "",
		"\x7f":           `\x7F`,
	} {
		if got := Escape(in); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"user:1":         "user:1",
		"a-b_c.d/e{1}@#": "a-b_c.d/e{1}@#",
		"a b":            `"a b"`,
		"\x1f\x8bab\xff": `"\x1f\x8bab\xff"`,
		`say "hi"`:       `"say \"hi\""`,
		"":               `""`,
		"a\nb":           `"a\nb"`,
		`a\b`:            `"a\\b"`,
		"中文":             `"中文"`,
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPattern(t *testing.T) {
	for in, want := range map[string]string{
		"user":        "*user*",
		"user:*:sess": "user:*:sess",
		"a?c":         "a?c",
		"[ab]c":       "[ab]c",
		" x ":         "*x*",
		"":            "",
		"   ":         "",
	} {
		if got := Pattern(in); got != want {
			t.Errorf("Pattern(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSegments(t *testing.T) {
	for _, tc := range []struct {
		key, sep string
		want     []string
	}{
		{"user:1:name", ":", []string{"user", "1", "name"}},
		{"a::b", ":", []string{"a", "", "b"}},
		{"plain", ":", []string{"plain"}},
		{"a:b", "", []string{"a:b"}},
		{"a::b", "::", []string{"a", "b"}},
	} {
		if got := Segments(tc.key, tc.sep); !slices.Equal(got, tc.want) {
			t.Errorf("Segments(%q, %q) = %q, want %q", tc.key, tc.sep, got, tc.want)
		}
	}
}
