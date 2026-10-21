// Package keymap maps key sequences to actions per UI context.
package keymap

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Context names the part of the UI that has focus.
type Context string

const (
	Global    Context = "global"
	Tree      Context = "tree"
	Grid      Context = "grid"
	Structure Context = "structure"
	Console   Context = "console"
	Results   Context = "results"
	Diff      Context = "diff"
	Import    Context = "import"
	RedisKey  Context = "redis_key"
	// RedisResults is the result pane of a redis console.
	RedisResults Context = "redis_results"
	// RedisServer is a redis connection's server tab (status, slow log, clients).
	RedisServer Context = "redis_server"
	// DBServer is a SQL connection's server tab (status, sessions, locks, …).
	DBServer Context = "db_server"
	// RedisBigKeys is a big-key analysis tab.
	RedisBigKeys Context = "redis_bigkeys"
	// KafkaTopic is a Kafka topic tab's Partitions, Config and Consumers.
	KafkaTopic Context = "kafka_topic"
	// KafkaMessages is a Kafka topic tab's Messages.
	KafkaMessages Context = "kafka_messages"
	// KafkaGroup is a consumer group tab.
	KafkaGroup Context = "kafka_group"
	// KafkaProduceResults is the result pane of a producer console.
	KafkaProduceResults Context = "kafka_produce_results"
	// KafkaServer is a Kafka connection's cluster status tab.
	KafkaServer Context = "kafka_server"
	// KafkaConfig is a Kafka topic tab's Config section, where configs are edited.
	KafkaConfig Context = "kafka_config"
	// KafkaSchema is a Schema Registry subject's tab.
	KafkaSchema Context = "kafka_schema"
	// KafkaConnect is a Kafka Connect connector's tab.
	KafkaConnect Context = "kafka_connect"
	// KafkaACL is a Kafka connection's ACL tab.
	KafkaACL Context = "kafka_acl"
	// ESDocs is an Elasticsearch index tab's Documents.
	ESDocs Context = "es_docs"
	// ESIndex is an Elasticsearch index tab's Mapping, Aliases and Stats.
	ESIndex Context = "es_index"
	// ESSettings is an Elasticsearch index tab's Settings, where they are edited.
	ESSettings Context = "es_settings"
	// ESServer is an Elasticsearch connection's cluster status tab.
	ESServer Context = "es_server"
	// ESResults is the result pane of an Elasticsearch REST console.
	ESResults Context = "es_results"
	// Audit is the audit log of writes.
	Audit Context = "audit"
	// Popup holds the keys popups read themselves (they bypass the keymap):
	// only one-key bindings count there.
	Popup Context = "popup"
)

// Action is what a key sequence does.
type Action string

