package session

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/secret"
)

type fakeSecrets struct{ m map[string]string }

func (f *fakeSecrets) Get(name string) (string, error) {
	pw, ok := f.m[name]
	if !ok {
		return "", secret.ErrNotFound
	}
	return pw, nil
}

func (f *fakeSecrets) Set(name, pw string) error { f.m[name] = pw; return nil }

type fakeConn struct {
	db.Conn
	closed bool
}

func (c *fakeConn) Close() error { c.closed = true; return nil }

type fakeDriver struct {
	name   string
	scoped bool
	mu     sync.Mutex
	opens  []db.ConnConfig
	err    error
}

func (d *fakeDriver) Name() string         { return d.name }
func (d *fakeDriver) DatabaseScoped() bool { return d.scoped }
func (d *fakeDriver) Open(_ context.Context, cfg db.ConnConfig) (db.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opens = append(d.opens, cfg)
	if d.err != nil {
		return nil, d.err
	}
	return &fakeConn{}, nil
}

func setup(scoped bool) (*Manager, *fakeDriver, *fakeSecrets) {
	drv := &fakeDriver{name: "postgres", scoped: scoped}
	sec := &fakeSecrets{m: map[string]string{}}
	conns := []config.Connection{{Name: "local", Driver: "postgres", Host: "h", Port: 5432, User: "u", Database: "app"}}
	return NewManager(conns, sec, drv), drv, sec
}

func TestConnUsesStoredPasswordAndCaches(t *testing.T) {
	m, drv, sec := setup(true)
	sec.m["local"] = "pw"
	ctx := context.Background()
	c1, err := m.Conn(ctx, "local", "")
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := m.Conn(ctx, "local", "app")
	if c1 != c2 {
		t.Error("empty database must fall back to the configured one and reuse the conn")
	}
	if len(drv.opens) != 1 || drv.opens[0].Password != "pw" || drv.opens[0].Database != "app" {
		t.Fatalf("opens = %+v", drv.opens)
	}
}

func TestPasswordRequiredThenSet(t *testing.T) {
	m, drv, sec := setup(true)
	ctx := context.Background()
	if _, err := m.Conn(ctx, "local", ""); !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("err = %v, want ErrPasswordRequired", err)
	}
	if err := m.SetPassword("local", "typed", false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Conn(ctx, "local", ""); err != nil {
		t.Fatal(err)
	}
	if drv.opens[0].Password != "typed" {
		t.Errorf("password = %q", drv.opens[0].Password)
	}
	if _, ok := sec.m["local"]; ok {
		t.Error("save=false must not write the keychain")
	}
	_ = m.SetPassword("local", "saved", true)
	if sec.m["local"] != "saved" {
		t.Error("save=true must write the keychain")
	}
}

func TestSetPasswordClosesOpenConns(t *testing.T) {
	m, _, sec := setup(true)
	sec.m["local"] = "old"
	c, _ := m.Conn(context.Background(), "local", "")
	_ = m.SetPassword("local", "new", false)
	if !c.(*fakeConn).closed {
		t.Error("existing conns must be closed so the new password is used")
	}
}

func TestScopedDriverOpensPerDatabase(t *testing.T) {
	m, drv, sec := setup(true)
	sec.m["local"] = "pw"
	ctx := context.Background()
	_, _ = m.Conn(ctx, "local", "app")
	_, _ = m.Conn(ctx, "local", "other")
	if len(drv.opens) != 2 || drv.opens[1].Database != "other" {
		t.Fatalf("opens = %+v", drv.opens)
	}
}

func TestUnscopedDriverSharesConn(t *testing.T) {
	m, drv, sec := setup(false)
	sec.m["local"] = "pw"
	ctx := context.Background()
	_, _ = m.Conn(ctx, "local", "app")
	_, _ = m.Conn(ctx, "local", "other")
	if len(drv.opens) != 1 {
		t.Fatalf("opens = %+v", drv.opens)
	}
}

func TestOpenFailureIsNotCached(t *testing.T) {
	m, drv, sec := setup(true)
	sec.m["local"] = "pw"
	drv.err = errors.New("connection refused")
	ctx := context.Background()
	if _, err := m.Conn(ctx, "local", ""); err == nil {
		t.Fatal("want error")
	}
	drv.err = nil
	if _, err := m.Conn(ctx, "local", ""); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestErrors(t *testing.T) {
	m, _, _ := setup(true)
	if _, err := m.Conn(context.Background(), "nope", ""); err == nil {
		t.Error("unknown connection must error")
	}
	sec := &fakeSecrets{m: map[string]string{"t": "pw"}}
	m2 := NewManager([]config.Connection{{Name: "t", Driver: "postgres", SSH: "bastion"}}, sec, &fakeDriver{name: "postgres"})
	if _, err := m2.Conn(context.Background(), "t", ""); err == nil {
		t.Error("ssh connections must error until v0.3")
	}
}
