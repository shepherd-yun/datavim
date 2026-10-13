package vimedit

import (
	"strings"
	"testing"
)

func TestBufferOffsetsCountBytes(t *testing.T) {
	b := newBuffer("ab\n李c\n")
	for _, c := range []struct {
		p   Pos
		off int
	}{
		{Pos{0, 0}, 0},
		{Pos{0, 2}, 2},
		{Pos{1, 0}, 3},
		{Pos{1, 1}, 6}, // after the three bytes of 李
		{Pos{1, 2}, 7},
		{Pos{2, 0}, 8},
	} {
		if got := b.offset(c.p); got != c.off {
			t.Errorf("offset(%v) = %d, want %d", c.p, got, c.off)
		}
		if got := b.pos(c.off); got != c.p {
			t.Errorf("pos(%d) = %v, want %v", c.off, got, c.p)
		}
	}
	if got := b.pos(4); got != (Pos{1, 0}) {
		t.Errorf("an offset inside a rune is that rune: %v", got)
	}
	if got := b.pos(99); got != (Pos{2, 0}) {
		t.Errorf("pos past the end = %v", got)
	}
	if got := b.offset(Pos{9, 9}); got != 8 {
		t.Errorf("offset past the end = %d", got)
	}
}

func TestBufferReplaceAndSlice(t *testing.T) {
	b := newBuffer("select 1\nfrom t")
	end := b.replace(Pos{0, 7}, Pos{1, 4}, "*\n  from")
	if got := b.Text(); got != "select *\n  from t" {
		t.Fatalf("text = %q", got)
	}
	if end != (Pos{1, 6}) {
		t.Fatalf("end = %v", end)
	}
	if got := b.slice(Pos{0, 7}, Pos{1, 2}); got != "*\n  " {
		t.Fatalf("slice = %q", got)
	}
	if got := b.slice(Pos{1, 2}, Pos{0, 7}); got != "*\n  " {
		t.Fatalf("reversed slice = %q", got)
	}
	v := b.version
	b.replace(Pos{0, 0}, b.end(), "")
	if b.Text() != "" || len(b.lines) != 1 || b.version == v {
		t.Fatalf("emptied: %q %d lines, version %d", b.Text(), len(b.lines), b.version)
	}
}

func TestAfter(t *testing.T) {
	if got := after(Pos{2, 3}, "李x"); got != (Pos{2, 5}) {
		t.Errorf("same line: %v", got)
	}
	if got := after(Pos{2, 3}, "a\nbc\n李"); got != (Pos{4, 1}) {
		t.Errorf("lines: %v", got)
	}
}

func TestHistoryUndoRedo(t *testing.T) {
	b := newBuffer("abc")
	var h history
	apply := func(from, to Pos, s string) {
		h.record(edit{at: from, old: b.slice(from, to), new: s}, from)
		b.replace(from, to, s)
	}
	apply(Pos{0, 3}, Pos{0, 3}, "d") // typing: two inserts merge
	apply(Pos{0, 4}, Pos{0, 4}, "\ne")
	if len(h.open.edits) != 1 {
		t.Fatalf("consecutive inserts merge: %d edits", len(h.open.edits))
	}
	h.commit()
	apply(Pos{0, 0}, Pos{0, 1}, "") // delete "a"
	h.commit()
	if b.Text() != "bcd\ne" {
		t.Fatalf("text = %q", b.Text())
	}
	if p, ok := h.undoStep(&b); !ok || b.Text() != "abcd\ne" || p != (Pos{0, 0}) {
		t.Fatalf("undo 1: %q %v", b.Text(), p)
	}
	if _, ok := h.undoStep(&b); !ok || b.Text() != "abc" {
		t.Fatalf("undo 2: %q", b.Text())
	}
	if _, ok := h.undoStep(&b); ok {
		t.Fatal("nothing left to undo")
	}
	if p, ok := h.redoStep(&b); !ok || b.Text() != "abcd\ne" || p != (Pos{0, 3}) {
		t.Fatalf("redo: %q %v", b.Text(), p)
	}
	apply(Pos{0, 0}, Pos{0, 0}, "x")
	if _, ok := h.redoStep(&b); ok {
		t.Fatal("a new edit drops the redo history")
	}
}

func TestHistoryIsBounded(t *testing.T) {
	b := newBuffer("")
	var h history
	for range maxUndo + 10 {
		h.record(edit{at: Pos{}, new: "x"}, Pos{})
		b.replace(Pos{}, Pos{}, "x")
		h.commit()
	}
	if len(h.undo) != maxUndo {
		t.Fatalf("undo steps = %d", len(h.undo))
	}
	if strings.Count(b.Text(), "x") != maxUndo+10 {
		t.Fatal("text")
	}
}
