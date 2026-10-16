package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

// menuItem is one entry of the tree's actions menu.
type menuItem struct {
	action keymap.Action
	desc   string
	keys   string   // as bound in the keymap; empty when unbound
	seq    []string // the keys as key events read; nil when unbound
}

// menuPickedMsg runs the action picked in the actions menu.
type menuPickedMsg struct{ action keymap.Action }

// nodeMenu lists what can be done with n, the tree's right-click menu.
func (m *Model) nodeMenu(n *tree.Node) []menuItem {
	type entry struct {
		a    keymap.Action
		desc string
	}
	var entries []entry
	switch n.Kind {
	case tree.KindConn:
		entries = []entry{
			{keymap.OpenConsole, "Open console"},
			{keymap.EditNode, "Edit connection"},
			{keymap.Password, "Enter password"},
			{keymap.Refresh, "Refresh"},
			{keymap.DeleteNode, "Delete connection"},
		}
	case tree.KindDatabase, tree.KindSchema:
		entries = []entry{
			{keymap.OpenConsole, "Open console"},
			{keymap.Refresh, "Refresh"},
		}
		if cfg, _ := m.backend.Connection(n.Conn); n.Kind == tree.KindSchema || !sqlx.DialectOf(cfg.Driver).Features().Schemas {
			entries = append(entries, entry{keymap.ImportData, "Create table from file…"})
		}
		if _, problem := m.compareSideOf(n); problem == "" {
			entries = append(entries, entry{keymap.MarkCompare, "Mark for compare"}, entry{keymap.Compare, "Compare with the marked one"})
		}
	case tree.KindTable, tree.KindView:
		obj := "table"
		if n.Kind == tree.KindView {
			obj = "view"
		}
		entries = []entry{
			{keymap.Open, "Open data"},
			{keymap.OpenStructure, "Columns"},
			{keymap.OpenFullStructure, "Open structure"},
		}
		if n.Kind == tree.KindTable {
			entries = append(entries, entry{keymap.EditNode, "Modify table"})
			entries = append(entries, entry{keymap.ImportData, "Import data from file…"})
		}
		entries = append(entries,
			entry{keymap.CopyDDL, "Copy DDL"},
			entry{keymap.OpenConsole, "Open console"},
			entry{keymap.RenameTable, "Rename " + obj},
		)

		if n.Kind == tree.KindTable {
			entries = append(entries,
				entry{keymap.MarkCompare, "Mark for compare"},
				entry{keymap.Compare, "Compare with the marked one"},
				entry{keymap.TruncateTable, "Truncate table"},
			)
		}
		entries = append(entries, entry{keymap.DeleteNode, "Drop " + obj})
	case tree.KindGroup:
		entries = []entry{{keymap.Open, "Expand / collapse"}, {keymap.Refresh, "Refresh"}}
		if n.Table().Kind == tree.KindTable {
			entries = append(entries, entry{keymap.EditNode, "Modify table"})
		}
	case tree.KindColumn, tree.KindKey, tree.KindIndex:
		obj := map[tree.Kind]string{tree.KindColumn: "column", tree.KindKey: "key", tree.KindIndex: "index"}[n.Kind]
		entries = []entry{{keymap.Open, "Show in structure"}}
		if n.Table().Kind == tree.KindTable {
			entries = append(entries, entry{keymap.EditNode, "Modify " + obj}, entry{keymap.DeleteNode, "Drop " + obj})
		}
		entries = append(entries, entry{keymap.Refresh, "Refresh"})
	case tree.KindForeignKey:
		entries = []entry{{keymap.Open, "Go to " + n.Ref.Name}, {keymap.Refresh, "Refresh"}}
	}
	entries = append(entries, entry{keymap.YankName, "Copy name"})
	if n.Kind != tree.KindConn {
		entries = append(entries, entry{keymap.YankQualified, "Copy qualified name"})
	}
	items := make([]menuItem, len(entries))
	for i, e := range entries {
		seq := m.keys.SeqFor(keymap.Tree, e.a)
		items[i] = menuItem{action: e.a, desc: e.desc, keys: m.keys.Display(seq), seq: seq}
	}
	return items
}

// openNodeMenu shows the actions menu of the selected node: an entry's
// keys run it, the same keys that run it in the tree.
func (m *Model) openNodeMenu() {
	n := m.tree.Selected()
	if n == nil {
		return
	}
	items := m.nodeMenu(n)
	entries := make([]overlay.MenuItem, len(items))
	for i, it := range items {
		entries[i] = overlay.MenuItem{Keys: it.seq, Label: it.keys, Desc: it.desc}
	}
	m.overlay = overlay.NewMenu(n.Name, entries, func(i int) tea.Msg {
		return menuPickedMsg{action: items[i].action}
	})
}
