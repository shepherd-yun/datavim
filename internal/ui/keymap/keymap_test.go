package keymap

import (
	"slices"
	"strings"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	if _, err := New("space", DefaultBindings()); err != nil {
		t.Fatal(err)
	}
}

func TestResolve(t *testing.T) {
	k := Default()
	tests := []struct {
		ctx    Context
		seq    []string
		action Action
		res    Result
	}{
		{Tree, []string{"l"}, Expand, Matched},
		{Grid, []string{"l"}, Right, Matched},
		{Tree, []string{"g"}, "", Pending},
		{Tree, []string{"g", "g"}, Top, Matched},
		{Tree, []string{"space"}, "", Pending},
		{Grid, []string{"space", "f"}, Recent, Matched},
		{Tree, []string{"space", "space"}, GlobalSearch, Matched},
		{Console, []string{"space", "space"}, GlobalSearch, Matched},
		{Tree, []string{"space", "s"}, OpenStructure, Matched},
		{Grid, []string{"space", "e"}, ToggleTree, Matched},
		{Tree, []string{"3"}, GotoTab(3), Matched},
		{Tree, []string{"z"}, "", Pending},
		{Tree, []string{"z", "M"}, FoldCloseTree, Matched},
		{Tree, []string{"Z"}, FoldCloseTree, Matched},
		{Tree, []string{"z", "o"}, FoldOpen, Matched},
		{Tree, []string{"enter"}, Open, Matched},
		{Tree, []string{"right"}, Expand, Matched},
		{Tree, []string{"space", "z"}, "", NoMatch},
	}
	for _, tt := range tests {
		a, r := k.Resolve(tt.ctx, tt.seq)
		if a != tt.action || r != tt.res {
			t.Errorf("Resolve(%s, %v) = %q, %v; want %q, %v", tt.ctx, tt.seq, a, r, tt.action, tt.res)
		}
	}
}

func TestNewRejectsPrefixConflicts(t *testing.T) {
	_, err := New("space", []Binding{
		{Context: Global, Keys: []string{"g"}, Action: Top},
		{Context: Tree, Keys: []string{"g", "g"}, Action: Top},
	})
	if err == nil || !strings.Contains(err.Error(), "prefix") {
		t.Fatalf("err = %v, want prefix conflict", err)
	}
	_, err = New("space", []Binding{
		{Context: Tree, Keys: []string{"x"}, Action: Top},
		{Context: Tree, Keys: []string{"x"}, Action: Bottom},
	})
	if err == nil {
		t.Fatal("duplicate binding in one context must be rejected")
	}
}

func TestContextOverridesGlobal(t *testing.T) {
	k, err := New("space", []Binding{
		{Context: Global, Keys: []string{"x"}, Action: Quit},
		{Context: Tree, Keys: []string{"x"}, Action: Expand},
	})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := k.Resolve(Tree, []string{"x"}); a != Expand {
		t.Errorf("tree x = %q", a)
	}
	if a, _ := k.Resolve(Grid, []string{"x"}); a != Quit {
		t.Errorf("grid x = %q", a)
	}
}

func TestContinuations(t *testing.T) {
	hints := Default().Continuations(Tree, KindAny, []string{"space"})
	var found bool
	for _, h := range hints {
		if h.Keys == "s" && strings.Contains(h.Desc, "structure") {
			found = true
		}
	}
	if !found {
		t.Fatalf("hints = %+v", hints)
	}
}

func TestHelpMergesAliases(t *testing.T) {
	var line string
	for _, e := range Default().Help(Tree, KindAny) {
		if e.Desc == "Move down" {
			line = e.Keys
		}
	}
	if line != "j, <down>" {
		t.Fatalf("keys for Move down = %q", line)
	}
}

func TestDisplay(t *testing.T) {
	if got := Default().Display([]string{"space", "s"}); got != "<space>s" {
		t.Errorf("got %q", got)
	}
	if got := Default().Display([]string{"g", "g"}); got != "gg" {
		t.Errorf("got %q", got)
	}
}

