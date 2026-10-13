package meta

import (
	"context"
	"sort"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// State is the load state of one catalog.
type State int

const (
	Missing State = iota // never loaded (or forgotten)
	Loading              // a load is running; an older snapshot may be shown meanwhile
	Ready
	Failed // the last load failed; an older snapshot may still be there
)

type entry struct {
	snap    *Snapshot
	loading int // generation of the running load; 0 = none
	err     error
}

// Cache holds the catalogs of the connections in use. It belongs to the UI
// goroutine and is not safe for concurrent use; loads run in commands (Load)
// and report back through Finish.
type Cache struct {
	entries map[Key]*entry
	gen     int
}

// NewCache returns an empty cache.
func NewCache() *Cache { return &Cache{entries: map[Key]*entry{}} }

func (c *Cache) entry(k Key) *entry {
	e := c.entries[k]
	if e == nil {
		e = &entry{}
		c.entries[k] = e
	}
	return e
}

// Get returns the snapshot of k (possibly stale while a reload runs) and
// its state.
func (c *Cache) Get(k Key) (*Snapshot, State) {
	e := c.entries[k]
	switch {
	case e == nil:
		return nil, Missing
	case e.loading != 0:
		return e.snap, Loading
	case e.err != nil:
		return e.snap, Failed
	case e.snap == nil:
		return nil, Missing
	}
	return e.snap, Ready
}

// Err is the error of the last failed load of k.
func (c *Cache) Err(k Key) error {
	if e := c.entries[k]; e != nil {
		return e.err
	}
	return nil
}

// Start marks k as loading and returns the generation to pass to Finish.
// ok is false when a load of k is already running.
func (c *Cache) Start(k Key) (gen int, ok bool) {
	if e := c.entries[k]; e != nil && e.loading != 0 {
		return 0, false
	}
	return c.Restart(k), true
}

// Restart starts a new load of k even if one is running; the running one's
// result will be dropped (it may predate a change).
func (c *Cache) Restart(k Key) int {
	c.gen++
	c.entry(k).loading = c.gen
	return c.gen
}

// Finish records the result of load gen of k and reports whether it was
// the current load. A failure keeps the previous snapshot.
func (c *Cache) Finish(k Key, gen int, s *Snapshot, err error) bool {
	e := c.entries[k]
	if e == nil || e.loading != gen {
		return false
	}
	e.loading = 0
	if err != nil {
		e.err = err
		return true
	}
	e.snap, e.err = s, nil
	return true
}

// Forget drops every catalog of conn; loads still running are dropped too.
func (c *Cache) Forget(conn string) {
	for k := range c.entries {
		if k.Conn == conn {
			delete(c.entries, k)
		}
	}
}

// Snapshots returns the loaded snapshots of conn ("" for every connection),
// sorted by key.
func (c *Cache) Snapshots(conn string) []*Snapshot {
	var out []*Snapshot
	for k, e := range c.entries {
		if e.snap != nil && (conn == "" || k.Conn == conn) {
			out = append(out, e.snap)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Key, out[j].Key
		if a.Conn != b.Conn {
			return a.Conn < b.Conn
		}
		return a.Database < b.Database
	})
	return out
}

// Load reads the catalog for k.
func Load(ctx context.Context, mr db.MetaReader, k Key) (*Snapshot, error) {
	c, err := mr.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	return NewSnapshot(k, c, time.Now()), nil
}

// Failed lists the keys of conn whose last load failed and that are not
// loading again, sorted.
func (c *Cache) Failed(conn string) []Key {
	var out []Key
	for k, e := range c.entries {
		if k.Conn == conn && e.err != nil && e.loading == 0 {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Database < out[j].Database })
	return out
}
