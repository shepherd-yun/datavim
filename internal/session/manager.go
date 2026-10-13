// Package session owns passwords and open connections for configured servers.
package session

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/secret"
)

// ErrPasswordRequired means no password is known; ask the user.
var ErrPasswordRequired = errors.New("password required")

type connKey struct {
	conn     string
	database string
}

// Manager opens connections lazily and reuses them. Safe for concurrent use.
type Manager struct {
	conns   []config.Connection
	drivers map[string]db.Driver
	secrets secret.Store

	mu        sync.Mutex
	passwords map[string]string
	open      map[connKey]db.Conn
}

// NewManager builds a Manager for the configured connections.
func NewManager(conns []config.Connection, secrets secret.Store, drivers ...db.Driver) *Manager {
	m := &Manager{
		conns:     conns,
		drivers:   make(map[string]db.Driver, len(drivers)),
		secrets:   secrets,
		passwords: map[string]string{},
		open:      map[connKey]db.Conn{},
	}
	for _, d := range drivers {
		m.drivers[d.Name()] = d
	}
	return m
}

// Connections returns the configured connections in file order.
func (m *Manager) Connections() []config.Connection { return m.conns }

// Conn returns an open connection to database ("" means the configured default).
func (m *Manager) Conn(ctx context.Context, name, database string) (db.Conn, error) {
	cfg, drv, err := m.lookup(name)
	if err != nil {
		return nil, err
	}
	if cfg.SSH != "" {
		return nil, fmt.Errorf("connection %q: ssh tunnels are not supported yet", name)
	}
	if database == "" {
		database = cfg.Database
	}
	key := connKey{conn: name}
	if drv.DatabaseScoped() {
		key.database = database
	}

	m.mu.Lock()
	c, ok := m.open[key]
	m.mu.Unlock()
	if ok {
		return c, nil
	}

	pw, err := m.password(name)
	if err != nil {
		return nil, err
	}
	c, err = drv.Open(ctx, db.ConnConfig{
		Host: cfg.Host, Port: cfg.Port, User: cfg.User, Password: pw,
		Database: database, Params: cfg.Params,
	})
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", name, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.open[key]; ok {
		_ = c.Close() // lost a race with another opener
		return existing, nil
	}
	m.open[key] = c
	return c, nil
}

// SetPassword remembers a password for this run and optionally saves it to
// the keychain. Open connections for name are closed so the next use reconnects.
func (m *Manager) SetPassword(name, password string, save bool) error {
	m.mu.Lock()
	m.passwords[name] = password
	for k, c := range m.open {
		if k.conn == name {
			_ = c.Close()
			delete(m.open, k)
		}
	}
	m.mu.Unlock()
	if save {
		return m.secrets.Set(name, password)
	}
	return nil
}

// CloseAll closes every open connection.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, c := range m.open {
		_ = c.Close()
		delete(m.open, k)
	}
}

func (m *Manager) lookup(name string) (config.Connection, db.Driver, error) {
	for _, c := range m.conns {
		if c.Name == name {
			drv, ok := m.drivers[c.Driver]
			if !ok {
				return c, nil, fmt.Errorf("connection %q: driver %q not available", name, c.Driver)
			}
			return c, drv, nil
		}
	}
	return config.Connection{}, nil, fmt.Errorf("unknown connection %q", name)
}

func (m *Manager) password(name string) (string, error) {
	m.mu.Lock()
	pw, ok := m.passwords[name]
	m.mu.Unlock()
	if ok {
		return pw, nil
	}
	pw, err := m.secrets.Get(name)
	if errors.Is(err, secret.ErrNotFound) {
		return "", ErrPasswordRequired
	}
	if err != nil {
		return "", fmt.Errorf("read password for %s: %w", name, err)
	}
	m.mu.Lock()
	m.passwords[name] = pw
	m.mu.Unlock()
	return pw, nil
}
