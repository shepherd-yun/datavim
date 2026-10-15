package keymap

import "strconv"

func keys(k ...string) []string { return k }

// DefaultBindings is the built-in keymap. See spec §6.
func DefaultBindings() []Binding {
	b := []Binding{
		{Global, keys("tab"), FocusNext, "Next pane"},
		{Global, keys("shift+tab"), FocusPrev, "Previous pane"},
		{Global, keys("ctrl+h"), FocusLeft, "Focus left pane (wraps)"},
		{Global, keys("ctrl+l"), FocusRight, "Focus right pane (wraps)"},
		{Global, keys("?"), Help, "Help"},
		{Global, keys(":"), CommandLine, "Command line"},
		{Global, keys("ctrl+c"), Cancel, "Cancel query"},
		{Global, keys(LeaderToken, "b"), ToggleTree, "Toggle tree"},
		{Global, keys(LeaderToken, "s"), OpenStructure, "Columns / structure"},
		{Global, keys(LeaderToken, "S"), OpenFullStructure, "Open structure"},
		{Global, keys(LeaderToken, "y", "d"), CopyDDL, "Copy DDL"},
		{Global, keys(LeaderToken, "r"), Refresh, "Refresh"},
		{Global, keys(LeaderToken, "c"), OpenConsole, "Open console"},
		{Global, keys(LeaderToken, "n"), NewConnection, "New connection"},
		{Global, keys(LeaderToken, "h"), History, "SQL history"},
		{Global, keys(LeaderToken, "t"), ToggleTx, "Toggle manual commit"},
		{Global, keys(LeaderToken, LeaderToken), GlobalSearch, "Search everything"},
		{Global, keys(LeaderToken, "e"), Recent, "Recent tabs"},
		{Global, keys(LeaderToken, "l"), Locate, "Locate the current tab in the tree"},
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
	b = append(b,
		Binding{Global, keys("L"), NextTab, "Next tab (wraps)"},
		Binding{Global, keys("g", "t"), NextTab, "Next tab (wraps)"},
		Binding{Global, keys("H"), PrevTab, "Previous tab (wraps)"},
		Binding{Global, keys("g", "T"), PrevTab, "Previous tab (wraps)"},
	)
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
		Binding{Tree, keys("e"), EditNode, "Edit connection / modify table"},
		Binding{Tree, keys("d", "d"), DeleteNode, "Delete connection / drop table or view"},
		Binding{Tree, keys("r"), RenameTable, "Rename table or view"},
		Binding{Tree, keys("a"), NodeActions, "Actions on the selected node"},
		Binding{Tree, keys("m"), MarkCompare, "Mark as the compare source"},
		Binding{Tree, keys("C"), Compare, "Compare with the marked node"},
		Binding{Tree, keys("y", "y"), YankName, "Copy name"},
		Binding{Tree, keys("y", "q"), YankQualified, "Copy qualified name (db.table)"},
	)

	b = append(b, gridBindings(Grid)...)
	b = append(b,
		Binding{Grid, keys("["), PrevPage, "Previous page"},
		Binding{Grid, keys("]"), NextPage, "Next page"},
		Binding{Grid, keys("f", "f"), FilterEqual, "Filter: equals this value"},
		Binding{Grid, keys("f", "!"), FilterNotEqual, "Filter: not this value"},
		Binding{Grid, keys("f", "s"), FilterContains, "Filter: contains… (LIKE %…%)"},
		Binding{Grid, keys("f", "~"), FilterContains, "Filter: contains… (LIKE %…%)"},
		Binding{Grid, keys("f", "l"), FilterLike, "Filter: LIKE pattern… (% any, _ one char)"},
		Binding{Grid, keys("f", ">"), FilterGreater, "Filter: greater than…"},
		Binding{Grid, keys("f", "<"), FilterLess, "Filter: less than…"},
		Binding{Grid, keys("f", "n"), FilterNull, "Filter: is NULL"},
		Binding{Grid, keys("f", "N"), FilterNotNull, "Filter: is not NULL"},
		Binding{Grid, keys("f", "x"), FilterClear, "Clear the filter"},
		Binding{Grid, keys("F"), FilterPrompt, "Edit the filter (WHERE)"},
		Binding{Grid, keys("s"), SortColumn, "Sort by column: asc, desc, off"},
		Binding{Grid, keys("i"), EditCell, "Edit cell"},
		Binding{Grid, keys("enter"), EditCell, "Edit cell"},
		Binding{Grid, keys("o"), InsertRow, "Add a row below"},
		Binding{Grid, keys("d", "d"), DeleteRow, "Delete row"},
		Binding{Grid, keys("u"), UndoEdit, "Undo the last change"},
		Binding{Grid, keys("U"), UndoAllEdits, "Discard every change"},
		Binding{Grid, keys(LeaderToken, "w"), SubmitEdits, "Submit changes (:w)"},
	)

	b = append(b, gridBindings(Results)...)
	b = append(b,
		Binding{Results, keys("]"), LoadMore, "Load more rows"},
		Binding{Results, keys("M"), ToggleMessages, "Toggle messages"},
		Binding{Results, keys("ctrl+k"), FocusEditor, "Focus editor"},
		Binding{Results, keys("ctrl+j"), FocusEditor, "Focus editor (wraps)"},
	)

	// The console's editing keys belong to its vim editor (vimedit), which
	// sees keys before the keymap; these are what it leaves alone.
	b = append(b,
		Binding{Console, keys("enter"), RunStatement, "Run statement under cursor / selection"},
		Binding{Console, keys("R"), RunAll, "Run all statements"},
		Binding{Console, keys("ctrl+j"), FocusResults, "Focus results"},
		Binding{Console, keys("ctrl+k"), FocusResults, "Focus results (wraps)"},
		Binding{Console, keys("M"), ToggleMessages, "Toggle messages"},
		Binding{Console, keys(LeaderToken, "v"), ExternalEdit, "Edit in $EDITOR"},
	)

	b = append(b, vertical(Structure)...)
	b = append(b,
		Binding{Structure, keys("["), PrevSection, "Previous section"},
		Binding{Structure, keys("]"), NextSection, "Next section"},
		Binding{Structure, keys("h"), Left, "Previous field"},
		Binding{Structure, keys("left"), Left, "Previous field"},
		Binding{Structure, keys("l"), Right, "Next field"},
		Binding{Structure, keys("right"), Right, "Next field"},
		Binding{Structure, keys("y", "y"), YankName, "Copy the name under the cursor (DDL: all of it)"},
		Binding{Structure, keys("e"), ModifyTable, "Modify the table / stop modifying"},
		Binding{Structure, keys("i"), EditCell, "Edit the field"},
		Binding{Structure, keys("enter"), EditCell, "Edit the field"},
		Binding{Structure, keys("o"), InsertRow, "Add a column or index below"},
		Binding{Structure, keys("d", "d"), DeleteRow, "Delete the column or index"},
		Binding{Structure, keys("u"), UndoEdit, "Undo the last change"},
		Binding{Structure, keys("U"), UndoAllEdits, "Discard every change"},
		Binding{Structure, keys(LeaderToken, "w"), SubmitEdits, "Submit changes (:w)"},
	)

	b = append(b, vertical(Diff)...)
	b = append(b,
		Binding{Diff, keys("l"), Expand, "Expand the table · data: scroll columns right"},
		Binding{Diff, keys("enter"), Expand, "Expand the table · data: scroll columns right"},
		Binding{Diff, keys("right"), Expand, "Expand the table · data: scroll columns right"},
		Binding{Diff, keys("h"), Collapse, "Collapse the table · data: scroll columns left"},
		Binding{Diff, keys("left"), Collapse, "Collapse the table · data: scroll columns left"},
		Binding{Diff, keys("x"), ToggleChange, "Include / skip in the script"},
		Binding{Diff, keys("~"), SwapSides, "Swap source and target"},
		Binding{Diff, keys("]", "c"), NextChange, "Next change"},
		Binding{Diff, keys("[", "c"), PrevChange, "Previous change"},
		Binding{Diff, keys(LeaderToken, "w"), WriteScript, "Write the sync script to a new console"},
		Binding{Diff, keys("D"), DataDiff, "Compare the data / back to the structure"},
		Binding{Diff, keys("F"), FilterPrompt, "Filter the compared rows (WHERE on both sides)"},
	)

	b = append(b,
		Binding{Popup, keys("ctrl+o"), OpenFullStructure, "Open structure"},
		Binding{Popup, keys("ctrl+s"), SaveForm, "Save the form"},
		Binding{Popup, keys("tab"), NextSearchScope, "Search: next scope (objects, consoles, history)"},
		Binding{Popup, keys("shift+tab"), PrevSearchScope, "Search: previous scope"},
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
		Binding{ctx, keys("z", "h"), ScrollHalfLeft, "Scroll half a screen left"},
		Binding{ctx, keys("z", "l"), ScrollHalfRight, "Scroll half a screen right"},
		Binding{ctx, keys("z", "H"), ScrollLeft, "Scroll left one column"},
		Binding{ctx, keys("z", "L"), ScrollRight, "Scroll right one column"},
		Binding{ctx, keys("y", "l"), YankCell, "Copy cell"},
		Binding{ctx, keys("y", "y"), YankRow, "Copy row"},
		Binding{ctx, keys("K"), CellDetail, "Show cell value"},
	)
}