const (
	Quit               Action = "app.quit"
	CommandLine        Action = "app.cmdline"
	Help               Action = "app.help"
	FocusNext          Action = "focus.next"
	FocusPrev          Action = "focus.prev"
	FocusLeft          Action = "focus.left"
	FocusRight         Action = "focus.right"
	ToggleTree         Action = "tree.toggle"
	FullScreen         Action = "screen.full"
	ExitFullScreen     Action = "screen.normal"
	OpenStructure      Action = "structure.open"
	OpenFullStructure  Action = "structure.full"
	CopyDDL            Action = "ddl.copy"
	SaveForm           Action = "form.save"
	NextSearchScope    Action = "search.scope.next"
	PrevSearchScope    Action = "search.scope.prev"
	Refresh            Action = "refresh"
	Down               Action = "move.down"
	Up                 Action = "move.up"
	Left               Action = "move.left"
	Right              Action = "move.right"
	Top                Action = "move.top"
	Bottom             Action = "move.bottom"
	HalfPageDown       Action = "move.halfpage.down"
	HalfPageUp         Action = "move.halfpage.up"
	LineStart          Action = "move.linestart"
	LineEnd            Action = "move.lineend"
	Expand             Action = "tree.expand"
	Collapse           Action = "tree.collapse"
	Open               Action = "tree.open"
	FoldOpen           Action = "tree.fold.open"
	FoldClose          Action = "tree.fold.close"
	FoldToggle         Action = "tree.fold.toggle"
	FoldOpenAll        Action = "tree.fold.open_all"
	FoldCloseAll       Action = "tree.fold.close_all"
	FoldOpenTree       Action = "tree.fold.open_tree"
	FoldCloseTree      Action = "tree.fold.close_tree"
	FoldFocus          Action = "tree.fold.focus"
	Password           Action = "conn.password"
	PrevPage           Action = "page.prev"
	NextPage           Action = "page.next"
	CellDetail         Action = "cell.detail"
	PrevSection        Action = "section.prev"
	NextSection        Action = "section.next"
	Cancel             Action = "query.cancel"
	OpenConsole        Action = "console.open"
	NewConnection      Action = "conn.new"
	EditNode           Action = "node.edit"
	DeleteNode         Action = "node.delete"
	NodeActions        Action = "node.actions"
	RenameTable        Action = "table.rename"
	SetTTL             Action = "redis.ttl"
	NewKey             Action = "redis.newkey"
	TruncateTable      Action = "table.truncate"
	ModifyTable        Action = "table.modify"
	History            Action = "history.open"
	ExternalEdit       Action = "editor.external"
	RunStatement       Action = "sql.run"
	RunAll             Action = "sql.runall"
	RunFromCursor      Action = "sql.runfrom"
	FocusResults       Action = "console.results"
	FocusEditor        Action = "console.editor"
	ToggleMessages     Action = "console.messages"
	LoadMore           Action = "results.more"
	PrevResult         Action = "results.prev"
	NextResult         Action = "results.next"
	ResultView         Action = "results.view"
	Search             Action = "search.start"
	SearchNext         Action = "search.next"
	SearchPrev         Action = "search.prev"
	ScrollLeft         Action = "scroll.left"
	ScrollRight        Action = "scroll.right"
	ScrollHalfLeft     Action = "scroll.halfleft"
	ScrollHalfRight    Action = "scroll.halfright"
	YankCell           Action = "yank.cell"
	YankRow            Action = "yank.row"
	YankName           Action = "yank.name"
	YankQualified      Action = "yank.qualified"
	ToggleTx           Action = "tx.toggle"
	GlobalSearch       Action = "search.global"
	Recent             Action = "recent.open"
	Locate             Action = "tree.locate"
	OpenKey            Action = "redis.openkey"
	ValueFormat        Action = "redis.format"
	OpenCellKey        Action = "redis.open_cell_key"
	ServerInfo         Action = "redis.server"
	KillClient         Action = "redis.killclient"
	CancelSessionQuery Action = "server.cancelquery"
	ToggleIdle         Action = "server.toggleidle"
	ToggleFavorite     Action = "redis.favorite"
	Favorites          Action = "redis.favorites"
	AuditLog           Action = "audit"
	PauseResume        Action = "kafka.connect_pause"
	RestartConnector   Action = "kafka.connect_restart"
	BigKeys            Action = "redis.bigkeys"
	FilterEqual        Action = "filter.eq"
	FilterNotEqual     Action = "filter.ne"
	FilterContains     Action = "filter.contains"
	FilterLike         Action = "filter.like"
	FilterGreater      Action = "filter.gt"
	FilterLess         Action = "filter.lt"
	FilterNull         Action = "filter.null"
	FilterNotNull      Action = "filter.notnull"
	FilterClear        Action = "filter.clear"
	FilterPrompt       Action = "filter.prompt"
	SortColumn         Action = "sort.column"
	EditCell           Action = "edit.cell"
	InsertRow          Action = "edit.insert"
	DeleteRow          Action = "edit.delete"
	UndoEdit           Action = "edit.undo"
	UndoAllEdits       Action = "edit.undoall"
	SubmitEdits        Action = "edit.submit"
	NextTab            Action = "tab.next"
	PrevTab            Action = "tab.prev"
	MarkCompare        Action = "compare.mark"
	Compare            Action = "compare.run"
	ToggleChange       Action = "diff.toggle"
	SwapSides          Action = "diff.swap"
	NextChange         Action = "diff.next"
	PrevChange         Action = "diff.prev"
	WriteScript        Action = "diff.script"
	DataDiff           Action = "diff.data"
	ImportData         Action = "import.open"
	ImportToggle       Action = "import.toggle"
	ImportAll          Action = "import.all"
	ImportNone         Action = "import.none"
	ImportType         Action = "import.type"
	Seek               Action = "kafka.seek"
	LiveTail           Action = "kafka.tail"
	ResetOffsets       Action = "kafka.reset_offsets"
	ToggleInternal     Action = "kafka.internal"
	SortGroups         Action = "kafka.sort_groups"
	ToggleHeaders      Action = "kafka.headers"
	AddPartitions      Action = "kafka.add_partitions"
	DeleteRecords      Action = "kafka.delete_records"
	DeleteOffsets      Action = "kafka.delete_offsets"
	ResendMessage      Action = "kafka.resend"
	ResendShown        Action = "kafka.resend_shown"
	ToggleIsolation    Action = "kafka.isolation"
	SetCompatibility   Action = "kafka.compat"
	DeleteByQuery      Action = "es.delete_by_query"
	YankID             Action = "es.yank_id"
	YankSource         Action = "es.yank_source"
	YankRequest        Action = "es.yank_request"
	YankCurl           Action = "es.yank_curl"
)

