package keymap

import "strconv"

func keys(k ...string) []string { return k }

// DefaultBindings is the built-in keymap. See spec §6.
func DefaultBindings() []Binding {
	b := []Binding{
		{Global, keys("tab"), FocusNext, "Next pane"},
		{Global, keys("shift+tab"), FocusPrev, "Previous pane"},
		{Global, keys("ctrl+h"), FocusLeft, "Focus tree"},
		{Global, keys("ctrl+l"), FocusRight, "Focus tabs"},
		{Global, keys("?"), Help, "Help"},
		{Global, keys(":"), CommandLine, "Command line"},
		{Global, keys("ctrl+c"), Quit, "Quit"},
		{Global, keys(LeaderToken, "b"), ToggleTree, "Toggle tree"},
		{Global, keys(LeaderToken, "s"), OpenStructure, "Open structure"},
		{Global, keys(LeaderToken, "r"), Refresh, "Refresh"},
	}
	for n := 1; n <= 9; n++ {
		b = append(b, Binding{Global, keys(strconv.Itoa(n)), GotoTab(n), "Go to tab " + strconv.Itoa(n)})
	}
	b = append(b, vertical(Tree)...)
	b = append(b,
		Binding{Tree, keys("l"), Expand, "Expand / open"},
		Binding{Tree, keys("enter"), Expand, "Expand / open"},
		Binding{Tree, keys("right"), Expand, "Expand / open"},
		Binding{Tree, keys("h"), Collapse, "Collapse / parent"},
		Binding{Tree, keys("left"), Collapse, "Collapse / parent"},
		Binding{Tree, keys("p"), Password, "Enter password"},
	)
	b = append(b, vertical(Grid)...)
	b = append(b,
		Binding{Grid, keys("h"), Left, "Move left"},
		Binding{Grid, keys("left"), Left, "Move left"},
		Binding{Grid, keys("l"), Right, "Move right"},
		Binding{Grid, keys("right"), Right, "Move right"},
		Binding{Grid, keys("0"), LineStart, "First column"},
		Binding{Grid, keys("$"), LineEnd, "Last column"},
		Binding{Grid, keys("["), PrevPage, "Previous page"},
		Binding{Grid, keys("]"), NextPage, "Next page"},
		Binding{Grid, keys("K"), CellDetail, "Show cell value"},
	)
	b = append(b, vertical(Structure)...)
	b = append(b,
		Binding{Structure, keys("["), PrevSection, "Previous section"},
		Binding{Structure, keys("]"), NextSection, "Next section"},
	)
	return b
}

func vertical(ctx Context) []Binding {
	return []Binding{
		{ctx, keys("j"), Down, "Move down"},
		{ctx, keys("down"), Down, "Move down"},
		{ctx, keys("k"), Up, "Move up"},
		{ctx, keys("up"), Up, "Move up"},
		{ctx, keys("g", "g"), Top, "Go to top"},
		{ctx, keys("G"), Bottom, "Go to bottom"},
		{ctx, keys("ctrl+d"), HalfPageDown, "Half page down"},
		{ctx, keys("ctrl+u"), HalfPageUp, "Half page up"},
	}
}
