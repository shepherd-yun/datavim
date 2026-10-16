package ui

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/redit"
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

	applied  []redit.Plan       // every Apply, in order
	applyErr error              // what Apply returns
	onApply  func(p redit.Plan) // runs after an Apply is recorded, unlocked
	unlinks  [][]string         // the keys of every Unlink
	version  string             // Version(); "" is 7.2.0
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

func (c *fakeRedisClient) Version() string {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	if c.r.version != "" {
		return c.r.version
	}
	return "7.2.0"
}
func (c *fakeRedisClient) Close() error { return nil }

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
		v, exists := c.r.dbs[c.db][k] // Unlink keeps the order, so cursors stay valid
		if ok, _ := path.Match(match, k); exists && (match == "" || ok) {
			page.Keys = append(page.Keys, rdb.KeyEntry{Key: k, Type: v.typ})
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

func (c *fakeRedisClient) Apply(_ context.Context, p redit.Plan) error {
	c.r.mu.Lock()
	c.r.applied = append(c.r.applied, p)
	c.r.calls = append(c.r.calls, "Apply "+p.Key)
	err, hook := c.r.applyErr, c.r.onApply
	c.r.mu.Unlock()
	if hook != nil {
		hook(p)
	}
	return err
}

func (r *fakeRedis) appliedPlans() []redit.Plan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.applied)
}

func (c *fakeRedisClient) Expire(_ context.Context, key string, ttl time.Duration) (bool, error) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	c.r.calls = append(c.r.calls, fmt.Sprintf("Expire %s %v", key, ttl))
	v, ok := c.r.dbs[c.db][key]
	if !ok {
		return false, nil
	}
	v.ttl = max(ttl, 0)
	c.r.dbs[c.db][key] = v
	return true, nil
}

func (c *fakeRedisClient) Rename(_ context.Context, from, to string, overwrite bool) (bool, error) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	c.r.calls = append(c.r.calls, fmt.Sprintf("Rename %s %s %v", from, to, overwrite))
	data := c.r.dbs[c.db]
	v, ok := data[from]
	if !ok {
		return false, fmt.Errorf("ERR no such key")
	}
	if _, exists := data[to]; exists && !overwrite {
		return false, nil
	}
	delete(data, from)
	data[to] = v
	delete(c.r.sorted, c.db)
	return true, nil
}

func (c *fakeRedisClient) Unlink(_ context.Context, keys ...string) (int64, error) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	c.r.unlinks = append(c.r.unlinks, slices.Clone(keys))
	n := int64(0)
	for _, k := range keys {
		if _, ok := c.r.dbs[c.db][k]; ok {
			delete(c.r.dbs[c.db], k)
			n++
		}
	}
	return n, nil
}

func (c *fakeRedisClient) ScanKeys(ctx context.Context, cursor uint64, match string, count int64) ([]string, uint64, error) {
	page, err := c.Scan(ctx, cursor, match, count)
	keys := make([]string, len(page.Keys))
	for i, k := range page.Keys {
		keys[i] = k.Key
	}
	return keys, page.Cursor, err
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

// fakeRedisSession is a console session on fakeRedis. answer, when set,
// replies first (ok false falls through to the built-in commands). block
// makes every Do wait for ctx; lost makes Do and Ping fail like a dropped
// connection.
type fakeRedisSession struct {
	r *fakeRedis

	mu      sync.Mutex
	db      int
	inMulti bool
	queued  [][]string
	answer  func(args []string) (rdb.Reply, bool)
	block   bool
	lost    bool
	closed  bool
	dos     [][]string
	pings   int
}

func (s *fakeRedisSession) DB() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db
}

func (s *fakeRedisSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *fakeRedisSession) setLost() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lost = true
}

func (s *fakeRedisSession) setAnswer(fn func(args []string) (rdb.Reply, bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answer = fn
}

// commands is every Do so far, as "SET a 1" lines.
func (s *fakeRedisSession) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, d := range s.dos {
		out = append(out, strings.Join(d, " "))
	}
	return out
}

func (s *fakeRedisSession) pingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pings
}

func (s *fakeRedisSession) Ping(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pings++
	if s.lost || s.closed {
		return fmt.Errorf("%w: broken pipe", db.ErrConnLost)
	}
	return nil
}

