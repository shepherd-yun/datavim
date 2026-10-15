package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

type childrenLoadedMsg struct {
	node     *tree.Node
	children []*tree.Node
	err      error
}

// loadChildren copies what it needs from n so the goroutine never reads
// fields the UI goroutine may be writing.
func loadChildren(b Backend, n *tree.Node) tea.Cmd {
	kind, connName, ns, name := n.Kind, n.Conn, n.NS, n.Name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		var children []*tree.Node
		err := withConn(ctx, b, connName, ns.Database, func(c db.Conn) error {
			var err error
			children, err = fetchChildren(ctx, c, kind, connName, ns, name)
			return err
		})
		if err != nil {
			slog.Warn("load tree children", "conn", connName, "database", ns.Database, "schema", ns.Schema, "err", err)
		}
		return childrenLoadedMsg{node: n, children: children, err: err}
	}
}

func fetchChildren(ctx context.Context, c db.Conn, kind tree.Kind, connName string, ns db.Namespace, name string) ([]*tree.Node, error) {
	meta := c.Meta()
	switch kind {
	case tree.KindConn:
		names, err := meta.Databases(ctx)
		if err != nil {
			return nil, err
		}
		nodes := make([]*tree.Node, len(names))
		for i, name := range names {
			nodes[i] = &tree.Node{Kind: tree.KindDatabase, Name: name, Conn: connName, NS: db.Namespace{Database: name}}
		}
		return nodes, nil
	case tree.KindDatabase:
		schemas, err := meta.Schemas(ctx, ns.Database)
		if err != nil {
			return nil, err
		}
		if len(schemas) == 0 {
			return tableNodes(ctx, meta, connName, ns)
		}
		nodes := make([]*tree.Node, len(schemas))
		for i, s := range schemas {
			nodes[i] = &tree.Node{Kind: tree.KindSchema, Name: s, Conn: connName, NS: db.Namespace{Database: ns.Database, Schema: s}}
		}
		return nodes, nil
	case tree.KindSchema:
		return tableNodes(ctx, meta, connName, ns)
	case tree.KindTable:
		ref := db.TableRef{Namespace: ns, Name: name}
		s, err := meta.TableSchema(ctx, ref)
		if errors.Is(err, db.ErrNotFound) {
			return nil, fmt.Errorf("table %s not found", name)
		}
		if err != nil {
			return nil, err
		}
		return tableChildren(connName, s), nil
	case tree.KindView:
		ref := db.TableRef{Namespace: ns, Name: name}
		cols, err := meta.Columns(ctx, ref)
		if err != nil {
			return nil, err
		}
		return tableChildren(connName, db.TableSchema{Ref: ref, Columns: cols}), nil
	}
	return nil, fmt.Errorf("tree node kind %d has no children", kind)
}

func tableNodes(ctx context.Context, meta db.MetaReader, connName string, ns db.Namespace) ([]*tree.Node, error) {
	tables, err := meta.Tables(ctx, ns)
	if err != nil {
		return nil, err
	}
	nodes := make([]*tree.Node, len(tables))
	for i, t := range tables {
		kind := tree.KindTable
		if t.Kind == db.TableKindView {
			kind = tree.KindView
		}
		nodes[i] = &tree.Node{Kind: kind, Name: t.Name, Conn: connName, NS: ns}
	}
	return nodes, nil
}
