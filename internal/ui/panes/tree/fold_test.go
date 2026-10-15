package tree

import (
	"strings"
	"testing"
)

func selectName(t *testing.T, m *Model, name string) *Node {
	t.Helper()
	for _, r := range m.visible() {
		if r.node.Name == name {
			m.selected = r.node
			return r.node
		}
	}
	t.Fatalf("%s not visible: %v", name, names(m))
	return nil
}

func TestFoldOnLeafClosesParent(t *testing.T) {
	m, _, cols, _ := tableFixture()
	selectName(t, m, "id")
	m.Fold()
	if cols.Expanded || m.Selected() != cols {
		t.Fatalf("zc on a leaf closes and selects its group; selected %s", m.Selected().Name)
	}
	m.Fold()
	if m.Selected() != cols.Parent() || cols.Parent().Expanded {
		t.Fatal("zc on a closed group closes the table")
	}
}

func TestCloseAllAndOpenLoaded(t *testing.T) {
	m, users, cols, keys := tableFixture()
	selectName(t, m, "id")
	m.CloseAll(users)
	if users.Expanded || cols.Expanded || keys.Expanded {
		t.Fatal("zC closes the whole subtree")
	}
	if m.Selected() != users {
		t.Fatalf("cursor falls back to the visible ancestor, got %s", m.Selected().Name)
	}
	m.OpenLoaded(users)
	if !users.Expanded || !cols.Expanded || !keys.Expanded {
		t.Fatal("zO opens every loaded node under the cursor")
	}
}

func TestOpenTreeLoadsNothing(t *testing.T) {
	m, users, _, _ := tableFixture()
	m.CloseTree()
	prod := m.Roots()[1]
	broken := &Node{Kind: KindTable, Name: "gone", Conn: "local", NS: users.NS, Err: "not found"}
	users.Parent().Children = append(users.Parent().Children, broken)
	broken.parent = users.Parent()
	m.OpenTree()
	if prod.Expanded || prod.Loading {
		t.Fatal("zR leaves an unloaded root alone")
	}
	if broken.Expanded {
		t.Fatal("zR leaves a failed node alone")
	}
	if got := strings.Join(names(m), ","); got != "local,app,users,Columns,id,name,Keys,PRIMARY,gone,prod" {
		t.Fatalf("visible = %s", got)
	}
}

func TestCloseTreeSelectsTheConnection(t *testing.T) {
	m, _, _, _ := tableFixture()
	selectName(t, m, "name")
	m.CloseTree()
	if got := strings.Join(names(m), ","); got != "local,prod" {
		t.Fatalf("visible = %s", got)
	}
	if m.Selected().Name != "local" {
		t.Fatalf("selected %s", m.Selected().Name)
	}
}

func TestFocusPathKeepsOnlyTheCursorPath(t *testing.T) {
	m, users, _, keys := tableFixture()
	keys.Expanded = true
	selectName(t, m, "PRIMARY")
	m.FocusPath()
	if got := strings.Join(names(m), ","); got != "local,app,users,Columns,Keys,PRIMARY,prod" {
		t.Fatalf("visible = %s", got)
	}
	if m.Selected().Name != "PRIMARY" || !users.Expanded {
		t.Fatal("zx keeps the cursor and its ancestors open")
	}
}

func TestCloseAllLeavesLoadingNodesOpen(t *testing.T) {
	m, users, _, _ := tableFixture()
	m.StartLoad(users)
	m.CloseAll(nil)
	if !users.Expanded {
		t.Fatal("a loading node keeps showing loading…")
	}
}
