//go:build integration

package rdb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/edit"
	"github.com/shepherd-yun/sqlvim/internal/redit"
)

var images = []string{"redis:7", "redis:5"}

// server is one started container; servers are shared by the tests and
// stopped in TestMain.
type server struct {
	host string
	port int
}

var (
	serversMu sync.Mutex
	servers   = map[string]server{}
	stops     []func()
)

func TestMain(m *testing.M) {
	code := m.Run()
	for _, stop := range stops {
		stop()
	}
	os.Exit(code)
}

// start returns a container of image started with the extra redis-server
// args, reusing one already started with the same ones.
func start(t *testing.T, image string, args ...string) server {
	t.Helper()
	serversMu.Lock()
	defer serversMu.Unlock()
	id := image + " " + strings.Join(args, " ")
	if s, ok := servers[id]; ok {
		return s
	}
	ctx := context.Background()
	var opts []testcontainers.ContainerCustomizer
	if len(args) > 0 {
		opts = append(opts, testcontainers.WithCmd(append([]string{"redis-server"}, args...)...))
	}
	ctr, err := tcredis.Run(ctx, image, opts...)
	if err != nil {
		if ctr != nil {
			_ = ctr.Terminate(ctx)
		}
		t.Fatal(err)
	}
	stops = append(stops, func() { _ = ctr.Terminate(context.Background()) })
	endpoint, err := ctr.PortEndpoint(ctx, "6379/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, _ := net.SplitHostPort(endpoint)
	port, _ := strconv.Atoi(portStr)
	s := server{host: host, port: port}
	servers[id] = s
	return s
}

// raw is a plain go-redis client on db n, for writing fixtures. The db is
// flushed first.
func raw(t *testing.T, s server, n int, password string) *redis.Client {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: net.JoinHostPort(s.host, strconv.Itoa(s.port)), DB: n, Password: password})
	t.Cleanup(func() { _ = c.Close() })
	if err := c.FlushDB(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	return c
}

func open(t *testing.T, s server, cfg Config) Client {
	t.Helper()
	cfg.Host, cfg.Port = s.host, s.port
	c, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func eachImage(t *testing.T, fn func(t *testing.T, image string)) {
	for _, image := range images {
		t.Run(image, func(t *testing.T) { fn(t, image) })
	}
}

func TestOpenAndVersion(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		c := open(t, start(t, image), Config{})
		want := strings.TrimPrefix(image, "redis:") + "."
		if v := c.Version(); !strings.HasPrefix(v, want) {
			t.Fatalf("Version() = %q, want %s…", v, want)
		}
	})
}

func TestAuth(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		s := start(t, image, "--requirepass", "secret")
		ctx := context.Background()
		_, err := Open(ctx, Config{Host: s.host, Port: s.port})
		if !errors.Is(err, ErrAuthRequired) {
			t.Fatalf("no password: err = %v", err)
		}
		open(t, s, Config{Password: "secret"})
		_, err = Open(ctx, Config{Host: s.host, Port: s.port, Password: "hunter2"})
		if err == nil || errors.Is(err, ErrAuthRequired) || strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("wrong password: err = %v", err)
		}
	})
}

func TestDatabases(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r0, r5 := raw(t, s, 0, ""), raw(t, s, 5, "")
		for i := range 3 {
			r0.Set(ctx, fmt.Sprint("k", i), "v", 0)
		}
		r5.Set(ctx, "k", "v", 0)
		dbs, err := open(t, s, Config{}).Databases(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(dbs) != 16 || dbs[0] != (DBInfo{0, 3}) || dbs[5] != (DBInfo{5, 1}) || dbs[1] != (DBInfo{1, 0}) {
			t.Fatalf("dbs = %v", dbs)
		}

		s = start(t, image, "--rename-command", "CONFIG", `""`)
		r0, r5 = raw(t, s, 0, ""), raw(t, s, 5, "")
		r5.Set(ctx, "k", "v", 0)
		dbs, err = open(t, s, Config{DB: 2}).Databases(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(dbs) != 2 || dbs[0] != (DBInfo{2, 0}) || dbs[1] != (DBInfo{5, 1}) {
			t.Fatalf("CONFIG disabled: dbs = %v", dbs)
		}
		_ = r0
	})
}

