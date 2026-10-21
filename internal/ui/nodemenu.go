package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/rcmd"
	"github.com/shepherd-yun/datavim/internal/sqlx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// menuItem is one entry of the tree's actions menu.
type menuItem struct {
	action keymap.Action
	msg    tea.Msg // sent instead of action when set (an entry with no key)
	desc   string
	keys   string   // as bound in the keymap; empty when unbound
	seq    []string // the keys as key events read; nil when unbound
}

// menuEntry is an action of the actions menu before its keys are looked up.
type menuEntry struct {
	a    keymap.Action
	desc string
	msg  tea.Msg // an entry without an action of its own
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
			{a: keymap.OpenConsole, desc: "Open console"},
			{a: keymap.NewKey, desc: "New key"},
			{a: keymap.OpenKey, desc: "Open key by name…"},
		}
		entries = append(entries, m.redisFilterEntries(n)...)
		entries = append(entries,
			entry{a: keymap.BigKeys, desc: "Analyze big keys…"},
			entry{a: keymap.Favorites, desc: "Starred keys"},
			entry{a: keymap.ServerInfo, desc: "Server status"},
			entry{a: keymap.Refresh, desc: "Refresh"},
			entry{a: keymap.DeleteNode, desc: "Delete keys…"},
		)
	case tree.KindKeyFolder:
		entries = []entry{
			{a: keymap.Open, desc: "Expand / collapse"},
			{a: keymap.OpenConsole, desc: "Open console"},
			{a: keymap.NewKey, desc: "New key"},
			{a: keymap.OpenKey, desc: "Open key by name…"},
		}
		entries = append(entries, m.redisFilterEntries(n)...)
		entries = append(entries,
			entry{a: keymap.BigKeys, desc: "Analyze big keys…"},
			entry{a: keymap.Favorites, desc: "Starred keys"},
			entry{a: keymap.ServerInfo, desc: "Server status"},
			entry{a: keymap.Refresh, desc: "Refresh"},
			entry{a: keymap.DeleteNode, desc: "Delete keys under " + rcmd.Escape(n.Key) + "…"},
		)
	case tree.KindRedisKey:
		entries = []entry{
			{a: keymap.Open, desc: "Open"},
			{a: keymap.ToggleFavorite, desc: "Star / unstar"},
			{a: keymap.RenameTable, desc: "Rename key"},
			{a: keymap.OpenConsole, desc: "Open console"},
			{a: keymap.NewKey, desc: "New key"},
			{a: keymap.OpenKey, desc: "Open key by name…"},
		}
		entries = append(entries, m.redisFilterEntries(n)...)
		entries = append(entries,
			entry{a: keymap.BigKeys, desc: "Analyze big keys…"},
			entry{a: keymap.Favorites, desc: "Starred keys"},
			entry{a: keymap.ServerInfo, desc: "Server status"},
			entry{a: keymap.Refresh, desc: "Refresh"},
			entry{a: keymap.DeleteNode, desc: "Delete key"},
		)
	case tree.KindMore:
		if k, ok := redisKeyOf(n); ok {
			if sc := m.scans[k]; sc != nil && !sc.running && !sc.capped && !sc.done {
				entries = append(entries, entry{a: keymap.Open, desc: "Load more keys"})
			}
		}
		entries = append(entries, m.redisFilterEntries(n)...)
		entries = append(entries, entry{a: keymap.Refresh, desc: "Refresh"})
	case tree.KindConn:
		entries = []entry{
			{a: keymap.OpenConsole, desc: "Open console"},
			{a: keymap.EditNode, desc: "Edit connection"},
			{a: keymap.Password, desc: "Enter password"},
			{a: keymap.Refresh, desc: "Refresh"},
			{a: keymap.DeleteNode, desc: "Delete connection"},
		}
		switch {
		case isRedis(m.backend, n.Conn):
			entries = append(entries[:1], append([]entry{
				{a: keymap.ServerInfo, desc: "Server status"},
				{a: keymap.OpenKey, desc: "Open key by name…"},
				{a: keymap.Favorites, desc: "Starred keys"},
			}, entries[1:]...)...)
		case isES(m.backend, n.Conn):
			entries = append([]entry{
				{a: keymap.OpenConsole, desc: "REST console"},
				{a: keymap.OpenKey, desc: "Open document by _id…"},
				{a: keymap.ServerInfo, desc: "Cluster status"},
				{a: keymap.ToggleInternal, desc: "Show / hide system indices"},
				{a: keymap.SortGroups, desc: "Sort indices: name / docs / size"},
			}, entries[1:]...)
		case isKafka(m.backend, n.Conn):
			entries = append([]entry{
				{a: keymap.ServerInfo, desc: "Cluster status"},
				{a: keymap.ToggleInternal, desc: "Show / hide internal topics"},
				{a: keymap.SortGroups, desc: "Sort consumer groups: by lag / by name"},
			}, entries[1:]...)
		default:
			entries = append(entries[:1], append([]entry{{a: keymap.ServerInfo, desc: "Server status"}}, entries[1:]...)...)
		}
	case tree.KindKafkaSection, tree.KindTopicFolder, tree.KindTopic, tree.KindKafkaGroup, tree.KindBroker,
		tree.KindSchemaSubject, tree.KindSchemaVersion, tree.KindKafkaACLs, tree.KindConnectCluster, tree.KindConnector:
		entries = kafkaMenu(n)
	case tree.KindESSection, tree.KindIndexFolder, tree.KindESIndex, tree.KindESAlias, tree.KindDataStream, tree.KindESTemplate,
		tree.KindESPolicy, tree.KindESPipeline:
		entries = esMenu(n)
	case tree.KindDatabase, tree.KindSchema:
		entries = []entry{
			{a: keymap.OpenConsole, desc: "Open console"},
			{a: keymap.ServerInfo, desc: "Server status"},
			{a: keymap.Refresh, desc: "Refresh"},
		}
		if cfg, _ := m.backend.Connection(n.Conn); n.Kind == tree.KindSchema || !sqlx.DialectOf(cfg.Driver).Features().Schemas {
			entries = append(entries, entry{a: keymap.ImportData, desc: "Create table from file…"})
		}
		if _, problem := m.compareSideOf(n); problem == "" {
			entries = append(entries, entry{a: keymap.MarkCompare, desc: "Mark for compare"}, entry{a: keymap.Compare, desc: "Compare with the marked one"})
		}
	case tree.KindTable, tree.KindView:
		obj := "table"
		if n.Kind == tree.KindView {
			obj = "view"
		}
		entries = []entry{
			{a: keymap.Open, desc: "Open data"},
			{a: keymap.OpenStructure, desc: "Columns"},
			{a: keymap.OpenFullStructure, desc: "Open structure"},
		}
		if n.Kind == tree.KindTable {
			entries = append(entries, entry{a: keymap.EditNode, desc: "Modify table"})
			entries = append(entries, entry{a: keymap.ImportData, desc: "Import data from file…"})
		}
		entries = append(entries,
			entry{a: keymap.CopyDDL, desc: "Copy DDL"},
			entry{a: keymap.OpenConsole, desc: "Open console"},
			entry{a: keymap.RenameTable, desc: "Rename " + obj},
		)

		if n.Kind == tree.KindTable {
			entries = append(entries,
				entry{a: keymap.MarkCompare, desc: "Mark for compare"},
				entry{a: keymap.Compare, desc: "Compare with the marked one"},
				entry{a: keymap.TruncateTable, desc: "Truncate table"},
			)
		}
		entries = append(entries, entry{a: keymap.DeleteNode, desc: "Drop " + obj})
	case tree.KindGroup:
		entries = []entry{{a: keymap.Open, desc: "Expand / collapse"}, {a: keymap.Refresh, desc: "Refresh"}}
		if n.Table().Kind == tree.KindTable {
			entries = append(entries, entry{a: keymap.EditNode, desc: "Modify table"})
		}
	case tree.KindColumn, tree.KindKey, tree.KindIndex:
		obj := map[tree.Kind]string{tree.KindColumn: "column", tree.KindKey: "key", tree.KindIndex: "index"}[n.Kind]
		entries = []entry{{a: keymap.Open, desc: "Show in structure"}}
		if n.Table().Kind == tree.KindTable {
			entries = append(entries, entry{a: keymap.EditNode, desc: "Modify " + obj}, entry{a: keymap.DeleteNode, desc: "Drop " + obj})
		}
		entries = append(entries, entry{a: keymap.Refresh, desc: "Refresh"})
	case tree.KindForeignKey:
		entries = []entry{{a: keymap.Open, desc: "Go to " + n.Ref.Name}, {a: keymap.Refresh, desc: "Refresh"}}
	}
	if n.Kind != tree.KindMore {
		entries = append(entries, entry{a: keymap.YankName, desc: "Copy name"})
	}
	if n.Kind != tree.KindConn && !isRedisNode(n) {
		entries = append(entries, entry{a: keymap.YankQualified, desc: "Copy qualified name"})
	}
	items := make([]menuItem, len(entries))
	for i, e := range entries {
		seq := m.keys.SeqFor(keymap.Tree, e.a)
		if e.msg != nil {
			items[i] = menuItem{msg: e.msg, desc: e.desc}
			continue
		}
		items[i] = menuItem{action: e.a, desc: e.desc, keys: m.keys.Display(seq), seq: seq}
	}
	return items
}

