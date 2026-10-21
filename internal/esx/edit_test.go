package esx

import "testing"

func TestParseCell(t *testing.T) {
	cases := []struct {
		typ, in, want string
		err           bool
	}{
		{"long", "12", "12", false},
		{"long", " 12.5 ", "12.5", false},
		{"long", "x", "", true},
		{"double", "1e3", "1e3", false},
		{"integer", "", "", true},
		{"boolean", "true", "true", false},
		{"boolean", "yes", "", true},
		{"keyword", "12", `"12"`, false},
		{"text", `say "hi"`, `"say \"hi\""`, false},
		{"date", "2026-10-20", `"2026-10-20"`, false},
		{"keyword", "null", "null", false},
		{"long", "null", "null", false},
		{"object", `{"a":1}`, `{"a":1}`, false},
		{"object", `{a`, "", true},
		{"nested", `[{"a":1}]`, `[{"a":1}]`, false},
		{"keyword|long", "12", "12", false},
		{"keyword|long", "abc", "", true},
		{"keyword|long", `"abc"`, `"abc"`, false},
		{"", "abc", `"abc"`, false},
	}
	for _, tc := range cases {
		got, err := ParseCell(tc.typ, tc.in)
		if (err != nil) != tc.err || !tc.err && string(got) != tc.want {
			t.Errorf("ParseCell(%q, %q) = %s, %v", tc.typ, tc.in, got, err)
		}
	}
}