func scanAll(t *testing.T, c Client, match string) map[string]string {
	t.Helper()
	got := map[string]string{}
	var cursor uint64
	for {
		page, err := c.Scan(context.Background(), cursor, match, 1000)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range page.Keys {
			got[k.Key] = k.Type
		}
		if cursor = page.Cursor; cursor == 0 {
			return got
		}
	}
}

func TestScan(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		p := r.Pipeline()
		for i := range 2500 {
			p.Set(ctx, fmt.Sprint("user:", i), "v", 0)
		}
		for i := range 10 {
			p.HSet(ctx, fmt.Sprint("sess:", i), "f", "v")
		}
		if _, err := p.Exec(ctx); err != nil {
			t.Fatal(err)
		}
		c := open(t, s, Config{})
		got := scanAll(t, c, "")
		if len(got) != 2510 || got["user:7"] != "string" || got["sess:3"] != "hash" {
			t.Fatalf("scanned %d keys, user:7=%q sess:3=%q", len(got), got["user:7"], got["sess:3"])
		}
		got = scanAll(t, c, "sess:*")
		if len(got) != 10 {
			t.Fatalf("sess:* scanned %d keys", len(got))
		}
	})
}

func TestScanDropsVanishedKeys(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		r.Set(ctx, "a", "1", 0)
		r.Set(ctx, "b", "2", 0)
		scanTypeHook = func() { r.Del(ctx, "a") }
		defer func() { scanTypeHook = nil }()
		got := scanAll(t, open(t, s, Config{}), "")
		if _, ok := got["a"]; ok || got["b"] != "string" {
			t.Fatalf("got %v", got)
		}
	})
}

func TestDialer(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		var calls atomic.Int32
		var d net.Dialer
		open(t, start(t, image), Config{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			calls.Add(1)
			return d.DialContext(ctx, network, addr)
		}})
		if calls.Load() == 0 {
			t.Fatal("dialer not used")
		}
	})
}

func TestKeyInfo(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		r.HSet(ctx, "h", "a", "1", "b", "2", "c", "3")
		r.Expire(ctx, "h", 100*time.Second)
		r.Set(ctx, "s", "hello", 0)
		c := open(t, s, Config{})
		info, err := c.KeyInfo(ctx, "h")
		if err != nil {
			t.Fatal(err)
		}
		if !info.Exists || info.Type != "hash" || info.TTL <= 90*time.Second || info.TTL > 100*time.Second ||
			info.Len != 3 || info.Encoding == "" {
			t.Fatalf("hash info = %+v", info)
		}
		info, err = c.KeyInfo(ctx, "s")
		if err != nil || info.TTL != -1 || info.Len != 5 || info.Type != "string" {
			t.Fatalf("string info = %+v, err %v", info, err)
		}
		info, err = c.KeyInfo(ctx, "nope")
		if err != nil || info.Exists {
			t.Fatalf("missing key info = %+v, err %v", info, err)
		}
	})
}

func TestGetStringLimit(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		raw(t, s, 0, "").Set(ctx, "big", strings.Repeat("x", 2<<20), 0)
		v, err := open(t, s, Config{}).GetString(ctx, "big", 1<<20)
		if err != nil || len(v) != 1<<20 {
			t.Fatalf("len = %d, err %v", len(v), err)
		}
	})
}

