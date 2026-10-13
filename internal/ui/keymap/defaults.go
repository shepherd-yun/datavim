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
		{Global, keys("ctrl+c"), Cancel, "Cancel query"},
		{Global, keys(LeaderToken, "b"), ToggleTree, "Toggle tree"},
		{Global, keys(LeaderToken, "s"), OpenStructure, "Open structure"},
		{Global, keys(LeaderToken, "r"), Refresh, "Refresh"},
		{Global, keys(LeaderToken, "c"), OpenConsole, "Open console"},
		{Global, keys(LeaderToken, "n"), NewConnection, "New connection"},
		{Global, keys(LeaderToken, "h"), History, "SQL history"},
		{Global, keys(LeaderToken, "t"), ToggleTx, "Toggle manual commit"},
		{Global, keys(LeaderToken, LeaderToken), GlobalSearch, "Search everything"},
		{Global, keys(LeaderToken, "e"), Recent, "Recent tabs"},
	}
	for _, c := range []struct{ key, format, desc string }{
		{"t", "tsv", "Copy as TSV"},
		{"c", "csv", "Copy as CSV"},
		{"j", "json", "Copy as JSON"},
		{"m", "md", "Copy as Markdown"},
		{"i", "sql", "Copy as INSERT"},
	} {
		b = append(b, Binding{Global, keys(LeaderToken, "y", c.key), CopyAs(c.format), c.desc})
	}
	for _, e := range []struct{ key, format, desc string }{
		{"c", "csv", "Export as CSV"},
		{"j", "json", "Export as JSON"},
		{"s", "sql", "Export as SQL"},
		{"x", "xlsx", "Export as xlsx"},
	} {
		b = append(b, Binding{Global, keys(LeaderToken, "x", e.key), ExportAs(e.format), e.desc})
	}
	for n := 1; n <= 9; n++ {
		b = append(b, Binding{Global, keys(strconv.Itoa(n)), GotoTab(n), "Go to tab " + strconv.Itoa(n)})
	}

	b = append(b, vertical(Tree)...)
	b = append(b, search(Tree)...)
	b = append(b,
		Binding{Tree, keys("l"), Expand, "Expand / open"},
		Binding{Tree, keys("enter"), Expand, "Expand / open"},
		Binding{Tree, keys("right"), Expand, "Expand / open"},
		Binding{Tree, keys("h"), Collapse, "Collapse / parent"},
		Binding{Tree, keys("left"), Collapse, "Collapse / parent"},
		Binding{Tree, keys("p"), Password, "Enter password"},
		Binding{Tree, keys("e"), EditConnection, "Edit connection"},
		Binding{Tree, keys("d", "d"), DeleteConnection, "Delete connection"},
	)

	b = append(b, gridBindings(Grid)...)
	b = append(b,
		Binding{Grid, keys("["), PrevPage, "Previous page"},
		Binding{Grid, keys("]"), NextPage, "Next page"},
	)

	b = append(b, gridBindings(Results)...)
	b = append(b,
		Binding{Results, keys("]"), LoadMore, "Load more rows"},
		Binding{Results, keys("M"), ToggleMessages, "Toggle messages"},
		Binding{Results, keys("ctrl+k"), FocusEditor, "Focus editor"},
	)

	// The console's editing keys belong to its vim editor (vimedit), which
	// sees keys before the keymap; these are what it leaves alone.
	b = append(b,
		Binding{Console, keys("enter"), RunStatement, "Run statement under cursor / selection"},
		Binding{Console, keys("R"), RunAll, "Run all statements"},
		Binding{Console, keys("ctrl+j"), FocusResults, "Focus results"},
		Binding{Console, keys("M"), ToggleMessages, "Toggle messages"},
		Binding{Console, keys(LeaderToken, "v"), ExternalEdit, "Edit in $EDITOR"},
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

func search(ctx Context) []Binding {
	return []Binding{
		{ctx, keys("/"), Search, "Search"},
		{ctx, keys("n"), SearchNext, "Next match"},
		{ctx, keys("N"), SearchPrev, "Previous match"},
	}
}

// gridBindings are shared by the table-data grid and console results.
func gridBindings(ctx Context) []Binding {
	b := vertical(ctx)
	b = append(b, search(ctx)...)
	return append(b,
		Binding{ctx, keys("h"), Left, "Move left"},
		Binding{ctx, keys("left"), Left, "Move left"},
		Binding{ctx, keys("l"), Right, "Move right"},
		Binding{ctx, keys("right"), Right, "Move right"},
		Binding{ctx, keys("0"), LineStart, "First column"},
		Binding{ctx, keys("$"), LineEnd, "Last column"},
		Binding{ctx, keys("z", "h"), ScrollLeft, "Scroll left"},
		Binding{ctx, keys("z", "l"), ScrollRight, "Scroll right"},
		Binding{ctx, keys("y", "l"), YankCell, "Copy cell"},
		Binding{ctx, keys("y", "y"), YankRow, "Copy row"},
		Binding{ctx, keys("K"), CellDetail, "Show cell value"},
	)
}
