package ui

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/session"
)

// fakeValue is one key of fakeRedis; typ picks the field that holds it.
type fakeValue struct {
	typ    string
	str    string
	hash   [][2]string
	list   []string
	set    []string
	zset   []rdb.ZMember
	stream []rdb.StreamEntry
	ttl    time.Duration // 0 is no expiry
}

type fakeScanCall struct {
	db     int
	cursor uint64
	match  string
	count  int64
}

// fakeRedis is an in-memory Redis shared by the clients fakeBackend.Redis
// hands out; it records every call.
type fakeRedis struct {
	mu     sync.Mutex
	dbs    map[int]map[string]fakeValue
	sorted map[int][]string
	scans  []fakeScanCall
	calls  []string // "Method key" for the per-key reads
	getMax []int64
	match  []string // HashPage / SetPage match arguments
	block  chan struct{}
}

func newFakeRedis() *fakeRedis {
	return &fakeRedis{dbs: map[int]map[string]fakeValue{}, sorted: map[int][]string{}}
}

func (r *fakeRedis) put(n int, key string, v fakeValue) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dbs[n] == nil {
		r.dbs[n] = map[string]fakeValue{}
	}
	r.dbs[n][key] = v
	delete(r.sorted, n)
}

func (r *fakeRedis) del(n int, key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.dbs[n], key)
	delete(r.sorted, n)
}

// fill adds count string keys prefix0 … prefix<count-1>.
func (r *fakeRedis) fill(n int, prefix string, count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dbs[n] == nil {
		r.dbs[n] = map[string]fakeValue{}
	}
	for i := range count {
		r.dbs[n][fmt.Sprint(prefix, i)] = fakeValue{typ: "string", str: "v"}
	}
	delete(r.sorted, n)
}

func (r *fakeRedis) scanCalls() []fakeScanCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.scans)
}

func (r *fakeRedis) keyCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// fakeRedisClient is one db of fakeRedis.
type fakeRedisClient struct {
	r  *fakeRedis
	db int
}

func (c *fakeRedisClient) Version() string { return "7.2.0" }
func (c *fakeRedisClient) Close() error    { return nil }

func (c *fakeRedisClient) DBSize(context.Context) (int64, error) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	return int64(len(c.r.dbs[c.db])), nil
}

func (c *fakeRedisClient) Databases(context.Context) ([]rdb.DBInfo, error) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	out := make([]rdb.DBInfo, 16)
	for i := range out {
		out[i] = rdb.DBInfo{Index: i, Keys: int64(len(c.r.dbs[i]))}
	}
	return out, nil
}

// Scan pages through the keys in byte order: the cursor is an offset, and
// like the real SCAN, MATCH filters the count keys it walked.
func (c *fakeRedisClient) Scan(ctx context.Context, cursor uint64, match string, count int64) (rdb.ScanPage, error) {
	c.r.mu.Lock()
	c.r.scans = append(c.r.scans, fakeScanCall{db: c.db, cursor: cursor, match: match, count: count})
	block := c.r.block
	c.r.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return rdb.ScanPage{}, ctx.Err()
		}
	}
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	keys, ok := c.r.sorted[c.db]
	if !ok {
		for k := range c.r.dbs[c.db] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		c.r.sorted[c.db] = keys
	}
	end := min(int(cursor)+int(count), len(keys))
	var page rdb.ScanPage
	for _, k := range keys[min(int(cursor), len(keys)):end] {
		if ok, _ := path.Match(match, k); match == "" || ok {
			page.Keys = append(page.Keys, rdb.KeyEntry{Key: k, Type: c.r.dbs[c.db][k].typ})
		}
	}
	if end < len(keys) {
		page.Cursor = uint64(end)
	}
	return page, nil
}

func (c *fakeRedisClient) get(method, key string) (fakeValue, bool) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	c.r.calls = append(c.r.calls, method+" "+key)
	v, ok := c.r.dbs[c.db][key]
	return v, ok
}

func (c *fakeRedisClient) KeyInfo(_ context.Context, key string) (rdb.KeyInfo, error) {
	v, ok := c.get("KeyInfo", key)
	if !ok {
		return rdb.KeyInfo{}, nil
	}
	info := rdb.KeyInfo{Exists: true, Type: v.typ, TTL: -1, Encoding: "listpack", Memory: 1234}
	if v.ttl > 0 {
		info.TTL = v.ttl
	}
	switch v.typ {
	case "string":
		info.Len, info.Encoding = int64(len(v.str)), "raw"
	case "hash":
		info.Len = int64(len(v.hash))
	case "list":
		info.Len = int64(len(v.list))
	case "set":
		info.Len = int64(len(v.set))
	case "zset":
		info.Len = int64(len(v.zset))
	case "stream":
		info.Len = int64(len(v.stream))
	default:
		info.Len = -1
	}
	return info, nil
}