func TestPages(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		for i := range 25 {
			r.HSet(ctx, "h", fmt.Sprint("f", i), fmt.Sprint("v", i))
			r.RPush(ctx, "l", fmt.Sprint("e", i))
			r.SAdd(ctx, "st", fmt.Sprint("m", i))
			r.ZAdd(ctx, "z", redis.Z{Score: float64(25 - i), Member: fmt.Sprint("m", i)})
		}
		var ids []string
		for i := range 5 {
			id, err := r.XAdd(ctx, &redis.XAddArgs{Stream: "x", Values: []string{"n", fmt.Sprint(i)}}).Result()
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		c := open(t, s, Config{})

		fields := map[string]string{}
		var cursor uint64
		for {
			page, next, err := c.HashPage(ctx, "h", cursor, "", 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, fv := range page {
				fields[fv[0]] = fv[1]
			}
			if cursor = next; cursor == 0 {
				break
			}
		}
		if len(fields) != 25 || fields["f3"] != "v3" {
			t.Fatalf("hash fields = %v", fields)
		}
		page, _, err := c.HashPage(ctx, "h", 0, "f1*", 100)
		if err != nil || len(page) != 11 {
			t.Fatalf("f1* = %v, err %v", page, err)
		}

		items, err := c.ListPage(ctx, "l", 20, 10)
		if err != nil || len(items) != 5 || items[0] != "e20" {
			t.Fatalf("list = %v, err %v", items, err)
		}

		members := map[string]bool{}
		cursor = 0
		for {
			page, next, err := c.SetPage(ctx, "st", cursor, "", 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range page {
				members[m] = true
			}
			if cursor = next; cursor == 0 {
				break
			}
		}
		if len(members) != 25 {
			t.Fatalf("set members = %d", len(members))
		}

		zs, err := c.ZSetPage(ctx, "z", 0, 3)
		if err != nil || len(zs) != 3 || zs[0] != (ZMember{"m24", 1}) || zs[2] != (ZMember{"m22", 3}) {
			t.Fatalf("zset = %v, err %v", zs, err)
		}

		es, err := c.StreamPage(ctx, "x", "", 2)
		if err != nil || len(es) != 2 || es[0].ID != ids[0] || es[0].Fields[0] != [2]string{"n", "0"} {
			t.Fatalf("stream = %v, err %v", es, err)
		}
		es, err = c.StreamPage(ctx, "x", es[1].ID, 2)
		if err != nil || len(es) != 2 || es[0].ID != ids[2] || es[1].ID != ids[3] {
			t.Fatalf("stream after = %v, err %v", es, err)
		}
	})
}

func TestBinaryKeyRoundTrip(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		const key = "\x1f\x8bab\xff"
		raw(t, s, 0, "").Set(ctx, key, "\x00\x01", 0)
		c := open(t, s, Config{})
		if got := scanAll(t, c, ""); got[key] != "string" {
			t.Fatalf("scan = %q", got)
		}
		info, err := c.KeyInfo(ctx, key)
		if err != nil || !info.Exists || info.Len != 2 {
			t.Fatalf("info = %+v, err %v", info, err)
		}
		v, err := c.GetString(ctx, key, 1<<20)
		if err != nil || v != "\x00\x01" {
			t.Fatalf("value = %q, err %v", v, err)
		}
	})
}

func openSession(t *testing.T, s server, cfg Config) Session {
	t.Helper()
	cfg.Host, cfg.Port = s.host, s.port
	sess, err := OpenSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func do(t *testing.T, sess Session, args ...string) Reply {
	t.Helper()
	r, err := sess.Do(context.Background(), args...)
	if err != nil {
		t.Fatalf("Do(%q): %v", args, err)
	}
	return r
}

func TestSessionSelectSticks(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		s := start(t, image)
		raw(t, s, 0, "")
		raw(t, s, 3, "")
		sess := openSession(t, s, Config{})
		do(t, sess, "SET", "a", "0")
		if r := do(t, sess, "SELECT", "3"); r.Str != "OK" || sess.DB() != 3 {
			t.Fatalf("SELECT 3 = %+v, DB() = %d", r, sess.DB())
		}
		do(t, sess, "SET", "a", "3")
		for range 20 {
			if r := do(t, sess, "GET", "a"); r.Str != "3" {
				t.Fatalf("GET a = %+v, want 3", r)
			}
		}
		c := open(t, s, Config{})
		v, err := c.GetString(context.Background(), "a", 10)
		if err != nil || v != "0" {
			t.Fatalf("db0 a = %q, %v", v, err)
		}
		if r := do(t, sess, "SELECT", "99999"); r.Kind != KindError || sess.DB() != 3 {
			t.Fatalf("bad SELECT = %+v, DB() = %d", r, sess.DB())
		}
	})
}

