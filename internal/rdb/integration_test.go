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

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
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