func TestTabNumber(t *testing.T) {
	if n, ok := TabNumber(GotoTab(7)); !ok || n != 7 {
		t.Fatalf("got %d, %v", n, ok)
	}
	if _, ok := TabNumber(Quit); ok {
		t.Fatal("Quit is not a tab action")
	}
}

func TestV02Bindings(t *testing.T) {
	k := Default()
	tests := []struct {
		ctx    Context
		seq    []string
		action Action
	}{
		{Tree, []string{"ctrl+c"}, Cancel},
		{Console, []string{"enter"}, RunStatement},
		{Console, []string{"R"}, RunAll},
		{Console, []string{"ctrl+j"}, FocusResults},
		{Console, []string{"space", "v"}, ExternalEdit},
		{Results, []string{"ctrl+k"}, FocusEditor},
		{Results, []string{"}"}, LoadMore},
		{Results, []string{"]"}, ResultView},
		{Results, []string{"["}, ResultView},
		{Results, []string{"H"}, PrevResult},
		{Results, []string{"L"}, NextResult},
		{RedisResults, []string{"L"}, NextResult},
		{RedisResults, []string{"["}, ResultView},
		{Console, []string{"L"}, NextTab},
		{Results, []string{"y", "y"}, YankRow},
		{Grid, []string{"y", "l"}, YankCell},
		{Grid, []string{"z", "L"}, ScrollRight},
		{Results, []string{"z", "H"}, ScrollLeft},
		{Grid, []string{"z", "h"}, ScrollHalfLeft},
		{Results, []string{"z", "l"}, ScrollHalfRight},
		{Grid, []string{"/"}, Search},
		{Tree, []string{"N"}, SearchPrev},
		{Grid, []string{"space", "y", "c"}, CopyAs("csv")},
		{Results, []string{"space", "x", "x"}, ExportAs("xlsx")},
		{Tree, []string{"space", "c"}, OpenConsole},
		{Tree, []string{"space", "n"}, NewConnection},
		{Tree, []string{"space", "h"}, History},
	}
	for _, tt := range tests {
		if a, r := k.Resolve(tt.ctx, tt.seq); a != tt.action || r != Matched {
			t.Errorf("Resolve(%s, %v) = %q, %v; want %q", tt.ctx, tt.seq, a, r, tt.action)
		}
	}
	for _, key := range []string{"i", "j", "h", "0", "$"} {
		if _, r := k.Resolve(Console, []string{key}); r != NoMatch {
			t.Errorf("console %s belongs to the vim editor, keymap gives %v", key, r)
		}
	}
	if _, r := k.Resolve(Console, []string{"space", "y"}); r != Pending {
		t.Error("<space>y waits for the format key")
	}
	if a, _ := k.Resolve(Tree, []string{"ctrl+c"}); a == Quit {
		t.Error("ctrl+c must not quit")
	}
}

func TestCopyAndExportActions(t *testing.T) {
	if f, ok := CopyFormat(CopyAs("md")); !ok || f != "md" {
		t.Fatalf("CopyFormat = %q, %v", f, ok)
	}
	if f, ok := ExportFormat(ExportAs("sql")); !ok || f != "sql" {
		t.Fatalf("ExportFormat = %q, %v", f, ok)
	}
	if _, ok := CopyFormat(ExportAs("csv")); ok {
		t.Fatal("an export action is not a copy action")
	}
	if _, ok := ExportFormat(Quit); ok {
		t.Fatal("Quit is not an export action")
	}
}

func TestToggleTxBinding(t *testing.T) {
	k := Default()
	for _, ctx := range []Context{Console, Results, Tree, Grid} {
		if a, res := k.Resolve(ctx, []string{"space", "t"}); a != ToggleTx || res != Matched {
			t.Errorf("%s: got %q %v", ctx, a, res)
		}
	}
}