func TestSessionReplyTypes(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		s := start(t, image)
		raw(t, s, 0, "")
		sess := openSession(t, s, Config{})
		resp3 := image != "redis:5"
		do(t, sess, "HSET", "h", "f", "v")
		r := do(t, sess, "HGETALL", "h")
		if resp3 && (r.Kind != KindMap || len(r.Pairs) != 1 || r.Pairs[0][0].Str != "f") ||
			!resp3 && (r.Kind != KindArray || len(r.Elems) != 2) {
			t.Fatalf("HGETALL = %+v", r)
		}
		do(t, sess, "ZADD", "z", "1.5", "m")
		r = do(t, sess, "ZSCORE", "z", "m")
		if resp3 && (r.Kind != KindDouble || r.Float != 1.5) || !resp3 && (r.Kind != KindString || r.Str != "1.5") {
			t.Fatalf("ZSCORE = %+v", r)
		}
		if r := do(t, sess, "GET", "nope"); r.Kind != KindNil {
			t.Fatalf("GET nope = %+v", r)
		}
		if r := do(t, sess, "LPUSH", "h", "x"); r.Kind != KindError || !strings.HasPrefix(r.Str, "WRONGTYPE") {
			t.Fatalf("LPUSH h = %+v", r)
		}
	})
}

func TestSessionMulti(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		s := start(t, image)
		other := raw(t, s, 0, "")
		sess := openSession(t, s, Config{})
		for _, step := range []struct {
			args []string
			want string
		}{{[]string{"MULTI"}, "OK"}, {[]string{"SET", "a", "1"}, "QUEUED"}, {[]string{"INCR", "a"}, "QUEUED"}} {
			if r := do(t, sess, step.args...); r.Str != step.want {
				t.Fatalf("%q = %+v", step.args, r)
			}
		}
		r := do(t, sess, "EXEC")
		if r.Kind != KindArray || len(r.Elems) != 2 || r.Elems[0].Str != "OK" || r.Elems[1].Int != 2 {
			t.Fatalf("EXEC = %+v", r)
		}
		do(t, sess, "WATCH", "a")
		other.Set(context.Background(), "a", "9", 0)
		do(t, sess, "MULTI")
		do(t, sess, "SET", "a", "1")
		if r := do(t, sess, "EXEC"); r.Kind != KindNil {
			t.Fatalf("EXEC after WATCH = %+v", r)
		}
	})
}

func TestSessionBlockingTimeout(t *testing.T) {
	sess := openSession(t, start(t, "redis:7"), Config{})
	began := time.Now()
	r, err := sess.Do(context.Background(), "BLPOP", "empty", "6")
	if err != nil || r.Kind != KindNil {
		t.Fatalf("BLPOP = %+v, %v", r, err)
	}
	if d := time.Since(began); d < 5*time.Second {
		t.Fatalf("BLPOP returned after %v", d)
	}
}

func TestSessionCancelBlocking(t *testing.T) {
	sess := openSession(t, start(t, "redis:7"), Config{})
	do(t, sess, "SELECT", "3")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := sess.Do(ctx, "BLPOP", "empty", "0")
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled BLPOP err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("BLPOP not canceled")
	}
	if _, err := sess.Do(context.Background(), "PING"); !errors.Is(err, db.ErrConnLost) {
		t.Fatalf("PING after cancel = %v", err)
	}
	if err := sess.Ping(context.Background()); !errors.Is(err, db.ErrConnLost) {
		t.Fatalf("Ping after cancel = %v", err)
	}
}

