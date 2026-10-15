package ui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

// menuItem is one entry of the tree's actions menu.
type menuItem struct {
	action keymap.Action
	desc   string
	keys   string // as bound in the keymap; empty when unbound
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
			{keymap.EditConnection, "Edit connection"},
			{keymap.Password, "Enter password"},
			{keymap.Refresh, "Refresh"},
			{keymap.DeleteNode, "Delete connection"},
		}
	case tree.KindDatabase, tree.KindSchema:
		entries = []entry{
			{keymap.OpenConsole, "Open console"},
			{keymap.Refresh, "Refresh"},
		}
	case tree.KindTable, tree.KindView:
		obj := "table"
		if n.Kind == tree.KindView {
			obj = "view"
		}
		entries = []entry{
			{keymap.Expand, "Open data"},
			{keymap.OpenStructure, "Columns"},
			{keymap.OpenFullStructure, "Open structure"},
			{keymap.CopyDDL, "Copy DDL"},
			{keymap.OpenConsole, "Open console"},
			{keymap.RenameTable, "Rename " + obj},
		}
		if n.Kind == tree.KindTable {
			entries = append(entries, entry{keymap.TruncateTable, "Truncate table"})
		}
		entries = append(entries, entry{keymap.DeleteNode, "Drop " + obj})
	}
	items := make([]menuItem, len(entries))
	for i, e := range entries {
		items[i] = menuItem{action: e.a, desc: e.desc, keys: m.keys.KeysFor(keymap.Tree, e.a)}
	}
	return items
}

// openNodeMenu shows the actions menu of the selected node.
func (m *Model) openNodeMenu() {
	n := m.tree.Selected()
	if n == nil {
		return
	}
	items := m.nodeMenu(n)
	labels := make([]string, len(items))
	for i, it := range items {
		labels[i] = fmt.Sprintf("%-10s %s", it.keys, it.desc)
	}
	m.overlay = overlay.NewChoice(n.Name, labels, func(i int) tea.Msg {
		return menuPickedMsg{action: items[i].action}
	})
}