func TestConnectionEditBindings(t *testing.T) {
	k := Default()
	if a, res := k.Resolve(Tree, []string{"e"}); a != EditNode || res != Matched {
		t.Errorf("e: %q %v", a, res)
	}
	if a, res := k.Resolve(Tree, []string{"d", "d"}); a != DeleteNode || res != Matched {
		t.Errorf("dd: %q %v", a, res)
	}
	for key, want := range map[string]Action{"a": NodeActions, "r": RenameTable, "T": TruncateTable} {
		if a, res := k.Resolve(Tree, []string{key}); a != want || res != Matched {
			t.Errorf("%s = %v %v, want %v", key, a, res, want)
		}
	}
}

func TestFilterAndSortBindings(t *testing.T) {
	k := Default()
	for keys, want := range map[string]Action{
		"f f": FilterEqual, "f !": FilterNotEqual, "f s": FilterContains, "f ~": FilterContains,
		"f >": FilterGreater, "f <": FilterLess, "f n": FilterNull, "f N": FilterNotNull,
		"f x": FilterClear, "f l": FilterLike, "F": FilterPrompt, "s": SortColumn,
	} {
		seq := strings.Fields(keys)
		if a, res := k.Resolve(Grid, seq); a != want || res != Matched {
			t.Errorf("grid %s: %q %v", keys, a, res)
		}
		a, res := k.Resolve(Results, seq)
		switch {
		case want == SortColumn && res == Matched && a == want:
			t.Errorf("console results do not sort: %s", keys)
		case want != SortColumn && (res != Matched || a != want):
			t.Errorf("results %s: %q %v", keys, a, res)
		}
	}
	if _, res := k.Resolve(Grid, []string{"f"}); res != Pending {
		t.Errorf("f is a prefix: %v", res)
	}
}

func TestEditBindings(t *testing.T) {
	k := Default()
	for keys, want := range map[string]Action{
		"i": EditCell, "enter": EditCell, "o": InsertRow, "d d": DeleteRow,
		"u": UndoEdit, "U": UndoAllEdits, "space w": SubmitEdits,
	} {
		seq := strings.Fields(keys)
		if a, res := k.Resolve(Grid, seq); a != want || res != Matched {
			t.Errorf("grid %s: %q %v", keys, a, res)
		}
		if a, res := k.Resolve(Results, seq); a != want || res != Matched {
			t.Errorf("results %s: %q %v", keys, a, res)
		}
	}
}

func TestDDLAndStructureBindings(t *testing.T) {
	k := Default()
	for _, ctx := range []Context{Tree, Grid, Structure} {
		if a, res := k.Resolve(ctx, []string{"space", "y", "d"}); res != Matched || a != CopyDDL {
			t.Errorf("%s: <space>yd = %v %v, want ddl.copy", ctx, a, res)
		}
		if a, res := k.Resolve(ctx, []string{"space", "S"}); res != Matched || a != OpenFullStructure {
			t.Errorf("%s: <space>S = %v %v, want structure.full", ctx, a, res)
		}
	}
	if _, ok := CopyFormat(CopyDDL); ok {
		t.Fatal("copying the DDL is not a result format")
	}
}

func TestYCopyBindings(t *testing.T) {
	k := Default()
	for _, ctx := range []Context{Tree, Grid, Results, Structure} {
		if a, res := k.Resolve(ctx, []string{"y", "d"}); res != Matched || a != CopyDDL {
			t.Errorf("%s: yd = %v %v, want ddl.copy", ctx, a, res)
		}
	}
	for _, ctx := range []Context{Grid, Results} {
		for key, want := range map[string]Action{
			"t": CopyAs("tsv"), "c": CopyAs("csv"), "j": CopyAs("json"),
			"m": CopyAs("md"), "i": CopyAs("sql"),
		} {
			if a, res := k.Resolve(ctx, []string{"y", key}); res != Matched || a != want {
				t.Errorf("%s: y%s = %v %v, want %s", ctx, key, a, res, want)
			}
		}
	}
}

