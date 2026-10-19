package vimedit

// HelpLine is one line of the editor's key help.
type HelpLine struct{ Keys, Desc string }

// Help lists the editor's keys for the ? overlay.
func Help() []HelpLine {
	return []HelpLine{
		{"h j k l w b e W B E", "Move by rune / word"},
		{"0 ^ $ gg G f F t T % { }", "Move in the line / text"},
		{"<ctrl+d> <ctrl+u>", "Half page down / up"},
		{"i a I A o O", "Insert"},
		{"<ctrl+a> <ctrl+e> <ctrl+w> <ctrl+u>", "In INSERT: line start / end, delete a word / to line start"},
		{"v V", "Visual / visual line; enter runs the selection"},
		{"d c y > <", "Operators, with a motion, iw aw i\" a\" i' a' i( a( or doubled"},
		{"x D C p P", "Delete rune / to line end, change to line end, put"},
		{"u <ctrl+r> .", "Undo, redo, repeat the last change"},
		{"\"a-\"z \"+", "Registers, shared by every editor; y also copies to the system clipboard, \"+p pastes from it"},
		{"gt gT {n}gt", "Next / previous / nth tab"},
	}
}
