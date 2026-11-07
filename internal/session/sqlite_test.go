package session

import (
	"context"
	"errors"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
)

// fakeFileDriver is a db.FileDriver.
type fakeFileDriver struct{ fakeDriver }

func (*fakeFileDriver) FileBased() {}

// countingSecrets fails the test's expectation by counting every read.
type countingSecrets struct {
	fakeSecrets
	gets int
}

func (c *countingSecrets) Get(name string) (string, error) {
	c.gets++
	return c.fakeSecrets.Get(name)
}

func sqliteManager(conn config.Connection) (*Manager, *fakeFileDriver, *fakeTunnels, *countingSecrets) {
	conn.Name, conn.Driver = "app", "sqlite"
	drv := &fakeFileDriver{fakeDriver{name: "sqlite", scoped: true, conn: &fakeConn{}}}
	sec := &countingSecrets{fakeSecrets: fakeSecrets{m: map[string]string{}}}
	m := NewManager([]config.Connection{conn}, sec, drv)
	tun := &fakeTunnels{}
	m.Tunnels = tun
	return m, drv, tun, sec
}

func TestManagerSQLiteNoPassword(t *testing.T) {
	m, drv, _, sec := sqliteManager(config.Connection{Database: "/a.db"})
	if _, err := m.Conn(context.Background(), "app", ""); err != nil {
		t.Fatalf("Conn: %v", err)
	}
	if sec.gets != 0 {
		t.Fatalf("the secret store was read %d times", sec.gets)
	}
	if got := drv.opens[0]; got.Password != "" || got.Database != "/a.db" {
		t.Fatalf("%+v", got)
	}
}

func TestManagerSQLiteInjectsExec(t *testing.T) {
	m, drv, tun, _ := sqliteManager(config.Connection{Database: "/a.db", SSH: "box"})
	if _, err := m.Conn(context.Background(), "app", ""); err != nil {
		t.Fatal(err)
	}
	got := drv.opens[0]
	if got.Exec == nil || got.Dial != nil || got.ExecHost != "box" {
		t.Fatalf("Exec set: %v, Dial set: %v, host %q", got.Exec != nil, got.Dial != nil, got.ExecHost)
	}
	if len(tun.aliases) != 1 || tun.aliases[0] != "box" {
		t.Fatalf("aliases %v", tun.aliases)
	}

	m, drv, tun, _ = sqliteManager(config.Connection{Database: "/a.db"})
	if _, err := m.Conn(context.Background(), "app", ""); err != nil {
		t.Fatal(err)
	}
	if got := drv.opens[0]; got.Exec != nil || got.Dial != nil || len(tun.aliases) != 0 {
		t.Fatalf("a local file used the tunnel: %+v", got)
	}
}

func TestManagerSQLiteReadOnly(t *testing.T) {
	m, drv, _, _ := sqliteManager(config.Connection{Database: "/a.db", Env: config.EnvProd})
	if _, err := m.Conn(context.Background(), "app", ""); err != nil {
		t.Fatal(err)
	}
	if !drv.opens[0].ReadOnly {
		t.Fatal("prod is read-only by default")
	}
}

func TestManagerSQLiteExecErrorKeepsItsType(t *testing.T) {
	m, _, tun, _ := sqliteManager(config.Connection{Database: "/a.db", SSH: "box"})
	sentinel := errors.New("unknown host")
	tun.err = sentinel
	if _, err := m.Conn(context.Background(), "app", ""); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v", err)
	}
}

var _ db.FileDriver = (*fakeFileDriver)(nil)
