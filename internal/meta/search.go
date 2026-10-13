package meta

import (
	"sort"
	"strings"

	"github.com/sahilm/fuzzy"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// ItemKind is what a search item is. The order breaks ties between equally
// good matches.
type ItemKind int

const (
	ItemTable ItemKind = iota
	ItemView
	ItemRoutine
	ItemColumn
	ItemSchema
	ItemDatabase
)

func (k ItemKind) String() string {
	return [...]string{"table", "view", "routine", "column", "schema", "database"}[k]
}

// Item is one searchable object.
type Item struct {
	Kind    ItemKind
	Conn    string
	NS      db.Namespace
	Table   string // tables, views and columns: the table
	Name    string
	Detail  string     // column type; routine kind and arguments
	Routine db.Routine // ItemRoutine only
	Path    string     // what queries match: "users", "users.email", "add_one", "public", "app"
}

// Hit is an item that matches a query.
type Hit struct {
	Item
	Score int
}

func buildItems(k Key, c db.Catalog) []Item {
	out := make([]Item, 0, len(c.Databases)+len(c.Namespaces)+len(c.Tables)+len(c.Columns)+len(c.Routines))
	for _, d := range c.Databases {
		out = append(out, Item{Kind: ItemDatabase, Conn: k.Conn, NS: db.Namespace{Database: d}, Name: d, Path: d})
	}
	for _, ns := range c.Namespaces {
		if ns.Schema != "" {
			out = append(out, Item{Kind: ItemSchema, Conn: k.Conn, NS: ns, Name: ns.Schema, Path: ns.Schema})
		}
	}
	for _, t := range c.Tables {
		kind := ItemTable
		if t.Kind == db.TableKindView {
			kind = ItemView
		}
		out = append(out, Item{Kind: kind, Conn: k.Conn, NS: t.Namespace, Table: t.Name, Name: t.Name, Path: t.Name})
	}
	for _, col := range c.Columns {
		out = append(out, Item{
			Kind: ItemColumn, Conn: k.Conn, NS: col.Table.Namespace, Table: col.Table.Name,
			Name: col.Name, Detail: col.Type, Path: col.Table.Name + "." + col.Name,
		})
	}
	for _, r := range c.Routines {
		out = append(out, Item{
			Kind: ItemRoutine, Conn: k.Conn, NS: r.Namespace, Name: r.Name,
			Detail: routineDetail(r), Routine: r, Path: r.Name,
		})
	}
	return out
}

func routineDetail(r db.Routine) string {
	kind := "function"
	if r.Kind == db.RoutineProcedure {
		kind = "procedure"
	}
	if r.Args == "" {
		return kind
	}
	return kind + "(" + r.Args + ")"
}

// source presents the items of several snapshots as one fuzzy.Source
// without copying them.
type source struct {
	snaps  []*Snapshot
	starts []int
	n      int
}

func newSource(snaps []*Snapshot) source {
	s := source{snaps: snaps, starts: make([]int, len(snaps))}
	for i, sn := range snaps {
		s.starts[i] = s.n
		s.n += len(sn.items)
	}
	return s
}

func (s source) item(i int) *Item {
	k := sort.Search(len(s.starts), func(j int) bool { return s.starts[j] > i }) - 1
	return &s.snaps[k].items[i-s.starts[k]]
}

func (s source) String(i int) string { return s.item(i).Path }
func (s source) Len() int            { return s.n }

// Search matches query (fuzzy, ignoring case) against the items of snaps
// and returns at most limit hits, best first. Every PostgreSQL catalog of
// a connection lists all of its databases, so each database appears once.
func Search(snaps []*Snapshot, query string, limit int) []Hit {
	query = strings.TrimSpace(query)
	if query == "" || limit <= 0 {
		return nil
	}
	src := newSource(snaps)
	ms := fuzzy.FindFromNoSort(query, src)
	sort.SliceStable(ms, func(i, j int) bool {
		a, b := ms[i], ms[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if ka, kb := src.item(a.Index).Kind, src.item(b.Index).Kind; ka != kb {
			return ka < kb
		}
		return len(a.Str) < len(b.Str)
	})
	out := []Hit{}
	seen := map[string]bool{}
	for _, m := range ms {
		it := src.item(m.Index)
		if it.Kind == ItemDatabase {
			id := it.Conn + "\x00" + it.Name
			if seen[id] {
				continue
			}
			seen[id] = true
		}
		out = append(out, Hit{Item: *it, Score: m.Score})
		if len(out) == limit {
			break
		}
	}
	return out
}
