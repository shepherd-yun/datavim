package keymap

// Kind is the kind of connection the UI is working on. Help and which-key
// list only the bindings that make sense on it; keys still resolve either
// way, and the actions refuse what does not apply.
type Kind int

const (
	// KindAny lists every binding: no connection is in view.
	KindAny Kind = iota
	KindSQL
	KindRedis
)

// kinds marks, per context, the actions that belong to one kind only.
var kinds = map[Context]map[Action]Kind{
	Global: {
		OpenStructure:     KindSQL,
		OpenFullStructure: KindSQL,
		CopyDDL:           KindSQL,
		ToggleTx:          KindSQL,
		CopyAs("sql"):     KindSQL,
		ExportAs("sql"):   KindSQL,
		OpenKey:           KindRedis,
		Favorites:         KindRedis,
	},
	Tree: {
		TruncateTable:  KindSQL,
		MarkCompare:    KindSQL,
		Compare:        KindSQL,
		ImportData:     KindSQL,
		YankQualified:  KindSQL,
		CopyDDL:        KindSQL,
		NewKey:         KindRedis,
		ToggleFavorite: KindRedis,
		BigKeys:        KindRedis,
		FilterPrompt:   KindRedis,
		FilterClear:    KindRedis,
	},
	Grid:      {CopyDDL: KindSQL, CopyAs("sql"): KindSQL},
	Results:   {CopyDDL: KindSQL, CopyAs("sql"): KindSQL},
	Structure: {CopyDDL: KindSQL},
}

// kindDescs replaces the description of a binding shared by both kinds,
// which otherwise reads for both at once.
var kindDescs = map[Context]map[Action][2]string{ // [SQL, Redis]
	Global: {
		History: {"SQL history", "Command history"},
	},
	Tree: {
		Open:        {"Open: table data, a column or index in the structure, a foreign key's table", "Open the key"},
		EditNode:    {"Edit connection / modify table", "Edit connection"},
		DeleteNode:  {"Delete connection / drop table or view", "Delete connection / delete Redis keys"},
		RenameTable: {"Rename table or view", "Rename Redis key"},
	},
}

// shows reports whether b is listed on kind.
func (b Binding) shows(kind Kind) bool {
	k := kinds[b.Context][b.Action]
	return kind == KindAny || k == KindAny || k == kind
}

// descFor is b's description on kind.
func (b Binding) descFor(kind Kind) string {
	if d, ok := kindDescs[b.Context][b.Action]; ok && kind != KindAny {
		return d[kind-KindSQL]
	}
	return b.Desc
}
