package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/rcmd"
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

// menuEntry is an action of the actions menu before its keys are looked up.
type menuEntry struct {
	a    keymap.Action
	desc string
}

// menuPickedMsg runs the action picked in the actions menu.
type menuPickedMsg struct{ action keymap.Action }

// nodeMenu lists what can be done with n, the tree's right-click menu.
func (m *Model) nodeMenu(n *tree.Node) []menuItem {
	type entry = menuEntry
	var entries []entry
	switch n.Kind {
	case tree.KindRedisDB:
		entries = []entry{
			{keymap.OpenConsole, "Open console"},
			{keymap.NewKey, "New key"},
			{keymap.OpenKey, "Open key by name…"},
		}
		entries = append(entries, m.redisFilterEntries(n)...)
		entries = append(entries,
			entry{keymap.BigKeys, "Analyze big keys…"},
			entry{keymap.Favorites, "Starred keys"},
			entry{keymap.ServerInfo, "Server status"},
			entry{keymap.Refresh, "Refresh"},
			entry{keymap.DeleteNode, "Delete keys…"},
		)
	case tree.KindKeyFolder:
		entries = []entry{
			{keymap.Open, "Expand / collapse"},
			{keymap.OpenConsole, "Open console"},
			{keymap.NewKey, "New key"},
			{keymap.OpenKey, "Open key by name…"},
		}
		entries = append(entries, m.redisFilterEntries(n)...)
		entries = append(entries,
			entry{keymap.BigKeys, "Analyze big keys…"},
			entry{keymap.Favorites, "Starred keys"},
			entry{keymap.ServerInfo, "Server status"},
			entry{keymap.Refresh, "Refresh"},
			entry{keymap.DeleteNode, "Delete keys under " + rcmd.Escape(n.Key) + "…"},
		)
	case tree.KindRedisKey:
		entries = []entry{
			{keymap.Open, "Open"},
			{keymap.ToggleFavorite, "Star / unstar"},
			{keymap.RenameTable, "Rename key"},
			{keymap.OpenConsole, "Open console"},
			{keymap.NewKey, "New key"},
			{keymap.OpenKey, "Open key by name…"},
		}
		entries = append(entries, m.redisFilterEntries(n)...)
		entries = append(entries,
			entry{keymap.BigKeys, "Analyze big keys…"},
			entry{keymap.Favorites, "Starred keys"},
			entry{keymap.ServerInfo, "Server status"},
			entry{keymap.Refresh, "Refresh"},
			entry{keymap.DeleteNode, "Delete key"},
		)
	case tree.KindMore:
		if k, ok := redisKeyOf(n); ok {
			if sc := m.scans[k]; sc != nil && !sc.running && !sc.capped && !sc.done {
				entries = append(entries, entry{keymap.Open, "Load more keys"})
			}
		}
		entries = append(entries, m.redisFilterEntries(n)...)
		entries = append(entries, entry{keymap.Refresh, "Refresh"})
	case tree.KindConn:
		entries = []entry{
			{keymap.OpenConsole, "Open console"},
			{keymap.EditNode, "Edit connection"},
			{keymap.Password, "Enter password"},
			{keymap.Refresh, "Refresh"},
			{keymap.DeleteNode, "Delete connection"},
		}
		switch {
		case isRedis(m.backend, n.Conn):
			entries = append(entries[:1], append([]entry{
				{keymap.ServerInfo, "Server status"},
				{keymap.OpenKey, "Open key by name…"},
				{keymap.Favorites, "Starred keys"},
			}, entries[1:]...)...)
		case isKafka(m.backend, n.Conn):
			entries = append([]entry{
				{keymap.ServerInfo, "Cluster status"},
				{keymap.ToggleInternal, "Show / hide internal topics"},
				{keymap.SortGroups, "Sort consumer groups: by lag / by name"},
			}, entries[1:]...)
		default:
			entries = append(entries[:1], append([]entry{{keymap.ServerInfo, "Server status"}}, entries[1:]...)...)
		}
	case tree.KindKafkaSection, tree.KindTopicFolder, tree.KindTopic, tree.KindKafkaGroup, tree.KindBroker,
		tree.KindSchemaSubject, tree.KindSchemaVersion:
		entries = kafkaMenu(n)
	case tree.KindDatabase, tree.KindSchema:
		entries = []entry{
			{keymap.OpenConsole, "Open console"},
			{keymap.ServerInfo, "Server status"},
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
	if n.Kind != tree.KindMore {
		entries = append(entries, entry{keymap.YankName, "Copy name"})
	}
	if n.Kind != tree.KindConn && !isRedisNode(n) {
		entries = append(entries, entry{keymap.YankQualified, "Copy qualified name"})
	}
	items := make([]menuItem, len(entries))
	for i, e := range entries {
		seq := m.keys.SeqFor(keymap.Tree, e.a)
		items[i] = menuItem{action: e.a, desc: e.desc, keys: m.keys.Display(seq), seq: seq}
	}
	return items
}

// redisFilterEntries are the key filter's entries for a node of a redis db:
// clearing it only while one is set.
func (m *Model) redisFilterEntries(n *tree.Node) []menuEntry {
	out := []menuEntry{{keymap.FilterPrompt, "Filter keys…"}}
	if k, ok := redisKeyOf(n); ok {
		if s := m.scans[k]; s != nil && s.pattern != "" {
			out = append(out, menuEntry{keymap.FilterClear, "Clear the filter (" + rcmd.Escape(s.pattern) + ")"})
		}
	}
	return out
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

// kafkaMenu lists what can be done with a node of a kafka tree.
func kafkaMenu(n *tree.Node) []menuEntry {
	var entries []menuEntry
	switch n.Kind {
	case tree.KindTopic:
		entries = append(entries, menuEntry{keymap.Open, "Open topic"}, menuEntry{keymap.OpenConsole, "Producer console"})
	case tree.KindKafkaGroup:
		entries = append(entries, menuEntry{keymap.Open, "Open group"})
	case tree.KindTopicFolder:
		entries = append(entries, menuEntry{keymap.Open, "Expand / collapse"})
	case tree.KindSchemaSubject:
		entries = append(entries, menuEntry{keymap.Open, "Open subject"})
	case tree.KindSchemaVersion:
		entries = append(entries, menuEntry{keymap.Open, "Open this version"})
	}
	switch n.Section {
	case secTopics:
		entries = append(entries, menuEntry{keymap.NewKey, "New topic…"}, menuEntry{keymap.FilterPrompt, "Filter topics…"},
			menuEntry{keymap.FilterClear, "Clear the topic filter"}, menuEntry{keymap.ToggleInternal, "Show / hide internal topics"})
	case secGroups:
		entries = append(entries, menuEntry{keymap.FilterPrompt, "Filter groups…"}, menuEntry{keymap.FilterClear, "Clear the group filter"},
			menuEntry{keymap.SortGroups, "Sort: by lag / by name"})
	case secSchemas:
		entries = append(entries, menuEntry{keymap.FilterPrompt, "Filter subjects…"}, menuEntry{keymap.FilterClear, "Clear the subject filter"})
	}
	entries = append(entries, menuEntry{keymap.ServerInfo, "Cluster status"}, menuEntry{keymap.Refresh, "Refresh"})
	switch n.Kind {
	case tree.KindTopic:
		entries = append(entries, menuEntry{keymap.DeleteNode, "Delete topic…"})
	case tree.KindKafkaGroup:
		entries = append(entries, menuEntry{keymap.DeleteNode, "Delete group…"})
	case tree.KindSchemaVersion:
		entries = append(entries, menuEntry{keymap.DeleteNode, "Soft-delete version…"})
	}
	return entries
}

// isKafkaNode reports whether n is a node under a kafka connection.
func isKafkaNode(n *tree.Node) bool {
	switch n.Kind {
	case tree.KindKafkaSection, tree.KindTopicFolder, tree.KindTopic, tree.KindKafkaGroup, tree.KindBroker,
		tree.KindSchemaSubject, tree.KindSchemaVersion:
		return true
	}
	return false
}

// isRedisNode reports whether n is a Redis db, folder, key or "load more" line.
func isRedisNode(n *tree.Node) bool {
	_, ok := redisKeyOf(n)
	return ok
}
