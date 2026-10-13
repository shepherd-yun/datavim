package vimedit

import (
	"strings"
	"testing"
)

func TestNormalMotions(t *testing.T) {
	runCases(t, []editCase{
		{"|select id\nfrom t", "w", "select |id\nfrom t", Normal},
		{"|select id\nfrom t", "3w", "select id\nfrom |t", Normal},
		{"|select id\nfrom t", "jl", "select id\nf|rom t", Normal},
		{"select i|d\nx", "j", "select id\n|x", Normal},
		{"select i|d\nx\nfrom", "jj", "select id\nx\nfro|m", Normal}, // j remembers the column
		{"select |id", "$", "select i|d", Normal},
		{"|ab\nabcdef", "$j", "ab\nabcde|f", Normal}, // after $, j stays at the line end
		{"  sele|ct", "0", "|  select", Normal},
		{"  sele|ct", "^", "  |select", Normal},
		{"a\nb\n|c", "gg", "|a\nb\nc", Normal},
		{"|a\n  b\nc", "2G", "a\n  |b\nc", Normal},
		{"|a\nb\nc", "G", "a\nb\n|c", Normal},
		{"|a\nb\nc", "9j", "a\nb\n|c", Normal}, // as far as the text goes
		{"|count(*)", "%", "count(*|)", Normal},
		{"|a,b,c", "2f,", "a,b|,c", Normal},
		{"|a,b,c", "t,", "|a,b,c", Normal},
		{"a,b,|c", "F,", "a,b|,c", Normal},
		{"|a\n\nb", "}", "a\n|\nb", Normal},
		{"ab|c", "l", "ab|c", Normal}, // l stops on the last rune
		{"|abc", "h", "|abc", Normal},
		{"a|bc", "<right><left><left>", "|abc", Normal},
		{"|a\nb", "<down>", "a\n|b", Normal},
		{"|abc", "10l", "ab|c", Normal},
		{"|abc", "zl", "a|bc", Normal}, // an unknown key cancels the pending one
	})
}

func TestHalfPageMoves(t *testing.T) {
	e := newAt(t, "|"+strings.Repeat("x\n", 30)+"x")
	e.SetHeight(10)
	feed(e, "<c-d>")
	if e.Cursor().Line != 5 {
		t.Fatalf("ctrl+d: line %d", e.Cursor().Line)
	}
	feed(e, "<c-u><c-u>")
	if e.Cursor().Line != 0 {
		t.Fatalf("ctrl+u: line %d", e.Cursor().Line)
	}
}

func TestCountsAreBoundedAndPending(t *testing.T) {
	e := newAt(t, "|ab")
	feed(e, `"a2d`)
	if got := e.Pending(); got != `"a2d` {
		t.Fatalf("pending = %q", got)
	}
	feed(e, "<esc>")
	if e.Pending() != "" || e.Text() != "ab" {
		t.Fatalf("esc cancels: %q %q", e.Pending(), e.Text())
	}
	feed(e, "99999999999")
	if e.pend.count != maxCount {
		t.Fatalf("count = %d", e.pend.count)
	}
}

func TestUnhandledKeysGoToTheHost(t *testing.T) {
	e := newAt(t, "|ab")
	for _, k := range []Key{{Name: "space", Text: " "}, {Name: ":"}, {Name: "enter"}, {Name: "ctrl+c"}, {Name: "R"}, {Name: "M"}, {Name: "?"}, {Name: "tab"}} {
		if r := e.Feed(k); r.Handled {
			t.Errorf("%s is handled", k.Name)
		}
	}
	feed(e, "d")
	if r := e.Feed(Key{Name: "space", Text: " "}); !r.Handled || e.Pending() != "" {
		t.Error("a key that cancels a pending command is used")
	}
	for _, s := range []string{"3", "j", "<esc>"} {
		if r := feed(e, s); !r.Handled {
			t.Errorf("%s is not handled", s)
		}
	}
}

func TestTabJumps(t *testing.T) {
	e := newAt(t, "|ab")
	for _, c := range []struct {
		keys string
		want TabJump
	}{
		{"gt", TabJump{By: 1}},
		{"gT", TabJump{By: -1}},
		{"3gt", TabJump{To: 3}},
		{"2gT", TabJump{By: -2}},
	} {
		r := feed(e, c.keys)
		if r.Tab == nil || *r.Tab != c.want {
			t.Errorf("%s: %+v", c.keys, r.Tab)
		}
	}
	if r := feed(e, "dgt"); r.Tab != nil {
		t.Error("dgt is not a tab jump")
	}
}