func TestSessionNoSilentRedial(t *testing.T) {
	s := start(t, "redis:7")
	raw(t, s, 3, "")
	sess := openSession(t, s, Config{})
	do(t, sess, "SELECT", "3")
	do(t, sess, "CLIENT", "SETNAME", "victim")
	killer := raw(t, s, 5, "")
	if err := killer.Do(context.Background(), "CLIENT", "KILL", "SKIPME", "yes", "TYPE", "normal").Err(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	_, err := sess.Do(context.Background(), "SET", "x", "1")
	if !errors.Is(err, db.ErrConnLost) {
		t.Fatalf("Do after kill = %v, want ErrConnLost (no silent redial on db0)", err)
	}
}

func TestSessionAuthAndDial(t *testing.T) {
	s := start(t, "redis:7", "--requirepass", "secret")
	_, err := OpenSession(context.Background(), Config{Host: s.host, Port: s.port})
	if !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("no password: err = %v", err)
	}
	var calls atomic.Int32
	var d net.Dialer
	openSession(t, s, Config{Password: "secret", Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		calls.Add(1)
		return d.DialContext(ctx, network, addr)
	}})
	if calls.Load() == 0 {
		t.Fatal("dialer not used")
	}
}

func hashPlan(t *testing.T, key string, rows [][2]string, change func(tb *edit.Table)) redit.Plan {
	t.Helper()
	cols := []db.ColumnInfo{{Name: "field"}, {Name: "value"}}
	var vals [][]db.Value
	for _, r := range rows {
		vals = append(vals, []db.Value{{Text: r[0]}, {Text: r[1]}})
	}
	tb := edit.New(cols, vals, nil)
	change(tb)
	p, err := redit.Build(tb, redit.Target{Key: key, Type: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestApplyHash(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		r.HSet(ctx, "h", "a", "1", "b", "2")
		c := open(t, s, Config{})
		p := hashPlan(t, "h", [][2]string{{"a", "1"}, {"b", "2"}}, func(tb *edit.Table) {
			_ = tb.SetCell(0, 1, db.Value{Text: "9"})
			_ = tb.SetCell(1, 0, db.Value{Text: "bb"})
		})
		if err := c.Apply(ctx, p); err != nil {
			t.Fatal(err)
		}
		got := r.HGetAll(ctx, "h").Val()
		if len(got) != 2 || got["a"] != "9" || got["bb"] != "2" {
			t.Fatalf("hash = %v", got)
		}
	})
}

func TestApplyConflict(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		r.RPush(ctx, "l", "x", "y")
		c := open(t, s, Config{})
		p := redit.Plan{Key: "l", Checks: []redit.Check{
			{Args: []string{"LLEN", "l"}, Expect: redit.ExpectInt, Int: 2},
			{Args: []string{"LINDEX", "l", "0"}, Expect: redit.ExpectValue, Value: "x", What: "element 0 changed"},
		}, Cmds: [][]string{{"LSET", "l", "0", "z"}}}
		r.LSet(ctx, "l", 0, "w")
		err := c.Apply(ctx, p)
		var ce *ChangedError
		if !errors.As(err, &ce) || ce.What != "element 0 changed" {
			t.Fatalf("err = %v", err)
		}
		if v := r.LIndex(ctx, "l", 0).Val(); v != "w" {
			t.Fatalf("LINDEX 0 = %q", v)
		}
	})
}

func TestApplyCheckError(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		r.Set(ctx, "h", "now a string", 0)
		c := open(t, s, Config{})
		p := redit.Plan{Key: "h", Checks: []redit.Check{
			{Args: []string{"HGET", "h", "a"}, Expect: redit.ExpectValue, Value: "1", What: `field "a" changed`},
		}, Cmds: [][]string{{"HSET", "h", "a", "2"}}}
		var ce *ChangedError
		if err := c.Apply(ctx, p); !errors.As(err, &ce) {
			t.Fatalf("a failing check (WRONGTYPE) is a change: %v", err)
		}
	})
}

