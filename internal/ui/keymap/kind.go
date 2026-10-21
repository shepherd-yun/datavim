package keymap

import (
	"maps"
	"slices"
)

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
	KindES
)

// sql, redis, kafka and es are the kind sets of kinds.
var (
	sql          = []Kind{KindSQL}
	redis        = []Kind{KindRedis}
	kafka        = []Kind{KindKafka}
	es           = []Kind{KindES}
	redisES      = []Kind{KindRedis, KindES}
	kafkaES      = []Kind{KindKafka, KindES}
	redisKafkaES = []Kind{KindRedis, KindKafka, KindES}
	sqlRedis     = []Kind{KindSQL, KindRedis}
	// sqlOnlyGrid hides a grid's SQL-only copies on the ES tabs.
	sqlOnlyGrid = map[Action][]Kind{CopyDDL: sql, CopyAs("sql"): sql}
)

// kinds marks, per context, the actions that belong to some kinds only.
var kinds = map[Context]map[Action][]Kind{
	Global: {
		OpenStructure:      sql,
		OpenFullStructure:  sql,
		CopyDDL:            sql,
		ToggleTx:           sql,
		CopyAs("sql"):      sql,
		ExportAs("sql"):    sql,
		ExportAs("jsonl"):  kafka,
		ExportAs("ndjson"): es,
		OpenKey:            redisES,
		Favorites:          redis,
	},
	Tree: {
		TruncateTable:  sql,
		MarkCompare:    sql,
		Compare:        sql,
		ImportData:     sql,
		YankQualified:  sql,
		CopyDDL:        sql,
		RenameTable:    sqlRedis,
		NewKey:         redisKafkaES,
		ToggleFavorite: redis,
		BigKeys:        redis,
		FilterPrompt:   redisKafkaES,
		FilterClear:    redisKafkaES,
		ToggleInternal: kafkaES,
		SortGroups:     kafkaES,
	},
	ESDocs:            withKinds(sqlOnlyGrid, map[Action][]Kind{SimulatePipeline: es}),
	ESIndex:           sqlOnlyGrid,
	ESSettings:        sqlOnlyGrid,
	ESServer:          withKinds(sqlOnlyGrid, map[Action][]Kind{InsertRow: es, EditCell: es, ToggleEnabled: es, ChangePassword: es}),
	ESSnapshot:        withKinds(sqlOnlyGrid, map[Action][]Kind{RestoreSnapshot: es}),
	ESResults:         sqlOnlyGrid,
	ESClusterSettings: sqlOnlyGrid,
	ESObject:        sqlOnlyGrid,
	Grid:              {CopyDDL: sql, CopyAs("sql"): sql},
	Results:           {CopyDDL: sql, CopyAs("sql"): sql},
	Structure:         {CopyDDL: sql},
}

// kindDescs replaces the description of a binding shared by several
// kinds, which otherwise reads for all of them at once.
var kindDescs = map[Context]map[Action][4]string{ // [SQL, Redis, Kafka, ES]
	Global: {
		History:     {"SQL history", "Command history", "SQL and Redis command history", "Request history"},
		OpenConsole: {"Open console", "Open console", "Producer console for the topic", "REST console"},
		OpenKey:     {"", "", "", "Open a document by _id"},
	},
	Tree: {
		Open:           {"Open: table data, a column or index in the structure, a foreign key's table", "Open the key", "Open the topic / group", "Open the index / alias / data stream"},
		EditNode:       {"Edit connection / modify table", "Edit connection", "Edit connection", "Edit connection"},
		DeleteNode:     {"Delete connection / drop table or view", "Delete connection / delete Redis keys", "Delete connection / delete topic / delete group / soft-delete schema version", "Delete connection / index / template / ILM policy / pipeline / snapshot / repository"},
		RenameTable:    {"Rename table or view", "Rename Redis key", "", ""},
		NewKey:         {"", "New Redis key", "New topic", "New index · new snapshot (on a repository) · new repository (on Snapshots)"},
		FilterPrompt:   {"", "Filter Redis keys (pattern)", "Filter topics / groups / subjects by name", "Filter indices by name (* wildcard)"},
		FilterClear:    {"", "Clear the Redis key filter", "Clear the topic / group / subject filter", "Clear the index filter"},
		ToggleInternal: {"", "", "", "Show / hide system indices, templates, ILM policies and pipelines"},
		SortGroups:     {"", "", "", "Sort indices: name / docs / size"},
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

// withKinds is base with more actions marked.
func withKinds(base, more map[Action][]Kind) map[Action][]Kind {
	out := maps.Clone(base)
	maps.Copy(out, more)
	return out
}
