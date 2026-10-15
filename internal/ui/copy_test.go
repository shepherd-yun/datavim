package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

func TestTreeCopiesNames(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setSchemas(usersSchema())
	clip := captureClipboard(t)
	cases := []struct {
		conn string
		path []string
		keys []string
		want string
	}{
		{"local", []string{"local"}, []string{"y", "y"}, "local"},
		{"local", []string{"app"}, []string{"y", "y"}, "app"},
		{"local", []string{"app", "users"}, []string{"y", "y"}, "users"},
		{"local", []string{"app", "users"}, []string{"y", "q"}, "app.users"},
		{"local", []string{"app"}, []string{"y", "q"}, "app"},
		{"local", []string{"app", "users", "Columns", "name"}, []string{"y", "y"}, "name"},
		{"local", []string{"app", "users", "Columns", "name"}, []string{"y", "q"}, "users.name"},
		{"local", []string{"app", "users", "Columns"}, []string{"y", "y"}, "users"},
		{"local", []string{"app", "users", "Columns"}, []string{"y", "q"}, "app.users"},
	}
	for _, c := range cases {
		*clip = ""
		if len(c.path) == 1 && c.path[0] == c.conn {
			m.setFocus(focusTree)
			m.tree.Top()
		} else {
			selectNode(t, m, c.conn, c.path...)
		}
		press(t, m, c.keys...)
		if *clip != c.want {
			t.Errorf("%v %v: clipboard = %q, want %q", c.path, c.keys, *clip, c.want)
		}
		if !strings.Contains(screen(m), "copied "+c.want) {
			t.Errorf("%v: no status:\n%s", c.path, screen(m))
		}
	}
}

func TestNodeMenuOffersCopy(t *testing.T) {
	m, _ := newTestModel(t)
	selectNode(t, m, "local", "app", "users")
	var got []string
	for _, it := range m.nodeMenu(m.tree.Selected()) {
		got = append(got, it.keys+" "+it.desc)
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{"yy Copy name", "yq Copy qualified name"} {
		if !strings.Contains(all, want) {
			t.Errorf("menu misses %q:\n%s", want, all)
		}
	}
}

func TestStructureCopiesTheNameUnderTheCursor(t *testing.T) {
	m, _ := newTestModel(t)
	clip := captureClipboard(t)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "space", "S")
	if _, ok := m.activeTab().(*structureTab); !ok {
		t.Fatalf("structure not opened:\n%s", screen(m))
	}
	press(t, m, "j", "y", "y")
	if *clip != "name" {
		t.Fatalf("clipboard = %q", *clip)
	}
}

func TestCellEditorYankCopies(t *testing.T) {
	m, _, _ := openUsersTab(t)
	clip := captureClipboard(t)
	press(t, m, "l", "i", "esc", "0", "y", "w")
	if _, ok := m.overlay.(*overlay.CellEditor); !ok {
		t.Fatal("the editor stays open")
	}
	if *clip != "user1" {
		t.Fatalf("clipboard = %q", *clip)
	}
}

func TestConsoleYankCopies(t *testing.T) {
	m, _ := newTestModel(t)
	clip := captureClipboard(t)
	c := openLocalConsole(t, m)
	c.editor.SetValue("select 1")
	c.editor.Top()
	press(t, m, "y", "y")
	if *clip != "select 1\n" {
		t.Fatalf("yy copies the line: %q", *clip)
	}
	*clip = ""
	press(t, m, "d", "d")
	if *clip != "" {
		t.Fatalf("dd keeps the clipboard: %q", *clip)
	}
}