func TestApplyWatchAbort(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		r.HSet(ctx, "h", "a", "1")
		c := open(t, s, Config{})
		p := hashPlan(t, "h", [][2]string{{"a", "1"}}, func(tb *edit.Table) { _ = tb.SetCell(0, 1, db.Value{Text: "9"}) })
		applyHook = func() { r.HSet(ctx, "h", "a", "5") }
		defer func() { applyHook = nil }()
		var ce *ChangedError
		if err := c.Apply(ctx, p); !errors.As(err, &ce) {
			t.Fatalf("err = %v", err)
		}
		if v := r.HGet(ctx, "h", "a").Val(); v != "5" {
			t.Fatalf("HGET a = %q", v)
		}
	})
}

func TestApplyListDeleteDuplicates(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		r.RPush(ctx, "l", "a", "a", "a")
		c := open(t, s, Config{})
		cols := []db.ColumnInfo{{Name: "index"}, {Name: "value"}}
		tb := edit.New(cols, [][]db.Value{
			{{Text: "0"}, {Text: "a"}}, {{Text: "1"}, {Text: "a"}}, {{Text: "2"}, {Text: "a"}},
		}, nil)
		tb.Delete(0)
		tb.Delete(2)
		p, err := redit.Build(tb, redit.Target{Key: "l", Type: "list", Len: 3, Tomb: fmt.Sprintf("sqlvim:deleted:%016x", time.Now().UnixNano())})
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Apply(ctx, p); err != nil {
			t.Fatal(err)
		}
		if got := r.LRange(ctx, "l", 0, -1).Val(); len(got) != 1 || got[0] != "a" {
			t.Fatalf("list = %q", got)
		}
	})
}

func TestApplyStringKeepsTTL(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		r.Set(ctx, "s", "v", 100*time.Second)
		r.Set(ctx, "p", "v", 0)
		c := open(t, s, Config{})
		caps := redit.CapsFor(c.Version())
		if err := c.Apply(ctx, redit.String("s", "v", "w", caps)); err != nil {
			t.Fatal(err)
		}
		if v := r.Get(ctx, "s").Val(); v != "w" {
			t.Fatalf("GET s = %q", v)
		}
		if ttl := r.TTL(ctx, "s").Val(); ttl < 90*time.Second || ttl > 100*time.Second {
			t.Fatalf("TTL s = %v", ttl)
		}
		if err := c.Apply(ctx, redit.String("p", "v", "w", caps)); err != nil {
			t.Fatal(err)
		}
		if ttl := r.TTL(ctx, "p").Val(); ttl != -1 {
			t.Fatalf("TTL p = %v", ttl)
		}
	})
}

func TestApplyExecError(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		c := open(t, s, Config{})
		err := c.Apply(ctx, redit.Plan{Key: "k", Cmds: [][]string{{"SET", "k", "v"}, {"HSET", "k", "f", "v"}}})
		var ee *ExecError
		if !errors.As(err, &ee) || ee.Index != 1 || ee.Total != 2 {
			t.Fatalf("err = %v", err)
		}
		if v := r.Get(ctx, "k").Val(); v != "v" {
			t.Fatalf("GET k = %q", v)
		}
	})
}

func TestApplyBinary(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		c := open(t, s, Config{})
		const key, field = "b\xff k", "\x00f"
		p := hashPlan(t, key, nil, func(tb *edit.Table) {
			at := tb.InsertAfter(-1)
			_ = tb.SetCell(at, 0, db.Value{Text: field})
			_ = tb.SetCell(at, 1, db.Value{Text: "\xfe"})
		})
		if err := c.Apply(ctx, p); err != nil {
			t.Fatal(err)
		}
		if v := r.HGet(ctx, key, field).Val(); v != "\xfe" {
			t.Fatalf("HGET = %q", v)
		}
	})
}

