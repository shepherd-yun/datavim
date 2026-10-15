package keymap

import (
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
		{Grid, []string{"space", "e"}, Recent, Matched},
		{Tree, []string{"space", "space"}, GlobalSearch, Matched},
		{Console, []string{"space", "space"}, GlobalSearch, Matched},
		{Tree, []string{"space", "s"}, OpenStructure, Matched},
		{Grid, []string{"space", "b"}, ToggleTree, Matched},
		{Tree, []string{"3"}, GotoTab(3), Matched},
		{Tree, []string{"z"}, "", NoMatch},
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
	hints := Default().Continuations(Tree, []string{"space"})
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
	for _, e := range Default().Help(Tree) {
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
		{Results, []string{"]"}, LoadMore},
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
	if a, res := k.Resolve(Tree, []string{"e"}); a != EditConnection || res != Matched {
		t.Errorf("e: %q %v", a, res)
	}
	if a, res := k.Resolve(Tree, []string{"d", "d"}); a != DeleteNode || res != Matched {
		t.Errorf("dd: %q %v", a, res)
	}
	for key, want := range map[string]Action{"a": NodeActions, "r": RenameTable} {
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
		if _, res := k.Resolve(Results, seq); res == Matched {
			t.Errorf("console results are not filtered: %s", keys)
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
		if a, res := k.Resolve(Results, seq); res == Matched && a == want {
			t.Errorf("console results are read-only: %s", keys)
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