// redisFilterEntries are the key filter's entries for a node of a redis db:
// clearing it only while one is set.
func (m *Model) redisFilterEntries(n *tree.Node) []menuEntry {
	out := []menuEntry{{a: keymap.FilterPrompt, desc: "Filter keys…"}}
	if k, ok := redisKeyOf(n); ok {
		if s := m.scans[k]; s != nil && s.pattern != "" {
			out = append(out, menuEntry{a: keymap.FilterClear, desc: "Clear the filter (" + rcmd.Escape(s.pattern) + ")"})
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
		if items[i].msg != nil {
			return items[i].msg
		}
		return menuPickedMsg{action: items[i].action}
	})
}

// kafkaMenu lists what can be done with a node of a kafka tree.
func kafkaMenu(n *tree.Node) []menuEntry {
	var entries []menuEntry
	switch n.Kind {
	case tree.KindTopic:
		entries = append(entries, menuEntry{a: keymap.Open, desc: "Open topic"}, menuEntry{a: keymap.OpenConsole, desc: "Producer console"})
	case tree.KindKafkaGroup:
		entries = append(entries, menuEntry{a: keymap.Open, desc: "Open group"})
	case tree.KindTopicFolder:
		entries = append(entries, menuEntry{a: keymap.Open, desc: "Expand / collapse"})
	case tree.KindSchemaSubject:
		entries = append(entries, menuEntry{a: keymap.Open, desc: "Open subject"})
	case tree.KindSchemaVersion:
		entries = append(entries, menuEntry{a: keymap.Open, desc: "Open this version"})
	case tree.KindKafkaACLs:
		entries = append(entries, menuEntry{a: keymap.Open, desc: "Open the ACLs"})
	case tree.KindConnector:
		entries = append(entries, menuEntry{a: keymap.Open, desc: "Open connector"})
	}
	switch n.Section {
	case secTopics:
		entries = append(entries, menuEntry{a: keymap.NewKey, desc: "New topic…"}, menuEntry{a: keymap.FilterPrompt, desc: "Filter topics…"},
			menuEntry{a: keymap.FilterClear, desc: "Clear the topic filter"}, menuEntry{a: keymap.ToggleInternal, desc: "Show / hide internal topics"})
	case secGroups:
		entries = append(entries, menuEntry{a: keymap.FilterPrompt, desc: "Filter groups…"}, menuEntry{a: keymap.FilterClear, desc: "Clear the group filter"},
			menuEntry{a: keymap.SortGroups, desc: "Sort: by lag / by name"})
	case secSchemas:
		entries = append(entries, menuEntry{a: keymap.FilterPrompt, desc: "Filter subjects…"}, menuEntry{a: keymap.FilterClear, desc: "Clear the subject filter"})
	}
	entries = append(entries, menuEntry{a: keymap.ServerInfo, desc: "Cluster status"}, menuEntry{a: keymap.Refresh, desc: "Refresh"})
	switch n.Kind {
	case tree.KindTopic:
		entries = append(entries, menuEntry{a: keymap.DeleteNode, desc: "Delete topic…"})
	case tree.KindKafkaGroup:
		entries = append(entries, menuEntry{a: keymap.DeleteNode, desc: "Delete group…"})
	case tree.KindSchemaVersion:
		entries = append(entries, menuEntry{a: keymap.DeleteNode, desc: "Soft-delete version…"})
	}
	return entries
}

