package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/rcmd"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

// copyNodeName copies a tree node's name: as shown, or qualified and
// quoted as SQL writes it (db.table on MySQL, schema.table on PostgreSQL).
func (m *Model) copyNodeName(n *tree.Node, qualified bool) tea.Cmd {
	if n.Kind == tree.KindGroup {
		n = n.Table() // a group has no name of its own worth copying
	}
	if n.Kind == tree.KindRedisKey {
		text := rcmd.Quote(n.Key) // as redis-cli takes it
		return m.copyText(overlay.CopyMsg{Text: text, What: rcmd.Escape(n.Key)})
	}
	text := n.Name
	if qualified {
		text = m.qualifiedName(n)
	}
	return m.copyText(overlay.CopyMsg{Text: text, What: text})
}

func (m *Model) qualifiedName(n *tree.Node) string {
	cfg, _ := m.backend.Connection(n.Conn)
	d := sqlx.DialectOf(cfg.Driver)
	var parts []string
	switch n.Kind {
	case tree.KindConn:
		return n.Name
	case tree.KindDatabase:
		parts = []string{n.NS.Database}
	case tree.KindSchema:
		parts = []string{n.NS.Database, n.NS.Schema}
	case tree.KindColumn, tree.KindKey, tree.KindForeignKey, tree.KindIndex:
		parts = []string{n.Table().Name, n.Name}
	default:
		ns := n.NS.Schema
		if ns == "" {
			ns = n.NS.Database
		}
		parts = []string{ns, n.Name}
	}
	for i, p := range parts {
		parts[i] = sqlx.QuoteIfNeeded(p, d)
	}
	return strings.Join(parts, ".")
}
