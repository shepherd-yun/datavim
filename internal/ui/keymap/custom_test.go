package keymap

import (
	"slices"
	"strings"
	"testing"
)

func TestCustomLeader(t *testing.T) {
	k, err := Custom(",", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a, res := k.Resolve(Global, []string{",", "c"}); res != Matched || a != OpenConsole {
		t.Fatalf(",c = %v %v, want console.open", a, res)
	}
	if _, res := k.Resolve(Global, []string{"space"}); res != NoMatch {
		t.Fatal("space is no longer the leader")
	}
	if got := k.KeysFor(Grid, SubmitEdits); got != ",w" {
		t.Fatalf("KeysFor(submit) = %q, want ,w", got)
	}
}

func TestCustomRebindsAddsAndUnbinds(t *testing.T) {
	k, err := Custom("", map[Context]map[string]string{
		Grid:   {"S": "sort.column", "s": "", "<leader>f": "search.global"},
		Global: {"<space>s": "recent.open"}, // literal leader keys replace the <leader> binding
	})
	if err != nil {
		t.Fatal(err)
	}
	if a, res := k.Resolve(Grid, []string{"S"}); res != Matched || a != SortColumn {
		t.Fatalf("S = %v %v", a, res)
	}
	if _, res := k.Resolve(Grid, []string{"s"}); res != NoMatch {
		t.Fatal(`"s" = "" unbinds it`)
	}
	if a, _ := k.Resolve(Grid, []string{"space", "f"}); a != GlobalSearch {
		t.Fatalf("<leader>f in grid = %v", a)
	}
	if a, _ := k.Resolve(Tree, []string{"space", "s"}); a != Recent {
		t.Fatalf("<space>s = %v, want recent.open", a)
	}
	if a, _ := k.Resolve(Grid, []string{"f", "f"}); a != FilterEqual {
		t.Fatal("untouched defaults stay")
	}
	for _, h := range k.Help(Grid) {
		if h.Desc == "Sort by column: asc, desc, off" && h.Keys != "S" {
			t.Fatalf("help for sort = %q, want only S", h.Keys)
		}
	}
}

func TestCustomRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name      string
		leader    string
		overrides map[Context]map[string]string
		want      string
	}{
		{"unknown action", "", map[Context]map[string]string{Grid: {"S": "sort"}}, `[grid] "S": unknown action "sort"`},
		{"unknown context", "", map[Context]map[string]string{"gird": {"S": "sort.column"}}, `[gird]: unknown context`},
		{"bad keys", "", map[Context]map[string]string{Tree: {"<ctrl+d": "move.down"}}, `[tree] "<ctrl+d": `},
		{"bad leader", "ab", nil, `leader "ab"`},
	} {
		if _, err := Custom(tc.leader, tc.overrides); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestCustomReportsConflicts(t *testing.T) {
	_, err := Custom("", map[Context]map[string]string{Grid: {"f": "sort.column"}})
	if err == nil || !strings.Contains(err.Error(), "f (sort.column) is a prefix of") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Custom("g", nil); err == nil {
		t.Fatal(`leader "g" collides with gt / gT and must be rejected`)
	}
}

func TestKnownActionsCoverDefaults(t *testing.T) {
	known := KnownActions()
	for _, b := range DefaultBindings() {
		if known[b.Action] == "" {
			t.Errorf("%s has no description", b.Action)
		}
	}
	if known[Quit] == "" {
		t.Error("app.quit can be bound although it has no default key")
	}
}

func TestCustomRebindsPopupKeys(t *testing.T) {
	k, err := Custom("", map[Context]map[string]string{
		Popup: {"<ctrl+o>": "", "<ctrl+t>": "structure.full"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if k.Triggers(Popup, OpenFullStructure, "ctrl+o") || !k.Triggers(Popup, OpenFullStructure, "ctrl+t") {
		t.Fatal("keymap.toml rebinds the popup's structure key")
	}
}

func TestCustomAcceptsRenamedAction(t *testing.T) {
	k, err := Custom("", map[Context]map[string]string{Tree: {"x": "conn.delete"}})
	if err != nil {
		t.Fatal(err)
	}
	if a, res := k.Resolve(Tree, []string{"x"}); a != DeleteNode || res != Matched {
		t.Fatalf("x = %v %v", a, res)
	}
}

func TestTruncateIsBindable(t *testing.T) {
	if KnownActions()[TruncateTable] == "" {
		t.Fatal("table.truncate cannot be bound")
	}
	k, err := Custom("", map[Context]map[string]string{Tree: {"T": "table.truncate"}})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := k.Resolve(Tree, []string{"T"}); a != TruncateTable {
		t.Fatalf("T = %v", a)
	}
}

func TestCustomDiffTable(t *testing.T) {
	if !slices.Contains(Contexts(), Diff) {
		t.Fatalf("Contexts() = %v, want it to contain diff", Contexts())
	}
	k, err := Custom("space", map[Context]map[string]string{Diff: {"t": string(ToggleChange)}})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := k.Resolve(Diff, []string{"t"}); a != ToggleChange {
		t.Errorf("t = %q", a)
	}
}
