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
		{Global, keys(LeaderToken, "e"), ToggleTree, "Toggle tree"},
		{Global, keys("+"), FullScreen, "Full screen: the focused pane"},
		{Global, keys("="), FullScreen, "Full screen: the focused pane"},
		{Global, keys("-"), ExitFullScreen, "Leave full screen"},
		{Global, keys("_"), ExitFullScreen, "Leave full screen"},
		{Global, keys(LeaderToken, "s"), OpenStructure, "Columns / structure"},
		{Global, keys(LeaderToken, "S"), OpenFullStructure, "Open structure"},
		{Global, keys(LeaderToken, "y", "d"), CopyDDL, "Copy DDL"},
		{Global, keys(LeaderToken, "r"), Refresh, "Refresh"},
		{Global, keys(LeaderToken, "c"), OpenConsole, "Open console"},
		{Global, keys(LeaderToken, "n"), NewConnection, "New connection"},
		{Global, keys(LeaderToken, "h"), History, "SQL history"},
		{Global, keys(LeaderToken, "t"), ToggleTx, "Toggle manual commit"},
		{Global, keys(LeaderToken, LeaderToken), GlobalSearch, "Search everything"},
		{Global, keys(LeaderToken, "f"), Recent, "Recent tabs and files"},
		{Global, keys(LeaderToken, "l"), Locate, "Locate the current tab in the tree"},
		{Global, keys(LeaderToken, "k"), OpenKey, "Open a Redis key by name"},
		{Global, keys(LeaderToken, "i"), ServerInfo, "Server status: load, sessions, locks, …"},
		{Global, keys(LeaderToken, "F"), Favorites, "Starred Redis keys"},
	}
	for _, c := range copyFormats {
		b = append(b, Binding{Global, keys(LeaderToken, "y", c.key), CopyAs(c.format), c.desc})
	}
	for _, e := range []struct{ key, format, desc string }{
		{"c", "csv", "Export as CSV"},
		{"j", "json", "Export as JSON"},
		{"s", "sql", "Export as SQL"},
		{"x", "xlsx", "Export as xlsx"},
		{"l", "jsonl", "Export messages as JSON Lines"},
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
		Binding{Tree, keys("l"), Expand, "Expand"},
		Binding{Tree, keys("right"), Expand, "Expand"},
		Binding{Tree, keys("enter"), Open, "Open: table data, a column or index in the structure, a foreign key's table"},
		Binding{Tree, keys("h"), Collapse, "Collapse / parent"},
		Binding{Tree, keys("left"), Collapse, "Collapse / parent"},
		Binding{Tree, keys("p"), Password, "Enter password"},
		Binding{Tree, keys("e"), EditNode, "Edit connection / modify table"},
		Binding{Tree, keys("d", "d"), DeleteNode, "Delete connection / drop table or view / delete Redis keys or a topic"},
		Binding{Tree, keys("r"), RenameTable, "Rename table, view or Redis key"},
		Binding{Tree, keys("T"), TruncateTable, "Truncate table"},
		Binding{Tree, keys("a"), NodeActions, "Actions on the selected node"},
		Binding{Tree, keys("o"), NewKey, "New Redis key"},
		Binding{Tree, keys("*"), ToggleFavorite, "Star / unstar the Redis key"},
		Binding{Tree, keys("B"), BigKeys, "Find the biggest Redis keys"},
		Binding{Tree, keys("m"), MarkCompare, "Mark as the compare source"},
		Binding{Tree, keys("C"), Compare, "Compare with the marked node"},
		Binding{Tree, keys("I"), ImportData, "Import data from a file (database or schema: into a new table)"},
		Binding{Tree, keys("y", "y"), YankName, "Copy name"},
		Binding{Tree, keys("y", "q"), YankQualified, "Copy qualified name (db.table)"},
		Binding{Tree, keys("y", "d"), CopyDDL, "Copy DDL"},
		Binding{Tree, keys("F"), FilterPrompt, "Filter Redis keys (pattern)"},
		Binding{Tree, keys("f", "x"), FilterClear, "Clear the Redis key filter"},
		Binding{Tree, keys("z", "o"), FoldOpen, "Expand"},
		Binding{Tree, keys("z", "c"), FoldClose, "Collapse (on a leaf, its parent)"},
		Binding{Tree, keys("z", "a"), FoldToggle, "Toggle expanded"},
		Binding{Tree, keys("z", "O"), FoldOpenAll, "Expand everything loaded under the cursor"},
		Binding{Tree, keys("z", "C"), FoldCloseAll, "Collapse everything under the cursor"},
		Binding{Tree, keys("z", "R"), FoldOpenTree, "Expand everything loaded"},
		Binding{Tree, keys("z", "M"), FoldCloseTree, "Collapse everything"},
		Binding{Tree, keys("Z"), FoldCloseTree, "Collapse everything"},
		Binding{Tree, keys("z", "x"), FoldFocus, "Collapse all but the path to the cursor"},
		Binding{Tree, keys("."), ToggleInternal, "Show / hide internal topics"},
		Binding{Tree, keys("s"), SortGroups, "Sort consumer groups: by lag / by name"},
	)

	b = append(b, gridBindings(Grid)...)
	b = append(b,
		Binding{Grid, keys("["), PrevPage, "Previous page"},
		Binding{Grid, keys("]"), NextPage, "Next page"},
		Binding{Grid, keys("s"), SortColumn, "Sort by column: asc, desc, off"},
	)
	b = append(b, filterEditBindings(Grid)...)

	b = append(b, gridBindings(Results)...)
	b = append(b, filterEditBindings(Results)...)
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
		Binding{Console, keys(LeaderToken, "R"), RunFromCursor, "Run from the statement under the cursor to the end"},
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
		Binding{Structure, keys("y", "d"), CopyDDL, "Copy DDL"},
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

	b = append(b, gridBindings(RedisKey)...)
	b = append(b,
		Binding{RedisKey, keys("]"), NextPage, "Load more"},
		Binding{RedisKey, keys("v"), ValueFormat, "Value format: auto, text, JSON, hex"},
		Binding{RedisKey, keys("F"), FilterPrompt, "Filter fields / members (pattern)"},
		Binding{RedisKey, keys("f", "x"), FilterClear, "Clear the filter"},
		Binding{RedisKey, keys("i"), EditCell, "Edit the cell"},
		Binding{RedisKey, keys("enter"), EditCell, "Edit the cell"},
		Binding{RedisKey, keys("o"), InsertRow, "Add a field / element / member / entry"},
		Binding{RedisKey, keys("d", "d"), DeleteRow, "Delete the field / element / member / entry"},
		Binding{RedisKey, keys("u"), UndoEdit, "Undo the last change"},
		Binding{RedisKey, keys("U"), UndoAllEdits, "Discard every change"},
		Binding{RedisKey, keys(LeaderToken, "w"), SubmitEdits, "Submit changes (:w)"},
		Binding{RedisKey, keys(LeaderToken, "v"), ExternalEdit, "Edit the value in $EDITOR"},
		Binding{RedisKey, keys("t"), SetTTL, "Set or remove the TTL"},
		Binding{RedisKey, keys("r"), RenameTable, "Rename the key"},
		Binding{RedisKey, keys("D"), DeleteNode, "Delete the key"},
		Binding{RedisKey, keys("*"), ToggleFavorite, "Star / unstar the Redis key"},
	)

	b = append(b, gridBindings(RedisResults)...)
	b = append(b,
		Binding{RedisResults, keys("M"), ToggleMessages, "Toggle messages"},
		Binding{RedisResults, keys("g", "d"), OpenCellKey, "Open the Redis key in this cell"},
		Binding{RedisResults, keys("ctrl+k"), FocusEditor, "Focus editor"},
		Binding{RedisResults, keys("ctrl+j"), FocusEditor, "Focus editor (wraps)"},
	)

	b = append(b, gridBindings(RedisServer)...)
	b = append(b,
		Binding{RedisServer, keys("["), PrevSection, "Previous section"},
		Binding{RedisServer, keys("]"), NextSection, "Next section"},
		Binding{RedisServer, keys("d", "d"), KillClient, "Kill the client (clients list)"},
	)

	b = append(b, gridBindings(DBServer)...)
	b = append(b,
		Binding{DBServer, keys("["), PrevSection, "Previous section"},
		Binding{DBServer, keys("]"), NextSection, "Next section"},
		Binding{DBServer, keys("d", "d"), KillClient, "Disconnect the session (sessions list)"},
		Binding{DBServer, keys("c"), CancelSessionQuery, "Cancel the session's query (sessions list)"},
		Binding{DBServer, keys("a"), ToggleIdle, "Show / hide idle sessions"},
		Binding{DBServer, keys("F"), FilterPrompt, "Filter by name (variables)"},
		Binding{DBServer, keys("f", "x"), FilterClear, "Clear the filter"},
	)

	b = append(b, gridBindings(RedisBigKeys)...)
	b = append(b, Binding{RedisBigKeys, keys("enter"), Open, "Open the key"})

	for _, ctx := range []Context{KafkaTopic, KafkaMessages, KafkaGroup, KafkaServer, KafkaConfig} {
		b = append(b, gridBindings(ctx)...)
		b = append(b,
			Binding{ctx, keys("["), PrevSection, "Previous section"},
			Binding{ctx, keys("]"), NextSection, "Next section"},
		)
	}
	b = append(b,
		Binding{KafkaTopic, keys("enter"), Open, "Open the group (consumers)"},
		Binding{KafkaMessages, keys("v"), ValueFormat, "Value format: auto, text, JSON, hex, base64"},
		Binding{KafkaMessages, keys("F"), FilterPrompt, "Filter messages (key, value, header.x, value.path …)"},
		Binding{KafkaMessages, keys("f", "f"), FilterEqual, "Filter: equals this value"},
		Binding{KafkaMessages, keys("f", "!"), FilterNotEqual, "Filter: not this value"},
		Binding{KafkaMessages, keys("f", "s"), FilterContains, "Filter: contains this value"},
		Binding{KafkaMessages, keys("f", "~"), FilterContains, "Filter: contains this value"},
		Binding{KafkaMessages, keys("f", "x"), FilterClear, "Clear the filter"},
		Binding{KafkaMessages, keys("t"), Seek, "Jump to an offset, a time or a partition"},
		Binding{KafkaMessages, keys("T"), LiveTail, "Live tail: on / pause"},
		Binding{KafkaMessages, keys("c"), ToggleHeaders, "Show / hide the headers column"},
		Binding{KafkaMessages, keys("g", "d"), OpenCellKey, "Back to the group this view came from"},
		Binding{KafkaMessages, keys(LeaderToken, "v"), ExternalEdit, "Open the value in $EDITOR (read-only)"},
		Binding{KafkaGroup, keys("g", "d"), OpenCellKey, "Jump to the message this group is stuck on"},
		Binding{KafkaGroup, keys("R"), ResetOffsets, "Reset offsets…"},
		Binding{KafkaServer, keys("enter"), Open, "Open the topic's partitions"},
		Binding{KafkaConfig, keys("i"), EditCell, "Edit the config value"},
		Binding{KafkaConfig, keys("enter"), EditCell, "Edit the config value"},
		Binding{KafkaConfig, keys("d", "d"), DeleteRow, "Back to the default (removes the override)"},
		Binding{KafkaConfig, keys("u"), UndoEdit, "Undo the last change"},
		Binding{KafkaConfig, keys("U"), UndoAllEdits, "Discard every change"},
		Binding{KafkaConfig, keys(LeaderToken, "w"), SubmitEdits, "Apply the config changes (:w)"},
		Binding{KafkaTopic, keys("o"), AddPartitions, "Add partitions…"},
		Binding{KafkaTopic, keys("X"), DeleteRecords, "Delete messages before an offset…"},
		Binding{KafkaMessages, keys("X"), DeleteRecords, "Delete messages up to this one…"},
		Binding{KafkaMessages, keys("p"), ResendMessage, "Copy the message to a producer console"},
		Binding{KafkaMessages, keys("P"), ResendShown, "Copy every shown message to a producer console"},
		Binding{KafkaMessages, keys("I"), ToggleIsolation, "Isolation: read_committed / read_uncommitted"},
		Binding{KafkaGroup, keys("X"), DeleteOffsets, "Delete the group's offsets on this topic…"},
	)
	b = append(b, vertical(KafkaSchema)...)
	b = append(b, search(KafkaSchema)...)
	b = append(b,
		Binding{KafkaSchema, keys("["), PrevSection, "Previous section"},
		Binding{KafkaSchema, keys("]"), NextSection, "Next section"},
		Binding{KafkaSchema, keys("enter"), Open, "Show this version"},
		Binding{KafkaSchema, keys("m"), MarkCompare, "Mark the version to compare"},
		Binding{KafkaSchema, keys("C"), Compare, "Diff with the marked version (or the latest)"},
		Binding{KafkaSchema, keys(LeaderToken, "v"), ExternalEdit, "Register a new version (edit in $EDITOR)"},
		Binding{KafkaSchema, keys("c"), SetCompatibility, "Set the compatibility level…"},
		Binding{KafkaSchema, keys("d", "d"), DeleteNode, "Soft-delete the version"},
	)
	b = append(b, gridBindings(KafkaProduceResults)...)
	b = append(b,
		Binding{KafkaProduceResults, keys("M"), ToggleMessages, "Toggle messages"},
		Binding{KafkaProduceResults, keys("ctrl+k"), FocusEditor, "Focus editor"},
		Binding{KafkaProduceResults, keys("ctrl+j"), FocusEditor, "Focus editor (wraps)"},
	)

	b = append(b, vertical(Import)...)
	b = append(b,
		Binding{Import, keys("["), PrevSection, "Previous section"},
		Binding{Import, keys("]"), NextSection, "Next section"},
		Binding{Import, keys("i"), EditCell, "Edit the setting, the target column or the new column's name"},
		Binding{Import, keys("enter"), EditCell, "Edit the setting, the target column or the new column's name"},
		Binding{Import, keys("x"), ImportToggle, "Import / skip the column"},
		Binding{Import, keys("a"), ImportAll, "Import every column"},
		Binding{Import, keys("A"), ImportNone, "Skip every column"},
		Binding{Import, keys("t"), ImportType, "New table: the column's type"},
		Binding{Import, keys(LeaderToken, "w"), SubmitEdits, "Check the file and import it"},
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

// copyFormats are the result formats under <leader>y, and under y in the
// grids.
var copyFormats = []struct{ key, format, desc string }{
	{"t", "tsv", "Copy as TSV"},
	{"c", "csv", "Copy as CSV"},
	{"j", "json", "Copy as JSON"},
	{"m", "md", "Copy as Markdown"},
	{"i", "sql", "Copy as INSERT"},
}

// gridBindings are shared by the table-data grid and console results.
func gridBindings(ctx Context) []Binding {
	b := vertical(ctx)
	b = append(b, search(ctx)...)
	for _, c := range copyFormats {
		b = append(b, Binding{ctx, keys("y", c.key), CopyAs(c.format), c.desc})
	}
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
		Binding{ctx, keys("y", "d"), CopyDDL, "Copy DDL"},
		Binding{ctx, keys("K"), CellDetail, "Show cell value"},
	)
}

