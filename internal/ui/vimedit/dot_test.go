package vimedit

import "testing"

func TestDotRepeats(t *testing.T) {
	runCases(t, []editCase{
		{"|a b c d", "dw.", "|c d", Normal},
		{"|a b c d e", "dw2.", "|d e", Normal},
		{"|a b c d e f", "2dw.", "|e f", Normal},
		{"|abcd", "x..", "|d", Normal},
		{"|a\nb\nc", "dd.", "|c", Normal},
		{"|a b", "cwx<esc>w.", "x |x", Normal},
		{"|a", "Ax<esc>.", "ax|x", Normal},
		{"|a", "ox<esc>.", "a\nx\n|x", Normal},
		{"|a\nb", "A;<esc>j.", "a;\nb|;", Normal},
		{"|a\n\nb", ">>jj.", "    a\n\n    |b", Normal},
		{"|ab", "ylp.", "aa|ab", Normal},
		{"|a b", "ywwdw.", "|a", Normal},              // y is not repeated: . deletes again
		{"|ab", ".", "|ab", Normal},                   // nothing to repeat yet
		{"|a b", "dwu.", "|b", Normal},                // u is not repeated
		{"|ab", "ix<left>y<esc>l.", "y|yxab", Normal}, // after a move in INSERT mode only the rest repeats
	})
}

func TestDotRepeatsInsertedNewlinesAndCompletions(t *testing.T) {
	e := newAt(t, "  |a")
	feed(e, "Ab<cr>c<esc>")
	feed(e, ".")
	if got := show(e); got != "  ab\n  cb\n  |c" {
		t.Fatalf("enter re-indents on repeat: %q", got)
	}
	e = newAt(t, "|x\ny")
	feed(e, "Afrom us")
	e.ReplaceBeforeCursor(e.Offset()-len("us"), "users")
	feed(e, "<esc>j.")
	if got := e.Text(); got != "xfrom users\nyfrom users" {
		t.Fatalf("a completion repeats: %q", got)
	}
}