const (
	copyPrefix   = "copy."
	exportPrefix = "export."
)

// CopyAs is the action that copies the result in format (e.g. "csv").
func CopyAs(format string) Action { return Action(copyPrefix + format) }

// CopyFormat extracts the format from a CopyAs action.
func CopyFormat(a Action) (string, bool) { return strings.CutPrefix(string(a), copyPrefix) }

// ExportAs is the action that exports the result in format (e.g. "xlsx").
func ExportAs(format string) Action { return Action(exportPrefix + format) }

// ExportFormat extracts the format from an ExportAs action.
func ExportFormat(a Action) (string, bool) { return strings.CutPrefix(string(a), exportPrefix) }

const tabPrefix = "tab.goto."

// GotoTab is the action that focuses tab n (1-based).
func GotoTab(n int) Action { return Action(tabPrefix + strconv.Itoa(n)) }

// TabNumber extracts n from a GotoTab action.
func TabNumber(a Action) (int, bool) {
	s, ok := strings.CutPrefix(string(a), tabPrefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// LeaderToken in Binding.Keys is replaced by the configured leader key.
const LeaderToken = "<leader>"

// Binding maps a key sequence to an action within a context.
type Binding struct {
	Context Context
	Keys    []string
	Action  Action
	Desc    string
}

// Result is the outcome of resolving a key sequence.
type Result int

const (
	NoMatch Result = iota
	Pending
	Matched
)

// Hint is one which-key line: the remaining keys and what they do.
type Hint struct {
	Keys string
	Desc string
}

// HelpEntry is one help line; aliases for the same action are merged.
type HelpEntry struct {
	Context Context
	Action  Action
	Keys    string
	Desc    string
}

// Essential reports a global action help lists next to the focused
// area's own keys; the other global keys get a page of their own.
func (e HelpEntry) Essential() bool {
	if _, ok := TabNumber(e.Action); ok {
		return true
	}
	switch e.Action {
	case Help, CommandLine, Cancel, FocusNext, FocusPrev, FocusLeft, FocusRight,
		FullScreen, ExitFullScreen, NextTab, PrevTab:
		return true
	}
	return false
}

// Keymap resolves key sequences.
type Keymap struct {
	leader   string
	bindings []Binding
}

// New expands the leader token and validates bindings.
func New(leader string, bindings []Binding) (*Keymap, error) {
	k := &Keymap{leader: leader}
	for _, b := range bindings {
		keys := make([]string, len(b.Keys))
		for i, key := range b.Keys {
			if key == LeaderToken {
				key = leader
			}
			keys[i] = key
		}
		b.Keys = keys
		k.bindings = append(k.bindings, b)
	}
	if err := k.validate(); err != nil {
		return nil, err
	}
	return k, nil
}

// Default returns the built-in keymap with <space> as leader.
func Default() *Keymap {
	k, err := New("space", DefaultBindings())
	if err != nil {
		panic(err) // covered by TestDefaultIsValid
	}
	return k
}

// Leader returns the leader key.
func (k *Keymap) Leader() string { return k.leader }

func (k *Keymap) validate() error {
	contexts := map[Context]bool{Global: true}
	seen := map[string]bool{}
	for _, b := range k.bindings {
		contexts[b.Context] = true
		id := string(b.Context) + "\x00" + strings.Join(b.Keys, "\x00")
		if seen[id] {
			return fmt.Errorf("keymap: %s bound twice in context %s", k.Display(b.Keys), b.Context)
		}
		seen[id] = true
	}
	for ctx := range contexts {
		scope := k.scope(ctx)
		for _, a := range scope {
			for _, b := range scope {
				if len(a.Keys) < len(b.Keys) && hasPrefix(b.Keys, a.Keys) {
					return fmt.Errorf("keymap: %s (%s) is a prefix of %s (%s) in context %s",
						k.Display(a.Keys), a.Action, k.Display(b.Keys), b.Action, ctx)
				}
			}
		}
	}
	return nil
}

// scope returns the bindings visible in ctx: ctx first, then unshadowed globals.
func (k *Keymap) scope(ctx Context) []Binding {
	var out []Binding
	seen := map[string]bool{}
	for _, c := range []Context{ctx, Global} {
		for _, b := range k.bindings {
			if b.Context != c {
				continue
			}
			id := strings.Join(b.Keys, "\x00")
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, b)
		}
		if ctx == Global {
			break
		}
	}
	return out
}

// Resolve looks up seq in ctx.
func (k *Keymap) Resolve(ctx Context, seq []string) (Action, Result) {
	pending := false
	for _, b := range k.scope(ctx) {
		if slices.Equal(b.Keys, seq) {
			return b.Action, Matched
		}
		if hasPrefix(b.Keys, seq) {
			pending = true
		}
	}
	if pending {
		return "", Pending
	}
	return "", NoMatch
}

// Continuations lists what can follow prefix in ctx on kind, sorted by key.
func (k *Keymap) Continuations(ctx Context, kind Kind, prefix []string) []Hint {
	var out []Hint
	for _, b := range k.scope(ctx) {
		if len(b.Keys) > len(prefix) && hasPrefix(b.Keys, prefix) && b.shows(kind) {
			out = append(out, Hint{Keys: k.Display(b.Keys[len(prefix):]), Desc: b.descFor(kind)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Keys < out[j].Keys })
	return out
}

// Help lists bindings visible in ctx on kind, merging aliases of the same
// action.
func (k *Keymap) Help(ctx Context, kind Kind) []HelpEntry {
	var out []HelpEntry
	index := map[string]int{}
	for _, b := range k.scope(ctx) {
		if !b.shows(kind) {
			continue
		}
		id := string(b.Context) + "\x00" + string(b.Action)
		if i, ok := index[id]; ok {
			out[i].Keys += ", " + k.Display(b.Keys)
			continue
		}
		if n, ok := TabNumber(b.Action); ok {
			// 1 … 9 read as one line.
			id = string(b.Context) + "\x00" + tabPrefix
			if i, ok := index[id]; ok {
				out[i].Keys = strings.SplitN(out[i].Keys, "…", 2)[0] + "…" + k.Display(b.Keys)
				out[i].Desc = "Go to tab 1–" + strconv.Itoa(n)
				continue
			}
		}
		index[id] = len(out)
		out = append(out, HelpEntry{Context: b.Context, Action: b.Action, Keys: k.Display(b.Keys), Desc: b.descFor(kind)})
	}
	return out
}

// KeysFor is how the first binding of a reads in ctx ("<space>w"), or ""
// when nothing in ctx triggers it. Hints use it so they follow keymap.toml.
func (k *Keymap) KeysFor(ctx Context, a Action) string {
	if seq := k.SeqFor(ctx, a); seq != nil {
		return k.Display(seq)
	}
	return ""
}

// SeqFor is the first key sequence bound to a in ctx (or globally) as key
// events read, or nil.
func (k *Keymap) SeqFor(ctx Context, a Action) []string {
	for _, b := range k.scope(ctx) {
		if b.Action == a {
			return b.Keys
		}
	}
	return nil
}

// OwnKey is how the first one-key binding of a in ctx itself (globals
// aside) reads, or "": the hint matching Triggers.
func (k *Keymap) OwnKey(ctx Context, a Action) string {
	for _, b := range k.bindings {
		if b.Context == ctx && b.Action == a && len(b.Keys) == 1 {
			return k.Display(b.Keys)
		}
	}
	return ""
}

// Triggers reports whether key alone is bound to a in ctx itself (globals
// aside). Popups use it for the keys they handle without the keymap.
func (k *Keymap) Triggers(ctx Context, a Action, key string) bool {
	for _, b := range k.bindings {
		if b.Context == ctx && b.Action == a && len(b.Keys) == 1 && b.Keys[0] == key {
			return true
		}
	}
	return false
}

// Display renders keys vim-style: "gg", "<space>s", "<ctrl+d>".
func (k *Keymap) Display(keys []string) string {
	var b strings.Builder
	for _, key := range keys {
		if len([]rune(key)) == 1 {
			b.WriteString(key)
		} else {
			b.WriteString("<" + key + ">")
		}
	}
	return b.String()
}

func hasPrefix(keys, prefix []string) bool {
	return len(keys) >= len(prefix) && slices.Equal(keys[:len(prefix)], prefix)
}
