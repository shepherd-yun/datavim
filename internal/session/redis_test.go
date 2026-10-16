package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
)

type fakeRedisClient struct {
	rdb.Client
	mu     sync.Mutex
	closed bool
}

func (c *fakeRedisClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *fakeRedisClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// fakeRedisOpener records configs; it refuses an empty password when
// needAuth is set, like a server with requirepass.
type fakeRedisOpener struct {
	mu       sync.Mutex
	needAuth bool
	cfgs     []rdb.Config
	clients  []*fakeRedisClient
}

func (f *fakeRedisOpener) open(_ context.Context, cfg rdb.Config) (rdb.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfgs = append(f.cfgs, cfg)
	if f.needAuth && cfg.Password == "" {
		return nil, rdb.ErrAuthRequired
	}
	c := &fakeRedisClient{}
	f.clients = append(f.clients, c)
	return c, nil
}

func setupRedis(c config.Connection) (*Manager, *fakeRedisOpener, *fakeSecrets) {
	c.Name, c.Driver = "r", "redis"
	c, _ = config.Normalize(c)
	sec := &fakeSecrets{m: map[string]string{}}
	m := NewManager([]config.Connection{c}, sec)
	op := &fakeRedisOpener{}
	m.RedisOpen = op.open
	return m, op, sec
}

func TestRedisNoPassword(t *testing.T) {
	m, op, _ := setupRedis(config.Connection{})
	if _, err := m.Redis(context.Background(), "r", 0); err != nil {
		t.Fatal(err)
	}
	if len(op.cfgs) != 1 || op.cfgs[0].Password != "" || op.cfgs[0].Port != 6379 {
		t.Fatalf("cfgs = %+v", op.cfgs)
	}
}

func TestRedisAuthRequired(t *testing.T) {
	m, op, _ := setupRedis(config.Connection{})
	op.needAuth = true
	ctx := context.Background()
	if _, err := m.Redis(ctx, "r", 0); !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("err = %v", err)
	}
	if err := m.SetPassword("r", "pw", false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Redis(ctx, "r", 0); err != nil {
		t.Fatal(err)
	}
	if last := op.cfgs[len(op.cfgs)-1]; last.Password != "pw" {
		t.Fatalf("cfg = %+v", last)
	}
}

func TestRedisUsesKeychainPassword(t *testing.T) {
	m, op, sec := setupRedis(config.Connection{})
	sec.m["r"] = "saved"
	if _, err := m.Redis(context.Background(), "r", 0); err != nil || op.cfgs[0].Password != "saved" {
		t.Fatalf("cfgs = %+v, err %v", op.cfgs, err)
	}
}

func TestRedisCachesPerDB(t *testing.T) {
	m, op, _ := setupRedis(config.Connection{Database: "2"})
	ctx := context.Background()
	a, _ := m.Redis(ctx, "r", 0)
	b, _ := m.Redis(ctx, "r", 0)
	if a != b || len(op.cfgs) != 1 {
		t.Fatalf("db 0 opened %d times", len(op.cfgs))
	}
	if _, err := m.Redis(ctx, "r", 3); err != nil || len(op.cfgs) != 2 || op.cfgs[1].DB != 3 {
		t.Fatalf("cfgs = %+v, err %v", op.cfgs, err)
	}
	if _, err := m.Redis(ctx, "r", -1); err != nil || len(op.cfgs) != 3 || op.cfgs[2].DB != 2 {
		t.Fatalf("cfgs = %+v, err %v", op.cfgs, err)
	}
	if _, err := m.Redis(ctx, "r", 2); err != nil || len(op.cfgs) != 3 {
		t.Fatalf("default db not shared: %d opens", len(op.cfgs))
	}
}

func TestRedisResetCloses(t *testing.T) {
	m, op, _ := setupRedis(config.Connection{})
	ctx := context.Background()
	if _, err := m.Redis(ctx, "r", 0); err != nil {
		t.Fatal(err)
	}
	m.Reset("r")
	waitFor(t, op.clients[0].isClosed)
	if _, err := m.Redis(ctx, "r", 0); err != nil || len(op.cfgs) != 2 {
		t.Fatalf("opens = %d, err %v", len(op.cfgs), err)
	}
	m.CloseAll()
	if !op.clients[1].isClosed() {
		t.Fatal("CloseAll left the redis client open")
	}
}

func TestRedisTunnel(t *testing.T) {
	m, op, _ := setupRedis(config.Connection{SSH: "bastion"})
	tun := &fakeTunnels{}
	m.Tunnels = tun
	if _, err := m.Redis(context.Background(), "r", 0); err != nil {
		t.Fatal(err)
	}
	if op.cfgs[0].Dial == nil || len(tun.aliases) != 1 {
		t.Fatalf("cfg = %+v, aliases %v", op.cfgs[0], tun.aliases)
	}
}

func TestRedisTLSParams(t *testing.T) {
	m, op, _ := setupRedis(config.Connection{Params: map[string]string{"tls": "true", "tls_skip_verify": "true"}})
	if _, err := m.Redis(context.Background(), "r", 0); err != nil {
		t.Fatal(err)
	}
	if !op.cfgs[0].TLS || !op.cfgs[0].TLSSkipVerify {
		t.Fatalf("cfg = %+v", op.cfgs[0])
	}
}

func TestAddRedisConnection(t *testing.T) {
	m := NewManager(nil, &fakeSecrets{m: map[string]string{}})
	if err := m.AddConnection(config.Connection{Name: "r", Driver: "redis"}, "", false); err == nil {
		t.Fatal("redis added without an opener")
	}
	m.RedisOpen = (&fakeRedisOpener{}).open
	if err := m.AddConnection(config.Connection{Name: "r", Driver: "redis"}, "", false); err != nil {
		t.Fatal(err)
	}
	if err := m.UpdateConnection("r", config.Connection{Name: "r", Driver: "redis", Database: "1"}, "", false); err != nil {
		t.Fatal(err)
	}
}

func TestConnRefusesRedis(t *testing.T) {
	m, _, _ := setupRedis(config.Connection{})
	if _, err := m.Conn(context.Background(), "r", ""); err == nil || !strings.Contains(err.Error(), "is a redis connection") {
		t.Fatalf("err = %v", err)
	}
}

func TestRedisNotAvailable(t *testing.T) {
	m, _, _ := setupRedis(config.Connection{})
	m.RedisOpen = nil
	if _, err := m.Redis(context.Background(), "r", 0); err == nil {
		t.Fatal("no error without an opener")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
