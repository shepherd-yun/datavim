package vimedit

import "testing"

func TestTextObjects(t *testing.T) {
	cases := []struct {
		start string
		obj   rune
		inner bool
		want  string // the object's text; "" with ok false when there is none
	}{
		{"select us|ers from t", 'w', true, "users"},
		{"select us|ers from t", 'w', false, "users "},
		{"select x from us|ers", 'w', false, " users"}, // no blank after: take the one before
		{"selec|t x", 'w', true, "select"},
		{"select| x", 'w', true, " "},       // on a blank: the blanks
		{"select |  x", 'w', false, "   x"}, // on blanks: the blanks and the next word
		{"a.b|c.d", 'W', true, "a.bc.d"},
		{`where name = "a|b c" and`, '"', true, "ab c"},
		{`where name = "a|b c" and`, '"', false, `"ab c" `},
		{`wh|ere name = 'x' and`, '\'', true, "x"}, // the first string after the cursor
		{`select 'a', 'b|'`, '\'', true, "b"},
		{"count(|*)", '(', true, "*"},
		{"count(|*)", 'b', false, "(*)"},
		{"f(a, (b|), c)", ')', false, "(b)"},
		{"f(a, (b)|, c)", '(', true, "a, (b), c"},
		{"f(a, (b), c|)", '(', false, "(a, (b), c)"}, // on the close bracket
		{"|f(a)", '(', true, ""},                     // not inside a pair
		{"in (\n  1,\n  |2\n)", '(', true, "\n  1,\n  2\n"},
		{"m[1|]", '[', true, "1"},
		{"x|", '"', true, ""},
	}
	for _, c := range cases {
		text, p := parse(t, c.start)
		b := newBuffer(text)
		from, to, ok := b.textObject(p, c.obj, c.inner)
		got := ""
		if ok {
			got = b.slice(from, to)
		}
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%q %c inner=%v: %q (ok %v), want %q", c.start, c.obj, c.inner, got, ok, c.want)
		}
	}
}

func TestWordObjectOnEmptyLine(t *testing.T) {
	b := newBuffer("a\n\nb")
	if from, to, ok := b.textObject(Pos{1, 0}, 'w', true); !ok || from != to {
		t.Fatalf("iw on an empty line is empty: %v %v %v", from, to, ok)
	}
}