func TestPopupKeys(t *testing.T) {
	k := Default()
	if !k.Triggers(Popup, OpenFullStructure, "ctrl+o") {
		t.Fatal("ctrl+o opens the structure from a popup")
	}
	if k.Triggers(Popup, OpenFullStructure, "ctrl+s") {
		t.Fatal("ctrl+s no longer opens the structure (it is a common tmux prefix)")
	}
	if !k.Triggers(Popup, SaveForm, "ctrl+s") {
		t.Fatal("ctrl+s saves a form")
	}
	if k.Triggers(Popup, OpenFullStructure, "S") {
		t.Fatal("only the popup's own one-key bindings count, not <space>S")
	}
	if got := k.OwnKey(Popup, OpenFullStructure); got != "<ctrl+o>" {
		t.Fatalf("OwnKey = %q", got)
	}
	if got := k.OwnKey(Popup, CopyDDL); got != "" {
		t.Fatalf("OwnKey ignores globals: %q", got)
	}
}

func TestCompareBindings(t *testing.T) {
	k := Default()
	for _, c := range []struct {
		ctx  Context
		keys []string
		want Action
	}{
		{Tree, []string{"m"}, MarkCompare},
		{Tree, []string{"C"}, Compare},
		{Diff, []string{"x"}, ToggleChange},
		{Diff, []string{"~"}, SwapSides},
		{Diff, []string{"]", "c"}, NextChange},
		{Diff, []string{"[", "c"}, PrevChange},
		{Diff, []string{"l"}, Expand},
		{Diff, []string{"enter"}, Expand},
		{Diff, []string{"h"}, Collapse},
		{Diff, []string{"j"}, Down},
		{Diff, []string{"g", "g"}, Top},
		{Diff, []string{"space", "w"}, WriteScript},
		{Diff, []string{"space", "r"}, Refresh},
	} {
		if a, res := k.Resolve(c.ctx, c.keys); a != c.want || res != Matched {
			t.Errorf("%s %v = %q %v, want %q", c.ctx, c.keys, a, res, c.want)
		}
	}
	if keys := k.KeysFor(Diff, WriteScript); keys == "" {
		t.Error("WriteScript has no keys in Diff")
	}
}

func TestStructureEditBindings(t *testing.T) {
	k := Default()
	for keys, want := range map[string]Action{
		"e": ModifyTable, "i": EditCell, "enter": EditCell, "o": InsertRow, "u": UndoEdit, "U": UndoAllEdits,
		"h": Left, "l": Right, "[": PrevSection, "]": NextSection,
	} {
		if a, res := k.Resolve(Structure, []string{keys}); a != want || res != Matched {
			t.Errorf("%s = %v %v, want %v", keys, a, res, want)
		}
	}
	if a, res := k.Resolve(Structure, []string{"d", "d"}); a != DeleteRow || res != Matched {
		t.Errorf("dd = %v %v", a, res)
	}
	if a, res := k.Resolve(Structure, []string{"space", "w"}); a != SubmitEdits || res != Matched {
		t.Errorf("<space>w = %v %v", a, res)
	}
}

func TestDataDiffBindings(t *testing.T) {
	k := Default()
	for _, c := range []struct {
		keys []string
		want Action
	}{
		{[]string{"D"}, DataDiff},
		{[]string{"F"}, FilterPrompt},
	} {
		if a, res := k.Resolve(Diff, c.keys); a != c.want || res != Matched {
			t.Errorf("diff %v = %q %v, want %q", c.keys, a, res, c.want)
		}
	}
	if KnownActions()[DataDiff] == "" {
		t.Error("diff.data cannot be bound in keymap.toml")
	}
}

