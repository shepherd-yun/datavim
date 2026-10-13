package session

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/db/tunnel"
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

// fakeConn closes like a pgx pool: Close returns only once release (if
// set) is closed, standing in for sessions still borrowed from the pool.
type fakeConn struct {
	db.Conn
	release chan struct{}
	done    chan struct{}
}

func (c *fakeConn) Close() error {
	if c.release != nil {
		<-c.release
	}
	close(c.done)
	return nil
}

// waitClosed reports whether c was closed within a second.
func (c *fakeConn) waitClosed() bool {
	select {
	case <-c.done:
		return true
	case <-time.After(time.Second):
		return false
	}
}

type fakeDriver struct {
	name   string
	scoped bool
	mu     sync.Mutex
	opens  []db.ConnConfig
	err    error
	// release, if set, makes Close wait until it is closed.
	release chan struct{}
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
	return &fakeConn{release: d.release, done: make(chan struct{})}, nil
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
	if !c.(*fakeConn).waitClosed() {
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
		t.Error("ssh connections need Tunnels")
	}
}

func TestConnPassesReadOnly(t *testing.T) {
	drv := &fakeDriver{name: "postgres", scoped: true}
	sec := &fakeSecrets{m: map[string]string{"prod": "pw"}}
	m := NewManager([]config.Connection{{Name: "prod", Driver: "postgres", Env: config.EnvProd}}, sec, drv)
	if _, err := m.Conn(context.Background(), "prod", ""); err != nil {
		t.Fatal(err)
	}
	if !drv.opens[0].ReadOnly {
		t.Fatal("prod connections default to read-only")
	}
}

func TestAddConnection(t *testing.T) {
	m, drv, sec := setup(true)
	var persisted []config.Connection
	m.Persist = func(c config.Connection) error { persisted = append(persisted, c); return nil }

	if err := m.AddConnection(config.Connection{Name: "new", Driver: "postgres", User: "u"}, "pw", true); err != nil {
		t.Fatal(err)
	}
	c, ok := m.Connection("new")
	if !ok || c.Port != 5432 || c.Env != config.EnvDev {
		t.Fatalf("connection = %+v, %v (want normalized)", c, ok)
	}
	if len(persisted) != 1 || persisted[0].Name != "new" {
		t.Fatalf("persisted = %v", persisted)
	}
	if sec.m["new"] != "pw" {
		t.Fatal("password saved to the keychain")
	}
	if names := len(m.Connections()); names != 2 {
		t.Fatalf("connections = %d", names)
	}
	if _, err := m.Conn(context.Background(), "new", ""); err != nil || drv.opens[0].Password != "pw" {
		t.Fatalf("connect new: %v %+v", err, drv.opens)
	}

	if err := m.AddConnection(config.Connection{Name: "new", Driver: "postgres"}, "", false); err == nil {
		t.Fatal("duplicate names are rejected")
	}
	if err := m.AddConnection(config.Connection{Name: "x", Driver: "mysql"}, "", false); err == nil {
		t.Fatal("drivers that are not registered are rejected")
	}
	m.Persist = func(config.Connection) error { return errors.New("disk full") }
	if err := m.AddConnection(config.Connection{Name: "y", Driver: "postgres"}, "", false); err == nil {
		t.Fatal("persist errors are returned")
	}
	if _, ok := m.Connection("y"); ok {
		t.Fatal("a connection that failed to persist is not added")
	}
}

func TestResetClosesOnlyThatConnection(t *testing.T) {
	m, _, sec := setup(true)
	sec.m["local"] = "pw"
	ctx := context.Background()
	a, _ := m.Conn(ctx, "local", "app")
	b, _ := m.Conn(ctx, "local", "other")
	m.Reset("local")
	if !a.(*fakeConn).waitClosed() || !b.(*fakeConn).waitClosed() {
		t.Fatal("all databases of the connection are closed")
	}
	c, _ := m.Conn(ctx, "local", "app")
	if c == a {
		t.Fatal("the next Conn reconnects")
	}
}

type fakeTunnels struct {
	mu      sync.Mutex
	aliases []string
	err     error
	pass    map[string]string
	trusted []string
}

func (f *fakeTunnels) Connect(_ context.Context, alias string) (db.DialFunc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aliases = append(f.aliases, alias)
	if f.err != nil {
		return nil, f.err
	}
	return func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("fake") }, nil
}

func (f *fakeTunnels) SetPassphrase(path, pass string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pass == nil {
		f.pass = map[string]string{}
	}
	f.pass[path] = pass
}

func (f *fakeTunnels) TrustHost(e *tunnel.UnknownHostError) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trusted = append(f.trusted, e.Host)
	return nil
}