func (s *fakeRedisSession) Do(ctx context.Context, args ...string) (rdb.Reply, error) {
	s.mu.Lock()
	s.dos = append(s.dos, slices.Clone(args))
	if s.lost || s.closed {
		s.mu.Unlock()
		return rdb.Reply{}, fmt.Errorf("%w: broken pipe", db.ErrConnLost)
	}
	block := s.block || len(args) == 3 && strings.EqualFold(args[0], "BLPOP") && args[2] == "0"
	answer := s.answer
	s.mu.Unlock()
	if block {
		<-ctx.Done()
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		return rdb.Reply{}, ctx.Err()
	}
	if answer != nil {
		if r, ok := answer(args); ok {
			return r, nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cmd := strings.ToUpper(args[0])
	if s.inMulti && cmd != "EXEC" && cmd != "DISCARD" && cmd != "MULTI" {
		s.queued = append(s.queued, args)
		return str("QUEUED"), nil
	}
	switch cmd {
	case "MULTI":
		s.inMulti, s.queued = true, nil
		return str("OK"), nil
	case "EXEC":
		if !s.inMulti {
			return rdb.Reply{Kind: rdb.KindError, Str: "ERR EXEC without MULTI"}, nil
		}
		s.inMulti = false
		out := rdb.Reply{Kind: rdb.KindArray}
		for _, q := range s.queued {
			out.Elems = append(out.Elems, s.run(q))
		}
		s.queued = nil
		return out, nil
	case "DISCARD":
		s.inMulti, s.queued = false, nil
		return str("OK"), nil
	}
	return s.run(args), nil
}

func str(s string) rdb.Reply { return rdb.Reply{Kind: rdb.KindString, Str: s} }

// run answers one command outside MULTI; s.mu is held.
func (s *fakeRedisSession) run(args []string) rdb.Reply {
	r := s.r
	r.mu.Lock()
	defer r.mu.Unlock()
	data := r.dbs[s.db]
	if data == nil {
		data = map[string]fakeValue{}
		r.dbs[s.db] = data
	}
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	switch strings.ToUpper(args[0]) {
	case "PING":
		return str("PONG")
	case "WATCH", "UNWATCH", "AUTH":
		return str("OK")
	case "SELECT":
		n, err := strconv.Atoi(arg(1))
		if err != nil {
			return rdb.Reply{Kind: rdb.KindError, Str: "ERR value is not an integer or out of range"}
		}
		s.db = n
		return str("OK")
	case "GET":
		v, ok := data[arg(1)]
		if !ok {
			return rdb.Reply{Kind: rdb.KindNil}
		}
		if v.typ != "string" {
			return rdb.Reply{Kind: rdb.KindError, Str: "WRONGTYPE Operation against a key holding the wrong kind of value"}
		}
		return str(v.str)
	case "SET":
		data[arg(1)] = fakeValue{typ: "string", str: arg(2)}
		delete(r.sorted, s.db)
		return str("OK")
	case "INCR":
		n, _ := strconv.ParseInt(data[arg(1)].str, 10, 64)
		data[arg(1)] = fakeValue{typ: "string", str: strconv.FormatInt(n+1, 10)}
		return rdb.Reply{Kind: rdb.KindInt, Int: n + 1}
	case "LPUSH":
		if v, ok := data[arg(1)]; ok && v.typ != "list" {
			return rdb.Reply{Kind: rdb.KindError, Str: "WRONGTYPE Operation against a key holding the wrong kind of value"}
		}
		v := data[arg(1)]
		v.typ = "list"
		v.list = append(slices.Clone(args[2:]), v.list...)
		data[arg(1)] = v
		return rdb.Reply{Kind: rdb.KindInt, Int: int64(len(v.list))}
	case "DEL":
		n := 0
		for _, k := range args[1:] {
			if _, ok := data[k]; ok {
				delete(data, k)
				n++
			}
		}
		delete(r.sorted, s.db)
		return rdb.Reply{Kind: rdb.KindInt, Int: int64(n)}
	case "DBSIZE":
		return rdb.Reply{Kind: rdb.KindInt, Int: int64(len(data))}
	case "KEYS", "SCAN":
		match := arg(1)
		if strings.EqualFold(args[0], "SCAN") {
			match = "*"
		}
		keys := rdb.Reply{Kind: rdb.KindArray}
		var names []string
		for k := range data {
			if matches(match, k) {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		for _, k := range names {
			keys.Elems = append(keys.Elems, str(k))
		}
		if strings.EqualFold(args[0], "KEYS") {
			return keys
		}
		return rdb.Reply{Kind: rdb.KindArray, Elems: []rdb.Reply{str("0"), keys}}
	case "HGETALL":
		out := rdb.Reply{Kind: rdb.KindMap}
		for _, fv := range data[arg(1)].hash {
			out.Pairs = append(out.Pairs, [2]rdb.Reply{str(fv[0]), str(fv[1])})
		}
		return out
	case "FLUSHDB":
		r.dbs[s.db] = map[string]fakeValue{}
		delete(r.sorted, s.db)
		return str("OK")
	}
	return rdb.Reply{Kind: rdb.KindError, Str: "ERR unknown command '" + args[0] + "'"}
}

func (f *fakeBackend) RedisSession(_ context.Context, name string, n int) (rdb.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionDBs = append(f.sessionDBs, n)
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
	s := &fakeRedisSession{r: f.redis, db: n}
	f.sessions = append(f.sessions, s)
	return s, nil
}

// lastSession is the newest session RedisSession handed out.
func (f *fakeBackend) lastSession() *fakeRedisSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sessions) == 0 {
		return nil
	}
	return f.sessions[len(f.sessions)-1]
}

func (f *fakeBackend) sessionCalls() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sessionDBs)
}
