package vimedit

import (
	"strings"
	"testing"
)

func TestOperators(t *testing.T) {
	runCases(t, []editCase{
		{"select |id, name", "dw", "select |, name", Normal},
		{"select |id, name", "d2w", "select |name", Normal},
		{"select |id, name", "2dw", "select |name", Normal},
		{"select |id\nfrom", "dW", "select| \nfrom", Normal}, // the last word of a line
		{"select |id, name", "de", "select |, name", Normal},
		{"select |id, name", "cwuid<esc>", "select ui|d, name", Normal},
		{"select i|d, name", "cwx<esc>", "select i|x, name", Normal}, // cw on the last rune of a word
		{"select |id, name", "c2wx<esc>", "select |x name", Normal},
		{"select |  id", "cwx<esc>", "select |xid", Normal},
		{"select id, |name", "db", "select id|name", Normal},
		{"select |id", "d$", "select| ", Normal},
		{"select |id", "D", "select| ", Normal},
		{"select |id", "Cx<esc>", "select |x", Normal},
		{"  sele|ct", "d0", "|ct", Normal},
		{"  sele|ct", "d^", "  |ct", Normal},
		{"a|bc", "x", "a|c", Normal},
		{"a|bc", "5x", "|a", Normal},
		{"|", "x", "|", Normal},
		{"a,|b,c", "dt,", "a,|,c", Normal},
		{"a,|b,c", "df,", "a,|c", Normal},
		{"a,b,|c", "dF,", "a,b|c", Normal},
		{"f|(a, b) x", "d%", "f| x", Normal},
		{"a\n|b\nc", "dd", "a\n|c", Normal},
		{"a\nb\n|c", "dd", "a\n|b", Normal},
		{"|a", "dd", "|", Normal},
		{"|a\nb\nc", "2dd", "|c", Normal},
		{"|a\nb\nc", "5dd", "|", Normal},
		{"|a\nb\nc", "dj", "|c", Normal},
		{"a\n|b\nc", "dk", "|c", Normal},
		{"a\nb\n|c", "dj", "a\nb\n|c", Normal}, // j cannot move: nothing happens
		{"a\n|b\nc", "dG", "|a", Normal},
		{"a\n|b\nc", "dgg", "|c", Normal},
		{"|a\nb\n\nc", "d}", "|\nc", Normal}, // a paragraph from its start goes linewise
		{"  |a\n  b", "ccx<esc>", "  |x\n  b", Normal},
		{"|a\nb", "cjx<esc>", "|x", Normal},
		{"count(a|, b)", "ci(x<esc>", "count(|x)", Normal},
		{"count(a|, b)", "da(", "coun|t", Normal},
		{"x = 'a|b' and", "ci'y<esc>", "x = '|y' and", Normal},
		{`x = "a|b" and`, `da"`, "x = |and", Normal},
		{"select us|ers from", "diw", "select | from", Normal},
		{"select us|ers from", "daw", "select |from", Normal},
		{"|select", "ci(x<esc>", "|elect", Normal}, // no object: ci( does nothing, then x deletes
		{"|a\n\nb", ">>", "    |a\n\nb", Normal},
		{"|a\n\nb", ">2j", "    |a\n\n    b", Normal}, // empty lines stay empty
		{"      |a", "<<", "  |a", Normal},
		{"\t|a", "<<", "|a", Normal},
		{"  |a", "<<", "|a", Normal},
	})
}

func TestYankAndPut(t *testing.T) {
	runCases(t, []editCase{
		{"|ab cd", "ywP", "ab| ab cd", Normal},
		{"|ab cd", "ywwP", "ab ab| cd", Normal},
		{"ab |cd", "ybP", "ab| ab cd", Normal},
		{"ab |cd", "yb", "|ab cd", Normal}, // the cursor goes to the range start
		{"|a\nb", "yyjp", "a\nb\n|a", Normal},
		{"a\n|b", "yykP", "|b\na\nb", Normal},
		{"|a\nb", "yj", "|a\nb", Normal},
		{"|a\nb", "yjGp", "a\nb\n|a\nb", Normal},
		{"|ab", "yl3p", "aaa|ab", Normal},
		{"|a\nb", "ddp", "b\n|a", Normal},
		{"|ab", "xp", "b|a", Normal},
		{"|", "p", "|", Normal}, // empty register
		{"|  x\ny", "yyjp", "  x\ny\n  |x", Normal},
		{"|a\n", "yy\"_p", "a\n|a\n", Normal}, // "_ is not a register: it is dropped and p uses the unnamed one
	})
}