func (c *fakeRedisClient) GetString(_ context.Context, key string, maxBytes int64) (string, error) {
	v, _ := c.get("GetString", key)
	c.r.mu.Lock()
	c.r.getMax = append(c.r.getMax, maxBytes)
	c.r.mu.Unlock()
	return v.str[:min(int64(len(v.str)), maxBytes)], nil
}

func matches(match, s string) bool {
	ok, _ := path.Match(match, s)
	return match == "" || ok
}

func (c *fakeRedisClient) HashPage(_ context.Context, key string, cursor uint64, match string, count int64) ([][2]string, uint64, error) {
	v, _ := c.get("HashPage", key)
	c.r.mu.Lock()
	c.r.match = append(c.r.match, match)
	c.r.mu.Unlock()
	end := min(int(cursor)+int(count), len(v.hash))
	var out [][2]string
	for _, fv := range v.hash[min(int(cursor), len(v.hash)):end] {
		if matches(match, fv[0]) {
			out = append(out, fv)
		}
	}
	if end == len(v.hash) {
		end = 0
	}
	return out, uint64(end), nil
}

func (c *fakeRedisClient) ListPage(_ context.Context, key string, start, count int64) ([]string, error) {
	v, _ := c.get("ListPage", key)
	return v.list[min(int(start), len(v.list)):min(int(start+count), len(v.list))], nil
}

func (c *fakeRedisClient) SetPage(_ context.Context, key string, cursor uint64, match string, count int64) ([]string, uint64, error) {
	v, _ := c.get("SetPage", key)
	c.r.mu.Lock()
	c.r.match = append(c.r.match, match)
	c.r.mu.Unlock()
	end := min(int(cursor)+int(count), len(v.set))
	var out []string
	for _, m := range v.set[min(int(cursor), len(v.set)):end] {
		if matches(match, m) {
			out = append(out, m)
		}
	}
	if end == len(v.set) {
		end = 0
	}
	return out, uint64(end), nil
}

func (c *fakeRedisClient) ZSetPage(_ context.Context, key string, start, count int64) ([]rdb.ZMember, error) {
	v, _ := c.get("ZSetPage", key)
	return v.zset[min(int(start), len(v.zset)):min(int(start+count), len(v.zset))], nil
}

func (c *fakeRedisClient) StreamPage(_ context.Context, key, after string, count int64) ([]rdb.StreamEntry, error) {
	v, _ := c.get("StreamPage", key)
	i := 0
	if after != "" {
		i = slices.IndexFunc(v.stream, func(e rdb.StreamEntry) bool { return e.ID == after }) + 1
	}
	return v.stream[i:min(i+int(count), len(v.stream))], nil
}

// addRedis adds a redis connection backed by f.redis (created on first use).
func (f *fakeBackend) addRedis(cfg config.Connection) *fakeRedis {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cfg.Driver == "" {
		cfg.Driver = "redis"
	}
	if cfg.Env == "" {
		cfg.Env = config.EnvDev
	}
	if f.redis == nil {
		f.redis = newFakeRedis()
	}
	f.conns = append(f.conns, cfg)
	return f.redis
}

func (f *fakeBackend) Redis(_ context.Context, name string, n int) (rdb.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.redisOpens = append(f.redisOpens, name)
	if f.lostConns > 0 {
		f.lostConns--
		return nil, fmt.Errorf("%w: connection reset by peer", db.ErrConnLost)
	}
	if f.connErr != nil {
		return nil, f.connErr
	}
	if f.needPassword {
		return nil, session.ErrPasswordRequired
	}
	var cfg config.Connection
	for _, c := range f.conns {
		if c.Name == name {
			cfg = c
		}
	}
	if cfg.Driver != "redis" || f.redis == nil {
		return nil, fmt.Errorf("connection %q is not a redis connection", name)
	}
	if n < 0 {
		n = config.RedisDB(cfg)
	}
	return &fakeRedisClient{r: f.redis, db: n}, nil
}