// filterEditBindings are the filter and edit keys shared by table data and
// console results.
func filterEditBindings(ctx Context) []Binding {
	return []Binding{
		{ctx, keys("f", "f"), FilterEqual, "Filter: equals this value"},
		{ctx, keys("f", "!"), FilterNotEqual, "Filter: not this value"},
		{ctx, keys("f", "s"), FilterContains, "Filter: contains… (LIKE %…%)"},
		{ctx, keys("f", "~"), FilterContains, "Filter: contains… (LIKE %…%)"},
		{ctx, keys("f", "l"), FilterLike, "Filter: LIKE pattern… (% any, _ one char)"},
		{ctx, keys("f", ">"), FilterGreater, "Filter: greater than…"},
		{ctx, keys("f", "<"), FilterLess, "Filter: less than…"},
		{ctx, keys("f", "n"), FilterNull, "Filter: is NULL"},
		{ctx, keys("f", "N"), FilterNotNull, "Filter: is not NULL"},
		{ctx, keys("f", "x"), FilterClear, "Clear the filter"},
		{ctx, keys("F"), FilterPrompt, "Edit the filter (WHERE)"},
		{ctx, keys("i"), EditCell, "Edit cell"},
		{ctx, keys("enter"), EditCell, "Edit cell"},
		{ctx, keys("o"), InsertRow, "Add a row below"},
		{ctx, keys("d", "d"), DeleteRow, "Delete row"},
		{ctx, keys("u"), UndoEdit, "Undo the last change"},
		{ctx, keys("U"), UndoAllEdits, "Discard every change"},
		{ctx, keys(LeaderToken, "w"), SubmitEdits, "Submit changes (:w)"},
	}
}
