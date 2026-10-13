package meta

import (
	"fmt"
	"testing"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func snap(k Key, c db.Catalog) *Snapshot { return NewSnapshot(k, c, time.Now()) }

func TestSearchFindsEveryKind(t *testing.T) {
	snaps := []*Snapshot{snap(Key{Conn: "local"}, catalog())}
	for _, tc := range []struct {
		query, path string
		kind        ItemKind
		detail      string
	}{
		{"users", "users", ItemTable, ""},
		{"active", "active", ItemView, ""},
		{"users.em", "users.email", ItemColumn, "text"},
		{"add", "add_one", ItemRoutine, "function"},
		{"Logs", "Logs", ItemDatabase, ""},
	} {
		hits := Search(snaps, tc.query, 10)
		if len(hits) == 0 {
			t.Errorf("%q: no hits", tc.query)
			continue
		}
		h := hits[0]
		if h.Path != tc.path || h.Kind != tc.kind || h.Detail != tc.detail || h.Conn != "local" {
			t.Errorf("%q: first hit %+v", tc.query, h.Item)
		}
	}
	col := Search(snaps, "users.email", 1)[0]
	if col.Table != "users" || col.Name != "email" || col.NS != appNS {
		t.Errorf("column hit = %+v", col.Item)
	}
	fn := Search(snaps, "add_one", 1)[0]
	if fn.Routine.Name != "add_one" || fn.NS != appNS {
		t.Errorf("routine hit = %+v", fn.Item)
	}
	if Search(snaps, "   ", 10) != nil || len(Search(snaps, "zzzz", 10)) != 0 {
		t.Error("blank and unmatched queries find nothing")
	}
}

func TestSearchPrefersTablesOnTies(t *testing.T) {
	c := db.Catalog{Tables: []db.CatalogTable{
		{Namespace: appNS, Name: "stats", Kind: db.TableKindView},
		{Namespace: logsNS, Name: "stats"},
	}}
	hits := Search([]*Snapshot{snap(Key{Conn: "local"}, c)}, "stats", 10)
	if len(hits) != 2 || hits[0].Kind != ItemTable || hits[1].Kind != ItemView {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestSearchSchemasOfPostgres(t *testing.T) {
	pub := db.Namespace{Database: "app", Schema: "public"}
	c := db.Catalog{Databases: []string{"app"}, Namespaces: []db.Namespace{pub}}
	hits := Search([]*Snapshot{snap(Key{Conn: "pg", Database: "app"}, c)}, "public", 10)
	if len(hits) != 1 || hits[0].Kind != ItemSchema || hits[0].NS != pub {
		t.Fatalf("hits = %+v", hits)
	}
}

func bigCatalog(tables, cols int) db.Catalog {
	ns := db.Namespace{Database: "app", Schema: "public"}
	c := db.Catalog{Databases: []string{"app", "other"}, Namespaces: []db.Namespace{ns}}
	for i := range tables {
		ref := db.TableRef{Namespace: ns, Name: fmt.Sprintf("t%04d", i)}
		c.Tables = append(c.Tables, db.CatalogTable{Namespace: ns, Name: ref.Name})
		for j := range cols {
			c.Columns = append(c.Columns, db.CatalogColumn{Table: ref, Name: fmt.Sprintf("c%02d", j), Type: "int"})
		}
	}
	return c
}

func TestSearchLargeCatalogIsBoundedAndDeduped(t *testing.T) {
	a := snap(Key{Conn: "pg", Database: "app"}, bigCatalog(2000, 20))
	b := snap(Key{Conn: "pg", Database: "other"}, db.Catalog{Databases: []string{"app", "other"}})
	snaps := []*Snapshot{a, b}
	if got := len(Search(snaps, "c07", 200)); got != 200 {
		t.Fatalf("hits = %d, want the limit", got)
	}
	n := 0
	for _, h := range Search(snaps, "other", 50) {
		if h.Kind == ItemDatabase && h.Name == "other" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("database other listed %d times", n)
	}
}

func BenchmarkSearch(b *testing.B) {
	snaps := []*Snapshot{snap(Key{Conn: "pg", Database: "app"}, bigCatalog(5000, 40))}
	for b.Loop() {
		Search(snaps, "t12c3", searchBenchLimit)
	}
}

const searchBenchLimit = 200
