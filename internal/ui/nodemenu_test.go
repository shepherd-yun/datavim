package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

func TestActionMenuOnTable(t *testing.T) {
	m, _ := newTestModel(t)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "a")
	s := screen(m)
	for _, want := range []string{"users", "Open data", "Copy DDL", "Rename table", "Truncate table", "Drop table", "dd"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
	// picking an entry runs it: Truncate asks for the table's name
	at := slices.IndexFunc(m.nodeMenu(m.tree.Selected()), func(it menuItem) bool { return it.action == keymap.TruncateTable })
	for range at {
		press(t, m, "j")
	}
	press(t, m, "enter")
	if !strings.Contains(screen(m), "Truncate table users") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestActionMenuRunsAnEntryByItsKeys(t *testing.T) {
	m, _ := newTestModel(t)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "a")
	wantScreen(t, m, "T", "Truncate table")
	press(t, m, "T")
	wantScreen(t, m, "Truncate table users")
	press(t, m, "esc")

	// a two-key entry waits for its second key
	press(t, m, "a")
	press(t, m, "d")
	wantScreen(t, m, "Drop table")
	press(t, m, "d")
	wantScreen(t, m, "Drop table users", "Type users to confirm")
}

func TestActionMenuFollowsRemappedKeys(t *testing.T) {
	m, _ := newTestModel(t)
	k, err := keymap.Custom("", map[keymap.Context]map[string]string{keymap.Tree: {"T": "", "X": "table.truncate"}})
	if err != nil {
		t.Fatal(err)
	}
	m.SetKeymap(k)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "a")
	press(t, m, "X")
	wantScreen(t, m, "Truncate table users")
}

func TestTruncateKeyInTheTree(t *testing.T) {
	m, _ := newTestModel(t)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "T")
	wantScreen(t, m, "Truncate table users")
}

func TestActionMenuOnView(t *testing.T) {
	m, _ := newTestModel(t)
	selectNode(t, m, "local", "app", "active")
	press(t, m, "a")
	s := screen(m)
	if !strings.Contains(s, "Drop view") || !strings.Contains(s, "Rename view") || strings.Contains(s, "Truncate") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestActionMenuOnConnection(t *testing.T) {
	m, _ := newTestModel(t)
	press(t, m, "a")
	s := screen(m)
	if !strings.Contains(s, "Edit connection") || !strings.Contains(s, "Delete connection") || strings.Contains(s, "Drop") {
		t.Fatalf("screen:\n%s", s)
	}
}

func TestActionMenuShowsRemappedKeys(t *testing.T) {
	m, _ := newTestModel(t)
	k, err := keymap.Custom("", map[keymap.Context]map[string]string{
		keymap.Tree: {"dd": "", "D": "node.delete", "T": "table.truncate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.SetKeymap(k)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "a")
	for _, it := range m.nodeMenu(m.tree.Selected()) {
		switch it.action {
		case keymap.DeleteNode:
			if it.keys != "D" {
				t.Fatalf("drop shows %q", it.keys)
			}
		case keymap.TruncateTable:
			if it.keys != "T" {
				t.Fatalf("truncate shows %q", it.keys)
			}
		}
	}
}

func TestActionMenuOffersCompare(t *testing.T) {
	m, _ := newTestModel(t)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "a")
	wantScreen(t, m, "Mark for compare", "Compare with the marked one")
	press(t, m, "esc")
	selectNode(t, m, "local", "app")
	press(t, m, "a")
	wantScreen(t, m, "Mark for compare")
	press(t, m, "esc")
	selectNode(t, m, "prod", "app") // a PostgreSQL database is not a namespace
	press(t, m, "a")
	if strings.Contains(screen(m), "Mark for compare") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestActionMenuOnColumn(t *testing.T) {
	m, b := newTestModel(t)
	expandUsers(t, m, b)
	selectNode(t, m, "local", "app", "users", "Columns", "name")
	press(t, m, "a")
	s := screen(m)
	for _, want := range []string{"Show in structure", "Modify column", "Drop column", "Copy name"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "Drop table") {
		t.Fatalf("a column's menu must not offer to drop the table:\n%s", s)
	}
}

func menuDescs(m *Model) []string {
	var out []string
	for _, it := range m.nodeMenu(m.tree.Selected()) {
		out = append(out, it.desc)
	}
	return out
}

func TestRedisNodeMenu(t *testing.T) {
	m, _ := newKeyModel(t, "user:1", "solo")
	if got := menuDescs(m); !slices.Equal(got, []string{
		"Open console", "New key", "Open key by name…", "Filter keys…", "Analyze big keys…",
		"Starred keys", "Server status", "Refresh", "Delete keys…", "Copy name",
	}) {
		t.Fatalf("db: %q", got)
	}
	pressAll(t, m, "j")
	if got := menuDescs(m); !slices.Equal(got, []string{
		"Expand / collapse", "Open console", "New key", "Open key by name…", "Filter keys…",
		"Analyze big keys…", "Starred keys", "Server status", "Refresh", "Delete keys under user:…", "Copy name",
	}) {
		t.Fatalf("folder: %q", got)
	}
	pressAll(t, m, "j")
	if got := menuDescs(m); !slices.Equal(got, []string{
		"Open", "Star / unstar", "Rename key", "Open console", "New key", "Open key by name…",
		"Filter keys…", "Analyze big keys…", "Starred keys", "Server status", "Refresh", "Delete key", "Copy name",
	}) {
		t.Fatalf("key: %q", got)
	}
	pressAll(t, m, "a")
	if s := screen(m); !strings.Contains(s, "Rename key") || strings.Contains(s, "Drop table") {
		t.Fatalf("menu:\n%s", s)
	}
	pressAll(t, m, "esc")
	pressAll(t, m, "g", "g")
	if got := menuDescs(m); !slices.Equal(got, []string{
		"Open console", "Server status", "Open key by name…", "Starred keys", "Edit connection",
		"Enter password", "Refresh", "Delete connection", "Copy name",
	}) {
		t.Fatalf("connection: %q", got)
	}
}

func TestRedisNodeMenuFilter(t *testing.T) {
	m, _ := newKeyModel(t, "user:1", "solo")
	pressAll(t, m, "a")
	pressAll(t, m, "F") // the menu runs an entry by its keys
	answer(t, m, "user:*")
	if got := menuDescs(m); !slices.Contains(got, "Clear the filter (user:*)") {
		t.Fatalf("filtered db: %q", got)
	}
	pressAll(t, m, "a", "f", "x")
	if got := menuDescs(m); slices.ContainsFunc(got, func(d string) bool { return strings.HasPrefix(d, "Clear the filter") }) {
		t.Fatalf("filter not cleared: %q", got)
	}
}

func TestRedisNodeMenuOnLoadMore(t *testing.T) {
	m, _, r := newRedisModel(t, config.Connection{})
	r.fill(0, "k", 25_000)
	dbn := expandDB0(t, m)
	m.tree.SelectWhere(func(n *tree.Node) bool { return n.Kind == tree.KindMore })
	if got := menuDescs(m); !slices.Equal(got, []string{"Load more keys", "Filter keys…", "Refresh"}) {
		t.Fatalf("load more: %q", got)
	}
	pressAll(t, m, "a", "enter")
	if got := countKeys(dbn); got != 20_000 {
		t.Fatalf("after load more = %d keys", got)
	}
}
