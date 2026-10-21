package keymap

import "slices"

// Kind is the kind of connection the UI is working on. Help and which-key
// list only the bindings that make sense on it; keys still resolve either
// way, and the actions refuse what does not apply.
type Kind int

const (
	// KindAny lists every binding: no connection is in view.
	KindAny Kind = iota
	KindSQL
	KindRedis
	KindKafka
)

// sql, redis and kafka are the kind sets of kinds.
var (
	sql        = []Kind{KindSQL}
	redis      = []Kind{KindRedis}
	kafka      = []Kind{KindKafka}
	redisKafka = []Kind{KindRedis, KindKafka}
	sqlRedis   = []Kind{KindSQL, KindRedis}
)

// kinds marks, per context, the actions that belong to some kinds only.
var kinds = map[Context]map[Action][]Kind{
	Global: {
		OpenStructure:     sql,
		OpenFullStructure: sql,
		CopyDDL:           sql,
		ToggleTx:          sql,
		CopyAs("sql"):     sql,
		ExportAs("sql"):   sql,
		ExportAs("jsonl"): kafka,
		OpenKey:           redis,
		Favorites:         redis,
	},
	Tree: {
		TruncateTable:  sql,
		MarkCompare:    sql,
		Compare:        sql,
		ImportData:     sql,
		YankQualified:  sql,
		CopyDDL:        sql,
		RenameTable:    sqlRedis,
		NewKey:         redisKafka,
		ToggleFavorite: redis,
		BigKeys:        redis,
		FilterPrompt:   redisKafka,
		FilterClear:    redisKafka,
		ToggleInternal: kafka,
		SortGroups:     kafka,
	},
	Grid:      {CopyDDL: sql, CopyAs("sql"): sql},
	Results:   {CopyDDL: sql, CopyAs("sql"): sql},
	Structure: {CopyDDL: sql},
}

// kindDescs replaces the description of a binding shared by several
// kinds, which otherwise reads for all of them at once.
var kindDescs = map[Context]map[Action][3]string{ // [SQL, Redis, Kafka]
	Global: {
		History:     {"SQL history", "Command history", "SQL and Redis command history"},
		OpenConsole: {"Open console", "Open console", "Producer console for the topic"},
	},
	Tree: {
		Open:         {"Open: table data, a column or index in the structure, a foreign key's table", "Open the key", "Open the topic / group"},
		EditNode:     {"Edit connection / modify table", "Edit connection", "Edit connection"},
		DeleteNode:   {"Delete connection / drop table or view", "Delete connection / delete Redis keys", "Delete connection / delete topic / delete group / soft-delete schema version"},
		RenameTable:  {"Rename table or view", "Rename Redis key", ""},
		NewKey:       {"", "New Redis key", "New topic"},
		FilterPrompt: {"", "Filter Redis keys (pattern)", "Filter topics / groups / subjects by name"},
		FilterClear:  {"", "Clear the Redis key filter", "Clear the topic / group / subject filter"},
	},
}

// shows reports whether b is listed on kind.
func (b Binding) shows(kind Kind) bool {
	ks := kinds[b.Context][b.Action]
	return kind == KindAny || ks == nil || slices.Contains(ks, kind)
}

// descFor is b's description on kind.
func (b Binding) descFor(kind Kind) string {
	if d, ok := kindDescs[b.Context][b.Action]; ok && kind != KindAny && d[kind-KindSQL] != "" {
		return d[kind-KindSQL]
	}
	return b.Desc
}
