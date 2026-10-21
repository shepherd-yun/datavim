package meta

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
)

var (
	appNS  = db.Namespace{Database: "app"}
	logsNS = db.Namespace{Database: "Logs"}
)

func catalog() db.Catalog {
	users := db.TableRef{Namespace: appNS, Name: "users"}
	return db.Catalog{
		Databases:  []string{"app", "Logs"},
		Namespaces: []db.Namespace{appNS, logsNS},
		Tables: []db.CatalogTable{
			{Namespace: appNS, Name: "MixedCase"},
			{Namespace: appNS, Name: "users"},
			{Namespace: appNS, Name: "active", Kind: db.TableKindView},
			{Namespace: logsNS, Name: "events"},
		},
		Columns: []db.CatalogColumn{
			{Table: users, Name: "id", Type: "bigint"},
			{Table: users, Name: "email", Type: "text"},
			{Table: db.TableRef{Namespace: appNS, Name: "MixedCase"}, Name: "Id", Type: "int"},
		},
		Routines: []db.Routine{{Namespace: appNS, Name: "add_one"}},
	}
}

func TestSnapshotLookups(t *testing.T) {
	s := NewSnapshot(Key{Conn: "local"}, catalog(), time.Unix(0, 0))
	if got := len(s.Tables(appNS)); got != 3 {
		t.Fatalf("app tables = %d", got)
	}
	if got := s.Tables(db.Namespace{Database: "logs"}); len(got) != 1 || got[0].Name != "events" {
		t.Fatalf("namespaces match ignoring case when there is no exact match: %v", got)
	}
	if tb, ok := s.FindTable(appNS, "mixedcase"); !ok || tb.Name != "MixedCase" {
		t.Fatalf("FindTable ignoring case = %v, %v", tb, ok)
	}
	if _, ok := s.FindTable(appNS, "nope"); ok {
		t.Fatal("unknown table found")
	}
	var names []string
	for _, c := range s.Columns(db.TableRef{Namespace: appNS, Name: "USERS"}) {
		names = append(names, c.Name)
	}
	if diff := cmp.Diff([]string{"id", "email"}, names); diff != "" {
		t.Fatalf("columns in order (-want +got):\n%s", diff)
	}
	if s.Columns(db.TableRef{Namespace: appNS, Name: "nope"}) != nil {
		t.Fatal("unknown table has no columns")
	}
}

func TestCacheLifecycle(t *testing.T) {
	c := NewCache()
	k := Key{Conn: "local"}
	if _, st := c.Get(k); st != Missing {
		t.Fatalf("state = %v, want Missing", st)
	}
	gen, ok := c.Start(k)
	if !ok {
		t.Fatal("first Start")
	}
	if _, ok := c.Start(k); ok {
		t.Fatal("one load at a time")
	}
	if _, st := c.Get(k); st != Loading {
		t.Fatalf("state = %v, want Loading", st)
	}
	snap := NewSnapshot(k, catalog(), time.Now())
	if !c.Finish(k, gen, snap, nil) {
		t.Fatal("Finish of the current load")
	}
	if got, st := c.Get(k); st != Ready || got != snap {
		t.Fatalf("Get = %p, %v", got, st)
	}

	gen, _ = c.Start(k)
	boom := errors.New("boom")
	c.Finish(k, gen, nil, boom)
	if got, st := c.Get(k); st != Failed || got != snap || !errors.Is(c.Err(k), boom) {
		t.Fatalf("a failed reload keeps the old snapshot: %p, %v, %v", got, st, c.Err(k))
	}

	c.Forget("local")
	if _, st := c.Get(k); st != Missing {
		t.Fatal("Forget drops the connection")
	}
}

func TestCacheRestartSupersedesRunningLoad(t *testing.T) {
	c := NewCache()
	k := Key{Conn: "pg", Database: "app"}
	old, _ := c.Start(k)
	cur := c.Restart(k)
	if c.Finish(k, old, NewSnapshot(k, db.Catalog{}, time.Now()), nil) {
		t.Fatal("the superseded load is dropped")
	}
	if _, st := c.Get(k); st != Loading {
		t.Fatal("still loading the new generation")
	}
	snap := NewSnapshot(k, catalog(), time.Now())
	if !c.Finish(k, cur, snap, nil) {
		t.Fatal("the current load is kept")
	}
	if got, _ := c.Get(k); got != snap {
		t.Fatal("snapshot of the current generation")
	}
	if c.Finish(Key{Conn: "other"}, 99, snap, nil) {
		t.Fatal("unknown keys are ignored")
	}
}

func TestCacheSnapshots(t *testing.T) {
	c := NewCache()
	for _, k := range []Key{{Conn: "pg", Database: "b"}, {Conn: "pg", Database: "a"}, {Conn: "my"}} {
		gen, _ := c.Start(k)
		c.Finish(k, gen, NewSnapshot(k, db.Catalog{}, time.Now()), nil)
	}
	c.Start(Key{Conn: "pg", Database: "loading"})
	var keys []Key
	for _, s := range c.Snapshots("pg") {
		keys = append(keys, s.Key)
	}
	if diff := cmp.Diff([]Key{{Conn: "pg", Database: "a"}, {Conn: "pg", Database: "b"}}, keys); diff != "" {
		t.Fatalf("pg snapshots (-want +got):\n%s", diff)
	}
	if got := len(c.Snapshots("")); got != 3 {
		t.Fatalf("all snapshots = %d", got)
	}
}

type fakeMeta struct {
	db.MetaReader
	cat db.Catalog
	err error
}

func (f fakeMeta) Catalog(context.Context) (db.Catalog, error) { return f.cat, f.err }

func TestLoad(t *testing.T) {
	k := Key{Conn: "local"}
	s, err := Load(context.Background(), fakeMeta{cat: catalog()}, k)
	if err != nil || s.Key != k || len(s.Catalog.Tables) != 4 || s.LoadedAt.IsZero() {
		t.Fatalf("Load = %+v, %v", s, err)
	}
	if _, err := Load(context.Background(), fakeMeta{err: errors.New("denied")}, k); err == nil {
		t.Fatal("errors are returned")
	}
}

func TestCacheFailed(t *testing.T) {
	c := NewCache()
	bad, good, other := Key{Conn: "pg", Database: "a"}, Key{Conn: "pg", Database: "b"}, Key{Conn: "my"}
	for _, k := range []Key{bad, good, other} {
		gen, _ := c.Start(k)
		var err error
		if k != good {
			err = errors.New("password required")
		}
		c.Finish(k, gen, nil, err)
	}
	if diff := cmp.Diff([]Key{bad}, c.Failed("pg")); diff != "" {
		t.Fatalf("failed keys (-want +got):\n%s", diff)
	}
	c.Start(bad)
	if got := c.Failed("pg"); len(got) != 0 {
		t.Fatalf("a key being reloaded is not failed: %v", got)
	}
}
