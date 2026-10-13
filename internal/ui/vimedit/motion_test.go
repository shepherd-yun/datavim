package vimedit

import "testing"

// motionCase moves from the "|" in start; want marks where it lands.
type motionCase struct {
	name, start, want string
	move              func(b *buffer, p Pos) Pos
}

func TestMotions(t *testing.T) {
	w := func(big bool) func(*buffer, Pos) Pos {
		return func(b *buffer, p Pos) Pos { return b.wordForward(p, big) }
	}
	bk := func(big bool) func(*buffer, Pos) Pos {
		return func(b *buffer, p Pos) Pos { return b.wordBack(p, big) }
	}
	e := func(big bool) func(*buffer, Pos) Pos {
		return func(b *buffer, p Pos) Pos { return b.wordEnd(p, big) }
	}
	para := func(dir int) func(*buffer, Pos) Pos {
		return func(b *buffer, p Pos) Pos { return b.paragraph(p, dir) }
	}
	cases := []motionCase{
		{"w to next word", "|select id, name", "select |id, name", w(false)},
		{"w stops at punctuation", "select |id, name", "select id|, name", w(false)},
		{"W skips punctuation", "select |id, name", "select id, |name", w(true)},
		{"w crosses lines", "a.|b\n  c", "a.b\n  |c", w(false)},
		{"w stops at an empty line", "|ab\n\ncd", "ab\n|\ncd", w(false)},
		{"w from an empty line", "ab\n|\n  cd", "ab\n\n  |cd", w(false)},
		{"w at the last word goes to the end", "ab |cd", "ab cd|", w(false)},
		{"w over CJK", "|李雷 x", "李雷 |x", w(false)},
		{"b to word start", "select na|me", "select |name", bk(false)},
		{"b to previous word", "select |name", "|select name", bk(false)},
		{"b stops at punctuation", "a.b|c", "a.|bc", bk(false)},
		{"B skips punctuation", "x a.b|c", "x |a.bc", bk(true)},
		{"b crosses lines", "ab\n  |cd", "|ab\n  cd", bk(false)},
		{"b stops at an empty line", "ab\n\n|cd", "ab\n|\ncd", bk(false)},
		{"b at the start stays", "|ab", "|ab", bk(false)},
		{"e to word end", "|select id", "selec|t id", e(false)},
		{"e from word end to next", "selec|t id", "select i|d", e(false)},
		{"e skips empty lines", "a|b\n\n cd", "ab\n\n c|d", e(false)},
		{"E over punctuation", "|a.b c", "a.|b c", e(true)},
		{"e at the end stays", "ab|c", "ab|c", e(false)},
		{"} to the next empty line", "|a\nb\n\nc", "a\nb\n|\nc", para(1)},
		{"} from an empty line skips it", "a\n|\nb\n\nc", "a\n\nb\n|\nc", para(1)},
		{"} without an empty line goes to the end", "|a\nb", "a\nb|", para(1)},
		{"{ to the previous empty line", "a\n\nb\n|c", "a\n|\nb\nc", para(-1)},
		{"{ without an empty line goes to the start", "a\n|b", "|a\nb", para(-1)},
	}
	for _, c := range cases {
		text, p := parse(t, c.start)
		b := newBuffer(text)
		got := c.move(&b, p)
		if _, want := parse(t, c.want); got != want {
			t.Errorf("%s: %q -> %v, want %v", c.name, c.start, got, want)
		}
	}
}

func TestFindChar(t *testing.T) {
	b := newBuffer("a,b,c,d")
	for _, c := range []struct {
		from          int
		forward, till bool
		count         int
		want          int
		ok            bool
	}{
		{0, true, false, 1, 1, true},  // f,
		{0, true, false, 2, 3, true},  // 2f,
		{0, true, true, 2, 2, true},   // 2t,
		{0, true, true, 1, 0, true},   // t, right before a comma does not move
		{6, false, false, 1, 5, true}, // F,
		{6, false, true, 1, 6, true},  // T, right after a comma does not move
		{0, true, false, 9, 0, false}, // not that many
	} {
		got, ok := b.findChar(Pos{0, c.from}, ',', c.forward, c.till, c.count)
		if ok != c.ok || ok && got.Col != c.want {
			t.Errorf("%+v: got %v %v", c, got, ok)
		}
	}
}

func TestFirstNonBlankAndBrackets(t *testing.T) {
	b := newBuffer("  \tselect (a, (b))\n   \nx[1]")
	if got := b.firstNonBlank(0); got != (Pos{0, 3}) {
		t.Errorf("^ = %v", got)
	}
	if got := b.firstNonBlank(1); got != (Pos{1, 2}) {
		t.Errorf("^ on a blank line = %v", got)
	}
	for _, c := range []struct{ from, want Pos }{
		{Pos{0, 0}, Pos{0, 17}},  // the first bracket after the cursor
		{Pos{0, 14}, Pos{0, 16}}, // inner pair
		{Pos{0, 17}, Pos{0, 10}}, // backwards from a close bracket
		{Pos{2, 0}, Pos{2, 3}},   // [ ]
	} {
		if got, ok := b.matchBracket(c.from); !ok || got != c.want {
			t.Errorf("%% from %v = %v %v, want %v", c.from, got, ok, c.want)
		}
	}
	if _, ok := b.matchBracket(Pos{1, 0}); ok {
		t.Error("% without a bracket fails")
	}
	if nb := newBuffer("(a"); func() bool { _, ok := nb.matchBracket(Pos{}); return ok }() {
		t.Error("% without a partner fails")
	}
}
