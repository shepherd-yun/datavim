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
