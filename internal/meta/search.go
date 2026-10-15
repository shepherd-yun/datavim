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
	qpath   string     // what dotted queries match: "app.users.email", "shop.public.orders"
}

// parts are the names from the database down to the item.
func (it *Item) parts() []string {
	p := []string{it.NS.Database}
	if it.Kind == ItemDatabase {
		return p
	}
	if it.NS.Schema != "" {
		p = append(p, it.NS.Schema)
	}
	switch it.Kind {
	case ItemTable, ItemView:
		p = append(p, it.Table)
	case ItemColumn:
		p = append(p, it.Table, it.Name)
	case ItemRoutine:
		p = append(p, it.Name)
	}
	return p
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
	for i := range out {
		out[i].qpath = strings.Join(out[i].parts(), ".")
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
	snaps     []*Snapshot
	starts    []int
	n         int
	qualified bool // match the qualified paths
}

func newSource(snaps []*Snapshot, qualified bool) source {
	s := source{snaps: snaps, starts: make([]int, len(snaps)), qualified: qualified}
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

func (s source) String(i int) string {
	if s.qualified {
		return s.item(i).qpath
	}
	return s.item(i).Path
}
func (s source) Len() int { return s.n }

// Search matches query (fuzzy, ignoring case) against the items of snaps
// and returns at most limit hits, best first. Every PostgreSQL catalog of
// a connection lists all of its databases, so each database appears once.
//
// A dotted query is matched by segment: the last one against the item's
// name, the others, in order, against its database, schema and table
// (any of which may be left out), so "app.users.email" and "us.em" find
// the column email of app.users and "users." lists its columns.
func Search(snaps []*Snapshot, query string, limit int) []Hit {
	query = strings.TrimSpace(query)
	if query == "" || limit <= 0 {
		return nil
	}
	var segs []string
	if strings.Contains(query, ".") {
		segs = strings.Split(strings.ToLower(query), ".")
	}
	src := newSource(snaps, segs != nil)
	ms := fuzzy.FindFromNoSort(query, src)
	if segs != nil {
		kept := ms[:0]
		for _, m := range ms {
			if matchSegments(segs, src.item(m.Index).parts()) {
				kept = append(kept, m)
			}
		}
		ms = kept
	}
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

// matchSegments reports whether the last segment fuzzily matches the last
// part and the others match earlier parts in order; an empty segment
// matches any one part.
func matchSegments(segs, parts []string) bool {
	last := len(segs) - 1
	if !subsequence(segs[last], parts[len(parts)-1]) {
		return false
	}
	j := len(parts) - 2
	for i := last - 1; i >= 0; i-- {
		for j >= 0 && !subsequence(segs[i], parts[j]) {
			j--
		}
		if j < 0 {
			return false
		}
		j--
	}
	return true
}

// subsequence reports whether the runes of seg (lower case) appear in s in
// order, ignoring case.
func subsequence(seg, s string) bool {
	s = strings.ToLower(s)
	for _, r := range seg {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+len(string(r)):]
	}
	return true
}
