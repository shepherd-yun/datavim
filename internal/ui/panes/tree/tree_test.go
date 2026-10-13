package tree

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func fixture() *Model {
	m := New([]*Node{
		{Kind: KindConn, Name: "local", Conn: "local"},
		{Kind: KindConn, Name: "prod", Conn: "prod", Prod: true},
	})
	m.SetHeight(10)
	return m
}

func names(m *Model) []string {
	var out []string
	for _, r := range m.visible() {
		out = append(out, r.node.Name)
	}
	return out
}

func TestExpandLoadAndCollapse(t *testing.T) {
	m := fixture()
	if !m.Expand() {
		t.Fatal("first expand of an unloaded node needs a load")
	}
	root := m.Selected()
	if !root.Loading || !root.Expanded {
		t.Fatalf("root = %+v", root)
	}
	m.SetChildren(root, []*Node{{Kind: KindDatabase, Name: "app", Conn: "local"}}, nil)
	if got := strings.Join(names(m), ","); got != "local,app,prod" {
		t.Fatalf("visible = %s", got)
	}
	m.Move(1)
	if m.Selected().Name != "app" || m.Selected().Parent() != root {
		t.Fatalf("selected = %+v", m.Selected())
	}
	m.Collapse() // app is collapsed already: jump to parent
	if m.Selected() != root {
		t.Fatal("h on a collapsed node selects its parent")
	}
	m.Collapse()
	if root.Expanded {
		t.Fatal("h on an expanded node collapses it")
	}
	if m.Expand() {
		t.Fatal("re-expanding a loaded node needs no load")
	}
}

func TestCursorFollowsNodeAcrossAsyncLoads(t *testing.T) {
	m := fixture()
	m.Expand()
	root := m.Selected()
	m.Move(1) // select "prod" while "local" is still loading
	m.SetChildren(root, []*Node{{Name: "a"}, {Name: "b"}}, nil)
	if m.Selected().Name != "prod" {
		t.Fatalf("selected = %s, want prod", m.Selected().Name)
	}
}

func TestLoadErrorAllowsRetry(t *testing.T) {
	m := fixture()
	m.Expand()
	root := m.Selected()
	m.SetChildren(root, nil, errors.New("connection refused"))
	if root.Err != "connection refused" || root.Expanded || root.Loading {
		t.Fatalf("root = %+v", root)
	}
	if !strings.Contains(ansi.Strip(m.View(60, true)), "connection refused") {
		t.Fatal("error must be visible")
	}
	if !m.Expand() || root.Err != "" {
		t.Fatal("expand after an error retries and clears it")
	}
}

func TestCancelLoadAndResetConn(t *testing.T) {
	m := fixture()
	m.Expand()
	root := m.Selected()
	m.CancelLoad(root)
	if root.Loading || root.Expanded {
		t.Fatalf("cancel: %+v", root)
	}
	m.Expand()
	m.SetChildren(root, []*Node{{Name: "app"}}, nil)
	m.Move(1)
	m.ResetConn("local")
	if root.Loaded || root.Expanded || len(root.Children) != 0 {
		t.Fatalf("reset: %+v", root)
	}
	if m.Selected() != root {
		t.Fatal("cursor falls back to the nearest visible ancestor")
	}
}

func TestMovementClampsAndScrolls(t *testing.T) {
	var roots []*Node
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		roots = append(roots, &Node{Kind: KindConn, Name: n})
	}
	m := New(roots)
	m.SetHeight(3)
	m.Move(-5)
	if m.Selected().Name != "a" {
		t.Fatal("clamp at top")
	}
	m.Bottom()
	if m.Selected().Name != "f" {
		t.Fatal("bottom")
	}
	view := ansi.Strip(m.View(20, true))
	if !strings.Contains(view, "f") || strings.Contains(view, "a") {
		t.Fatalf("view must scroll to keep the cursor visible:\n%s", view)
	}
	m.Top()
	m.HalfPage(1)
	if m.Selected().Name != "b" {
		t.Fatalf("half page of height 3 moves 1, got %s", m.Selected().Name)
	}
}

func TestViewMarksProdAndHandlesEmpty(t *testing.T) {
	m := fixture()
	m.SetHeight(10)
	if !strings.Contains(ansi.Strip(m.View(40, false)), "[PROD]") {
		t.Fatal("prod marker")
	}
	empty := New(nil)
	empty.SetHeight(5)
	if empty.Selected() != nil || empty.View(10, true) != "" || empty.Expand() {
		t.Fatal("empty tree")
	}
}