// esMenu is the actions menu of a node under an elasticsearch connection.
func esMenu(n *tree.Node) []menuEntry {
	var entries []menuEntry
	switch n.Kind {
	case tree.KindESIndex:
		entries = append(entries, menuEntry{a: keymap.Open, desc: "Open index"}, menuEntry{a: keymap.OpenKey, desc: "Open document by _id…"})
		entries = append(entries, esIndexMenu(n.Conn, n.Key, strings.Contains(n.Detail, "[closed]"))...)
		entries = append(entries, menuEntry{a: keymap.DeleteNode, desc: "Delete index…"})
	case tree.KindESAlias:
		entries = append(entries, menuEntry{a: keymap.Open, desc: "Open alias"}, menuEntry{a: keymap.OpenKey, desc: "Open document by _id…"})
	case tree.KindDataStream:
		entries = append(entries, menuEntry{a: keymap.Open, desc: "Open data stream"}, menuEntry{a: keymap.OpenKey, desc: "Open document by _id…"})
	case tree.KindESTemplate:
		entries = append(entries, menuEntry{a: keymap.Open, desc: "Open template"}, menuEntry{a: keymap.DeleteNode, desc: "Delete template…"})
	case tree.KindESPolicy:
		entries = append(entries, menuEntry{a: keymap.Open, desc: "Open ILM policy"}, menuEntry{a: keymap.DeleteNode, desc: "Delete ILM policy…"})
	case tree.KindESPipeline:
		entries = append(entries, menuEntry{a: keymap.Open, desc: "Open pipeline"}, menuEntry{a: keymap.DeleteNode, desc: "Delete pipeline…"})
	case tree.KindIndexFolder:
		entries = append(entries, menuEntry{a: keymap.Open, desc: "Expand / collapse"},
			menuEntry{desc: "Open " + n.Key + "* as one tab", msg: openIndexMsg{conn: n.Conn, target: n.Key + "*", kind: targetPattern}},
			menuEntry{a: keymap.DeleteNode, desc: "Delete these indices…"})
	}
	if n.Section == esSecIndices {
		entries = append(entries, menuEntry{a: keymap.NewKey, desc: "New index…"}, menuEntry{a: keymap.FilterPrompt, desc: "Filter indices…"}, menuEntry{a: keymap.FilterClear, desc: "Clear the index filter"},
			menuEntry{a: keymap.ToggleInternal, desc: "Show / hide system indices"}, menuEntry{a: keymap.SortGroups, desc: "Sort: name / docs / size"})
	}
	return append(entries, menuEntry{a: keymap.OpenConsole, desc: "REST console"}, menuEntry{a: keymap.ServerInfo, desc: "Cluster status"}, menuEntry{a: keymap.Refresh, desc: "Refresh"})
}

// isESNode reports whether n is a node under an elasticsearch connection.
func isESNode(n *tree.Node) bool {
	switch n.Kind {
	case tree.KindESSection, tree.KindIndexFolder, tree.KindESIndex, tree.KindESAlias, tree.KindDataStream, tree.KindESTemplate,
		tree.KindESPolicy, tree.KindESPipeline:
		return true
	}
	return false
}

// isKafkaNode reports whether n is a node under a kafka connection.
func isKafkaNode(n *tree.Node) bool {
	switch n.Kind {
	case tree.KindKafkaSection, tree.KindTopicFolder, tree.KindTopic, tree.KindKafkaGroup, tree.KindBroker,
		tree.KindSchemaSubject, tree.KindSchemaVersion, tree.KindKafkaACLs, tree.KindConnectCluster, tree.KindConnector:
		return true
	}
	return false
}

// isRedisNode reports whether n is a Redis db, folder, key or "load more" line.
func isRedisNode(n *tree.Node) bool {
	_, ok := redisKeyOf(n)
	return ok
}