func TestResultsShareFilterAndEditKeys(t *testing.T) {
	k := Default()
	for _, tc := range []struct {
		keys []string
		want Action
	}{
		{[]string{"f", "f"}, FilterEqual},
		{[]string{"F"}, FilterPrompt},
		{[]string{"i"}, EditCell},
		{[]string{"o"}, InsertRow},
		{[]string{"d", "d"}, DeleteRow},
		{[]string{"U"}, UndoAllEdits},
	} {
		for _, ctx := range []Context{Grid, Results} {
			if a, r := k.Resolve(ctx, tc.keys); r != Matched || a != tc.want {
				t.Errorf("%s %v = %v %v, want %v", ctx, tc.keys, a, r, tc.want)
			}
		}
	}
	if a, r := k.Resolve(Results, []string{"s"}); r == Matched && a == SortColumn {
		t.Error("results do not sort")
	}
	if a, _ := k.Resolve(Results, []string{"}"}); a != LoadMore {
		t.Errorf("} in results = %v", a)
	}
}

func TestImportBindings(t *testing.T) {
	k := Default()
	tests := []struct {
		ctx    Context
		seq    []string
		action Action
	}{
		{Tree, []string{"I"}, ImportData},
		{Import, []string{"j"}, Down},
		{Import, []string{"]"}, NextSection},
		{Import, []string{"["}, PrevSection},
		{Import, []string{"i"}, EditCell},
		{Import, []string{"enter"}, EditCell},
		{Import, []string{"x"}, ImportToggle},
		{Import, []string{"a"}, ImportAll},
		{Import, []string{"A"}, ImportNone},
		{Import, []string{"t"}, ImportType},
		{Import, []string{"space", "w"}, SubmitEdits},
		{Import, []string{"space", "r"}, Refresh},
	}
	for _, tt := range tests {
		if a, r := k.Resolve(tt.ctx, tt.seq); a != tt.action || r != Matched {
			t.Errorf("Resolve(%s, %v) = %q, %v; want %q", tt.ctx, tt.seq, a, r, tt.action)
		}
	}
	if !slices.Contains(Contexts(), Import) {
		t.Fatalf("Contexts() = %v", Contexts())
	}
}

func TestKindFiltersHelpAndContinuations(t *testing.T) {
	k := Default()
	has := func(entries []HelpEntry, desc string) bool {
		for _, e := range entries {
			if e.Desc == desc {
				return true
			}
		}
		return false
	}
	tests := []struct {
		kind      Kind
		want, not []string
	}{
		{KindAny, []string{"Truncate table", "New Redis key", "Copy DDL", "Open a Redis key by name"}, nil},
		{
			KindSQL,
			[]string{"Truncate table", "Copy DDL", "SQL history", "Delete connection / drop table or view"},
			[]string{"New Redis key", "Open a Redis key by name", "Filter Redis keys (pattern)", "Command history"},
		},
		{
			KindRedis,
			[]string{"New Redis key", "Open a Redis key by name", "Command history", "Delete connection / delete Redis keys", "Open the key"},
			[]string{"Truncate table", "Copy DDL", "Copy qualified name (db.table)", "Copy as INSERT", "SQL history"},
		},
	}
	for _, tt := range tests {
		help := k.Help(Tree, tt.kind)
		for _, d := range tt.want {
			if !has(help, d) {
				t.Errorf("kind %d: help lacks %q", tt.kind, d)
			}
		}
		for _, d := range tt.not {
			if has(help, d) {
				t.Errorf("kind %d: help lists %q", tt.kind, d)
			}
		}
	}

	leader := func(kind Kind) map[string]string {
		out := map[string]string{}
		for _, h := range k.Continuations(Tree, kind, []string{"space"}) {
			out[h.Keys] = h.Desc
		}
		return out
	}
	if l := leader(KindRedis); l["s"] != "" || l["t"] != "" || l["k"] == "" || l["i"] == "" {
		t.Errorf("redis leader = %v", l)
	}
	if l := leader(KindSQL); l["k"] != "" || l["F"] != "" || l["s"] == "" || l["t"] == "" {
		t.Errorf("sql leader = %v", l)
	}
	// Bindings stay live on either kind: only the listing changes.
	if a, _ := k.Resolve(Tree, []string{"T"}); a != TruncateTable {
		t.Errorf("T = %q", a)
	}
}