func TestNamedRegisters(t *testing.T) {
	e := newAt(t, "|one two")
	feed(e, `"ayw`+"w"+`"byw`+"$"+`"ap`)
	if got := show(e); got != "one twoone| " {
		t.Fatalf("\"ap: %q", got)
	}
	feed(e, "0"+`"bP`)
	if got := show(e); got != "tw|oone twoone " {
		t.Fatalf("\"bP: %q", got)
	}
	feed(e, "x"+`"ap`) // x went to the unnamed register only
	if got := show(e); got != "twoone| ne twoone " {
		t.Fatalf("\"a after x: %q", got)
	}
	feed(e, "p")
	if got := show(e); got != "twoone |one twoone " {
		t.Fatalf("p uses the unnamed register: %q", got)
	}
}

func TestClipboardRegister(t *testing.T) {
	e := newAt(t, "|select 1\nx")
	r := feed(e, `"+yy`)
	if r.Clipboard == nil || *r.Clipboard != "select 1\n" {
		t.Fatalf("\"+yy copies the line: %+v", r)
	}
	if r := feed(e, `"+dw`); r.Clipboard == nil || *r.Clipboard != "select " {
		t.Fatalf("\"+dw: %+v", r.Clipboard)
	}
	if r := feed(e, "yw"); r.Clipboard == nil || *r.Clipboard != "1" {
		t.Fatalf("a plain yank goes to the clipboard too: %+v", r.Clipboard)
	}
	if r := feed(e, `"ayw`); r.Clipboard != nil {
		t.Fatal("named registers stay inside the editor")
	}
	if r := feed(e, "x"); r.Clipboard != nil {
		t.Fatal("a plain delete stays inside the editor")
	}
	feed(e, "u")
	r = feed(e, `2"+p`)
	if r.Paste == nil || r.Paste.Before || r.Paste.Count != 2 {
		t.Fatalf("\"+p asks for the clipboard: %+v", r.Paste)
	}
	if !e.PasteClipboard(*r.Paste, "ab") {
		t.Fatal("paste refused")
	}
	if got := show(e); got != "1aba|b\nx" {
		t.Fatalf("pasted: %q", got)
	}
	r = feed(e, `"+P`)
	feed(e, "x")
	if e.PasteClipboard(*r.Paste, "zz") {
		t.Fatal("a paste after the text changed is dropped")
	}
	r = feed(e, `"+P`)
	if !e.PasteClipboard(*r.Paste, "line\r\n") || e.Text() != "line\n1aba\nx" {
		t.Fatalf("a clipboard ending in a line break pastes lines: %q", e.Text())
	}
}

func TestVisualYankCopies(t *testing.T) {
	e := newAt(t, "|select 1\nfrom t")
	if r := feed(e, "vey"); r.Clipboard == nil || *r.Clipboard != "select" {
		t.Fatalf("v…y: %+v", r.Clipboard)
	}
	if r := feed(e, "Vjy"); r.Clipboard == nil || *r.Clipboard != "select 1\nfrom t\n" {
		t.Fatalf("V…y: %+v", r.Clipboard)
	}
}

func TestPutIsBounded(t *testing.T) {
	e := newAt(t, "|"+strings.Repeat("x", 1<<20))
	feed(e, "yy9999p")
	if n := len(e.Text()); n > maxPut+(2<<20) {
		t.Fatalf("pasted %d bytes", n)
	}
}

func TestUndoRedo(t *testing.T) {
	e := newAt(t, "|select id")
	feed(e, "dwxu")
	if got := show(e); got != "|id" {
		t.Fatalf("u undoes x: %q", got)
	}
	feed(e, "u")
	if got := show(e); got != "|select id" {
		t.Fatalf("u undoes dw: %q", got)
	}
	feed(e, "u")
	if e.Text() != "select id" {
		t.Fatal("nothing more to undo")
	}
	feed(e, "<c-r>")
	if e.Text() != "id" {
		t.Fatalf("ctrl+r: %q", e.Text())
	}
	feed(e, "2<c-r>2u")
	if e.Text() != "select id" {
		t.Fatalf("counts: %q", e.Text())
	}
	feed(e, "A from t<esc>")
	feed(e, "u")
	if e.Text() != "select id" {
		t.Fatalf("an INSERT session undoes at once: %q", e.Text())
	}
	feed(e, "Aa<left>b<esc>u")
	if e.Text() != "select ida" {
		t.Fatalf("moving in INSERT mode starts a new undo step: %q", e.Text())
	}
}