func TestConnThroughSSHTunnel(t *testing.T) {
	m, drv, sec := setup(true)
	sec.m["local"] = "pw"
	tun := &fakeTunnels{}
	m.Tunnels = tun
	m.conns[0].SSH = "bastion"
	if _, err := m.Conn(context.Background(), "local", ""); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tun.aliases, []string{"bastion"}) || len(drv.opens) != 1 || drv.opens[0].Dial == nil {
		t.Fatalf("aliases = %v, opens = %+v", tun.aliases, drv.opens)
	}
}

func TestDirectConnectionsSkipTunnels(t *testing.T) {
	m, drv, sec := setup(true)
	sec.m["local"] = "pw"
	tun := &fakeTunnels{}
	m.Tunnels = tun
	if _, err := m.Conn(context.Background(), "local", ""); err != nil {
		t.Fatal(err)
	}
	if len(tun.aliases) != 0 || drv.opens[0].Dial != nil {
		t.Fatalf("aliases = %v, dial set = %v", tun.aliases, drv.opens[0].Dial != nil)
	}
}

func TestTunnelErrorsKeepTheirType(t *testing.T) {
	m, drv, sec := setup(true)
	sec.m["local"] = "pw"
	m.Tunnels = &fakeTunnels{err: &tunnel.PassphraseError{Path: "/home/me/.ssh/id_ed25519"}}
	m.conns[0].SSH = "bastion"
	_, err := m.Conn(context.Background(), "local", "")
	var pe *tunnel.PassphraseError
	if !errors.As(err, &pe) || len(drv.opens) != 0 {
		t.Fatalf("err = %v, opens = %d", err, len(drv.opens))
	}
}

func TestSetReadOnly(t *testing.T) {
	m, drv, sec := setup(true)
	sec.m["local"] = "pw"
	ctx := context.Background()
	c1, err := m.Conn(ctx, "local", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetReadOnly("local", true); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := m.Connection("local"); !cfg.IsReadOnly() {
		t.Fatal("Connection reflects the override")
	}
	if !c1.(*fakeConn).waitClosed() {
		t.Fatal("open connections are closed so the server-side guard follows")
	}
	if _, err := m.Conn(ctx, "local", ""); err != nil {
		t.Fatal(err)
	}
	if !drv.opens[1].ReadOnly {
		t.Fatal("reopened read-only")
	}
	if err := m.SetReadOnly("local", false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Conn(ctx, "local", ""); err != nil {
		t.Fatal(err)
	}
	if drv.opens[2].ReadOnly {
		t.Fatal("reopened read-write")
	}
	if err := m.SetReadOnly("nope", true); err == nil {
		t.Fatal("unknown connection must error")
	}
}

func TestSetPassphraseAndTrustHostGoToTunnels(t *testing.T) {
	m, _, _ := setup(true)
	if err := m.TrustHost(&tunnel.UnknownHostError{Host: "h"}); err == nil {
		t.Fatal("no tunnels: TrustHost errors")
	}
	m.SetPassphrase("/k", "x") // no tunnels: ignored
	tun := &fakeTunnels{}
	m.Tunnels = tun
	m.SetPassphrase("/k", "s3")
	if err := m.TrustHost(&tunnel.UnknownHostError{Host: "[h]:2222"}); err != nil {
		t.Fatal(err)
	}
	if tun.pass["/k"] != "s3" || !slices.Equal(tun.trusted, []string{"[h]:2222"}) {
		t.Fatalf("tunnels = %+v", tun)
	}
}

// A pgx pool's Close waits for sessions borrowed from it, and an idle console
// keeps one. Switching read-only from the UI must not wait for that session.
func TestSetReadOnlyDoesNotWaitForBorrowedSessions(t *testing.T) {
	m, drv, sec := setup(true)
	sec.m["local"] = "pw"
	drv.release = make(chan struct{})
	c, err := m.Conn(context.Background(), "local", "")
	if err != nil {
		t.Fatal(err)
	}
	returned := make(chan error, 1)
	go func() { returned <- m.SetReadOnly("local", true) }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(drv.release)
		t.Fatal("SetReadOnly blocked until the borrowed session was returned")
	}
	if _, err := m.Conn(context.Background(), "local", ""); err != nil {
		t.Fatal(err)
	}
	if len(drv.opens) != 2 || !drv.opens[1].ReadOnly {
		t.Fatalf("a new read-only pool is opened: %+v", drv.opens)
	}
	close(drv.release) // the console returns its session
	if !c.(*fakeConn).waitClosed() {
		t.Fatal("the retired pool closes once its sessions are back")
	}
}