func TestKindTablesNameBoundActions(t *testing.T) {
	bound := map[Context]map[Action]bool{}
	for _, b := range DefaultBindings() {
		if bound[b.Context] == nil {
			bound[b.Context] = map[Action]bool{}
		}
		bound[b.Context][b.Action] = true
	}
	for ctx, m := range kinds {
		for a := range m {
			if !bound[ctx][a] {
				t.Errorf("kinds: %s is not bound in %s", a, ctx)
			}
		}
	}
	for ctx, m := range kindDescs {
		for a := range m {
			if !bound[ctx][a] {
				t.Errorf("kindDescs: %s is not bound in %s", a, ctx)
			}
		}
	}
}

func TestDBServerBindings(t *testing.T) {
	k := Default()
	for keys, want := range map[string]Action{
		"c": CancelSessionQuery, "a": ToggleIdle, "d d": KillClient, "[": PrevSection, "]": NextSection,
		"F": FilterPrompt, "f x": FilterClear, "j": Down, "K": CellDetail,
	} {
		if a, r := k.Resolve(DBServer, strings.Fields(keys)); a != want || r != Matched {
			t.Errorf("%s = %q (%v), want %q", keys, a, r, want)
		}
	}
	if !slices.Contains(Contexts(), DBServer) {
		t.Fatalf("Contexts() = %v", Contexts())
	}
}

func TestServerInfoBothKinds(t *testing.T) {
	k := Default()
	for _, kind := range []Kind{KindSQL, KindRedis} {
		if l := k.Continuations(Tree, kind, []string{"space"}); !slices.ContainsFunc(l, func(h Hint) bool {
			return h.Keys == "i" && strings.HasPrefix(h.Desc, "Server status")
		}) {
			t.Errorf("kind %d: leader lacks i Server status: %v", kind, l)
		}
	}
}

func TestKafkaBindings(t *testing.T) {
	k := Default()
	for _, tc := range []struct {
		ctx  Context
		keys string
		want Action
	}{
		{KafkaGroup, "g d", OpenCellKey},
		{KafkaGroup, "R", ResetOffsets},
		{KafkaGroup, "]", NextSection},
		{KafkaGroup, "K", CellDetail},
		{KafkaMessages, "]", NextSection},
		{KafkaMessages, "[", PrevSection},
		{KafkaMessages, "K", CellDetail},
		{KafkaMessages, "v", ValueFormat},
		{KafkaMessages, "t", Seek},
		{KafkaMessages, "T", LiveTail},
		{KafkaMessages, "c", ToggleHeaders},
		{KafkaMessages, "F", FilterPrompt},
		{KafkaMessages, "f f", FilterEqual},
		{KafkaMessages, "f !", FilterNotEqual},
		{KafkaMessages, "f s", FilterContains},
		{KafkaMessages, "f x", FilterClear},
		{KafkaMessages, "g d", OpenCellKey},
		{KafkaMessages, "y y", YankRow},
		{KafkaMessages, "space v", ExternalEdit},
		{KafkaMessages, "L", NextTab}, // tab keys stay global
		{KafkaTopic, "enter", Open},
		{KafkaTopic, "]", NextSection},
		{KafkaServer, "]", NextSection},
		{KafkaServer, "enter", Open},
		{KafkaProduceResults, "ctrl+k", FocusEditor},
		{KafkaProduceResults, "M", ToggleMessages},
		{KafkaProduceResults, "j", Down},
		{Tree, ".", ToggleInternal},
		{Tree, "s", SortGroups},
	} {
		if a, r := k.Resolve(tc.ctx, strings.Fields(tc.keys)); a != tc.want || r != Matched {
			t.Errorf("%s %q = %q (%v), want %q", tc.ctx, tc.keys, a, r, tc.want)
		}
	}
	descOf := func(ctx Context, kind Kind, a Action) string {
		for _, b := range k.bindings {
			if b.Context == ctx && b.Action == a {
				return b.descFor(kind)
			}
		}
		return ""
	}
	if d := descOf(Tree, KindKafka, DeleteNode); d != "Delete connection / delete topic / delete group / soft-delete schema version" {
		t.Errorf("DeleteNode on kafka = %q", d)
	}
	if d := descOf(Tree, KindKafka, NewKey); d != "New topic" {
		t.Errorf("NewKey on kafka = %q", d)
	}
	if d := descOf(Tree, KindKafka, Open); d != "Open the topic / group" {
		t.Errorf("Open on kafka = %q", d)
	}
	has := func(entries []HelpEntry, a string) bool {
		return slices.ContainsFunc(entries, func(e HelpEntry) bool { return strings.Contains(e.Desc, a) })
	}
	if has(k.Help(Tree, KindSQL), "internal topics") || has(k.Help(Tree, KindRedis), "Sort consumer groups") {
		t.Error("kafka tree keys listed on SQL / Redis")
	}
	if !has(k.Help(Tree, KindKafka), "internal topics") || !has(k.Help(Tree, KindKafka), "New topic") ||
		!has(k.Help(Tree, KindKafka), "Filter topics") {
		t.Errorf("kafka tree help = %+v", k.Help(Tree, KindKafka))
	}
	if has(k.Help(Tree, KindKafka), "Truncate table") || has(k.Help(Tree, KindKafka), "Biggest Redis keys") {
		t.Error("SQL / Redis keys listed on kafka")
	}
	for _, ctx := range []Context{KafkaTopic, KafkaMessages, KafkaGroup, KafkaProduceResults, KafkaServer} {
		if !slices.Contains(Contexts(), ctx) {
			t.Errorf("Contexts() lacks %s", ctx)
		}
	}
}