func TestRename(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		c := open(t, s, Config{})
		r.Set(ctx, "a", "1", 0)
		if ok, err := c.Rename(ctx, "a", "b", false); err != nil || !ok {
			t.Fatalf("rename = %v %v", ok, err)
		}
		r.Set(ctx, "a", "2", 0)
		if ok, err := c.Rename(ctx, "a", "b", false); err != nil || ok {
			t.Fatalf("rename onto an existing key = %v %v", ok, err)
		}
		if r.Get(ctx, "a").Val() != "2" || r.Get(ctx, "b").Val() != "1" {
			t.Fatal("a refused rename changes nothing")
		}
		if ok, err := c.Rename(ctx, "a", "b", true); err != nil || !ok {
			t.Fatalf("overwrite = %v %v", ok, err)
		}
		if r.Exists(ctx, "a").Val() != 0 || r.Get(ctx, "b").Val() != "2" {
			t.Fatal("overwrite moves a onto b")
		}
		if _, err := c.Rename(ctx, "nope", "x", false); err == nil {
			t.Fatal("renaming a missing key is an error")
		}
	})
}

func TestExpire(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		c := open(t, s, Config{})
		r.Set(ctx, "k", "v", 0)
		if ok, err := c.Expire(ctx, "k", 50*time.Second); err != nil || !ok {
			t.Fatalf("expire = %v %v", ok, err)
		}
		if ttl := r.TTL(ctx, "k").Val(); ttl < 45*time.Second || ttl > 50*time.Second {
			t.Fatalf("TTL = %v", ttl)
		}
		if ok, err := c.Expire(ctx, "k", -1); err != nil || !ok {
			t.Fatalf("persist = %v %v", ok, err)
		}
		if ttl := r.TTL(ctx, "k").Val(); ttl != -1 {
			t.Fatalf("TTL = %v", ttl)
		}
		if ok, err := c.Expire(ctx, "k", -1); err != nil || !ok {
			t.Fatalf("persist without a TTL = %v %v", ok, err)
		}
		if ok, err := c.Expire(ctx, "gone", 5*time.Second); err != nil || ok {
			t.Fatalf("expire a missing key = %v %v", ok, err)
		}
		if ok, err := c.Expire(ctx, "gone", -1); err != nil || ok {
			t.Fatalf("persist a missing key = %v %v", ok, err)
		}
	})
}

func TestUnlinkScanKeys(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		c := open(t, s, Config{})
		for i := range 30 {
			r.Set(ctx, fmt.Sprintf("del:%d", i), "v", 0)
		}
		for i := range 5 {
			r.Set(ctx, fmt.Sprintf("keep:%d", i), "v", 0)
		}
		seen := map[string]bool{}
		var cursor uint64
		for {
			keys, next, err := c.ScanKeys(ctx, cursor, "del:*", 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range keys {
				seen[k] = true
			}
			if cursor = next; cursor == 0 {
				break
			}
		}
		var keys []string
		for k := range seen {
			keys = append(keys, k)
		}
		if n, err := c.Unlink(ctx, keys...); err != nil || n != 30 {
			t.Fatalf("unlink = %d %v", n, err)
		}
		if n := r.DBSize(ctx).Val(); n != 5 {
			t.Fatalf("DBSIZE = %d", n)
		}
		if n, err := c.Unlink(ctx); err != nil || n != 0 {
			t.Fatalf("unlink nothing = %d %v", n, err)
		}
	})
}