func TestInsertMode(t *testing.T) {
	runCases(t, []editCase{
		{"a|b", "ix<esc>", "a|xb", Normal},
		{"a|b", "ax<esc>", "ab|x", Normal},
		{"  a|b", "Ix<esc>", "  |xab", Normal},
		{"a|b", "Ax<esc>", "ab|x", Normal},
		{"  a|b", "ox<esc>", "  ab\n  |x", Normal}, // o keeps the indent
		{"  a|b", "Ox<esc>", "  |x\n  ab", Normal},
		{"a|b", "i<cr><esc>", "a\n|b", Normal},
		{"  a|b", "a<cr>x<esc>", "  ab\n  |x", Normal},
		{"ab|c", "i<bs><bs><bs>", "|c", Insert},
		{"a\n|b", "i<bs>", "a|b", Insert}, // backspace joins lines
		{"a|b\nc", "i<del><del>", "a|c", Insert},
		{"select user|s", "a<c-w>", "select |", Insert},
		{"select |users", "i<c-w>", "|users", Insert},
		{"select user|s", "a<c-u>", "|", Insert},
		{"|a", "i<tab>", "    |a", Insert},
		{"|a", "i李 <esc>", "李| a", Normal},
		{"a|b", "i<left><left>x", "x|ab", Insert},
		{"ab\n|cd", "i<up><end>x", "abx|\ncd", Insert},
		{"|", "i<esc>", "|", Normal},
	})
}

func TestInsertModeLeavesHostKeys(t *testing.T) {
	e := newAt(t, "|a")
	feed(e, "i")
	for _, name := range []string{"ctrl+j", "ctrl+enter", "ctrl+c", "ctrl+n"} {
		if r := e.Feed(Key{Name: name}); r.Handled {
			t.Errorf("%s is handled in INSERT mode", name)
		}
	}
}

func TestHostEdits(t *testing.T) {
	e := New()
	e.SetText("select 1;\nselect 2;")
	if got := show(e); got != "select 1;\nselect 2|;" {
		t.Fatalf("SetText puts the cursor at the end: %q", got)
	}
	feed(e, "u")
	if e.Text() != "select 1;\nselect 2;" {
		t.Fatal("SetText is not undone")
	}
	e.SetOffset(len("select 1;\nsel"))
	if e.Cursor() != (Pos{1, 3}) || e.Offset() != 13 {
		t.Fatalf("SetOffset: %v %d", e.Cursor(), e.Offset())
	}
	e.ReplaceAll("x")
	feed(e, "u")
	if e.Text() != "select 1;\nselect 2;" {
		t.Fatalf("ReplaceAll is undone: %q", e.Text())
	}
	e.SetText("select us from t")
	feed(e, "0")
	e.SetOffset(len("select us"))
	feed(e, "i")
	e.ReplaceBeforeCursor(len("select "), "users")
	if got := show(e); got != "select users| from t" {
		t.Fatalf("ReplaceBeforeCursor: %q", got)
	}
	e.InsertText("\nwhere")
	if got := show(e); got != "select users\nwhere| from t" {
		t.Fatalf("InsertText: %q", got)
	}
	v := e.Version()
	feed(e, "<esc>")
	if e.Version() != v {
		t.Fatal("esc does not change the text")
	}
	feed(e, "u")
	if e.Text() != "select us from t" {
		t.Fatalf("one INSERT session is one undo step: %q", e.Text())
	}
}

func TestSetCursorClampsToTheMode(t *testing.T) {
	e := New()
	e.SetText("abc\nd")
	e.SetCursor(Pos{0, 9})
	if e.Cursor() != (Pos{0, 2}) {
		t.Fatalf("NORMAL: %v", e.Cursor())
	}
	feed(e, "i")
	e.SetCursor(Pos{9, 9})
	if e.Cursor() != (Pos{1, 1}) {
		t.Fatalf("INSERT may sit after the last rune: %v", e.Cursor())
	}
}