func TestKafkaKeymapK2(t *testing.T) {
	k := Default()
	for _, tc := range []struct {
		ctx  Context
		keys string
		want Action
	}{
		{KafkaConfig, "i", EditCell},
		{KafkaConfig, "enter", EditCell},
		{KafkaConfig, "d d", DeleteRow},
		{KafkaConfig, "u", UndoEdit},
		{KafkaConfig, "U", UndoAllEdits},
		{KafkaConfig, "space w", SubmitEdits},
		{KafkaConfig, "]", NextSection},
		{KafkaConfig, "[", PrevSection},
		{KafkaConfig, "j", Down},
		{KafkaTopic, "o", AddPartitions},
		{KafkaTopic, "X", DeleteRecords},
		{KafkaMessages, "X", DeleteRecords},
		{KafkaMessages, "p", ResendMessage},
		{KafkaMessages, "P", ResendShown},
		{KafkaMessages, "I", ToggleIsolation},
		{KafkaGroup, "X", DeleteOffsets},
		{KafkaSchema, "enter", Open},
		{KafkaSchema, "m", MarkCompare},
		{KafkaSchema, "C", Compare},
		{KafkaSchema, "space v", ExternalEdit},
		{KafkaSchema, "c", SetCompatibility},
		{KafkaSchema, "d d", DeleteNode},
		{KafkaSchema, "]", NextSection},
		{KafkaSchema, "/", Search},
		{KafkaSchema, "space r", Refresh},
	} {
		if a, r := k.Resolve(tc.ctx, strings.Fields(tc.keys)); a != tc.want || r != Matched {
			t.Errorf("%s %q = %q (%v), want %q", tc.ctx, tc.keys, a, r, tc.want)
		}
	}
	for _, ctx := range []Context{KafkaConfig, KafkaSchema} {
		if !slices.Contains(Contexts(), ctx) {
			t.Errorf("Contexts() lacks %s", ctx)
		}
	}
	has := func(entries []HelpEntry, a string) bool {
		return slices.ContainsFunc(entries, func(e HelpEntry) bool { return strings.Contains(e.Desc, a) })
	}
	if !has(k.Help(Tree, KindKafka), "delete group") {
		t.Errorf("tree help on kafka lacks deleting groups: %+v", k.Help(Tree, KindKafka))
	}
	if has(k.Help(Tree, KindKafka), "Compare with the marked node") {
		t.Error("the tree's compare is listed on kafka")
	}
}