func TestInfo(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		r.FlushAll(ctx) // the container is shared: other tests leave keys in other dbs
		for i := range 3 {
			r.Set(ctx, fmt.Sprint("k", i), "v", 0)
		}
		info, err := open(t, s, Config{}).Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if info["redis_version"] == "" {
			t.Fatalf("no redis_version: %v", info)
		}
		if n, ok := info.Int("connected_clients"); !ok || n < 1 {
			t.Fatalf("connected_clients = %d %v", n, ok)
		}
		if n := info.Keys(); n != 3 {
			t.Fatalf("Keys() = %d", n)
		}
		if _, ok := info.Int("used_memory"); !ok {
			t.Fatal("no used_memory")
		}
	})
}

func TestSlowLog(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		r := raw(t, s, 0, "")
		if err := r.ConfigSet(ctx, "slowlog-log-slower-than", "0").Err(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.ConfigSet(context.Background(), "slowlog-log-slower-than", "10000") })
		r.Set(ctx, "a", "1", 0)
		got, err := open(t, s, Config{}).SlowLog(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range got {
			if len(e.Args) > 0 && strings.EqualFold(e.Args[0], "set") && e.Duration >= 0 {
				found = true
			}
		}
		if !found {
			t.Fatalf("no SET entry: %+v", got)
		}
	})
}

func TestClients(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		probe := raw(t, s, 0, "")
		if err := probe.Do(ctx, "client", "setname", "probe").Err(); err != nil {
			t.Fatal(err)
		}
		c := open(t, s, Config{})
		list, err := c.Clients(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var id int64 = -1
		for _, ci := range list {
			if ci["name"] == "probe" {
				id, _ = strconv.ParseInt(ci["id"], 10, 64)
			}
		}
		if id < 0 {
			t.Fatalf("no probe client: %v", list)
		}
		ok, err := c.KillClient(ctx, id)
		if err != nil || !ok {
			t.Fatalf("kill: %v %v", ok, err)
		}
		ok, err = c.KillClient(ctx, id)
		if err != nil || ok {
			t.Fatalf("kill again: %v %v", ok, err)
		}
	})
}

func keySizeFixture(t *testing.T, ctx context.Context, r *redis.Client) {
	t.Helper()
	p := r.Pipeline()
	p.Set(ctx, "s", "hello", 0)
	p.HSet(ctx, "h", "a", "1", "b", "2")
	p.RPush(ctx, "l", "1", "2", "3")
	p.SAdd(ctx, "st", "x", "y", "z", "w")
	p.ZAdd(ctx, "z", redis.Z{Score: 1, Member: "m"})
	p.XAdd(ctx, &redis.XAddArgs{Stream: "x", Values: []string{"f", "v"}})
	if _, err := p.Exec(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestKeySizes(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		keySizeFixture(t, ctx, raw(t, s, 0, ""))
		c := open(t, s, Config{})
		in := []KeyEntry{{"s", "string"}, {"h", "hash"}, {"l", "list"}, {"st", "set"}, {"z", "zset"}, {"x", "stream"}}
		got, err := c.KeySizes(ctx, in, true)
		if err != nil {
			t.Fatal(err)
		}
		want := []int64{5, 2, 3, 4, 1, 1}
		if len(got) != len(want) {
			t.Fatalf("got %+v", got)
		}
		for i, ks := range got {
			if ks.Key != in[i].Key || ks.Type != in[i].Type || ks.Len != want[i] || ks.Mem <= 0 {
				t.Fatalf("got[%d] = %+v", i, ks)
			}
		}
		got, err = c.KeySizes(ctx, in[:1], false)
		if err != nil || len(got) != 1 || got[0].Mem != -1 || got[0].Len != 5 {
			t.Fatalf("memory=false: %+v %v", got, err)
		}
	})
}

func TestKeySizesSkipsGone(t *testing.T) {
	eachImage(t, func(t *testing.T, image string) {
		ctx := context.Background()
		s := start(t, image)
		keySizeFixture(t, ctx, raw(t, s, 0, ""))
		got, err := open(t, s, Config{}).KeySizes(ctx, []KeyEntry{{"gone", "string"}, {"h", "string"}, {"s", "string"}}, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Key != "s" {
			t.Fatalf("got %+v", got)
		}
	})
}
