// Package session owns passwords and open connections for configured servers.
package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/db/tunnel"
	"github.com/shepherd-yun/sqlvim/internal/secret"
)

// ErrPasswordRequired means no password is known; ask the user.
var ErrPasswordRequired = errors.New("password required")

// ErrKeychain marks an UpdateConnection or RemoveConnection that changed
// connections.toml but could not update the saved password.
var ErrKeychain = errors.New("keychain not updated")

// Tunneler opens SSH tunnels; *tunnel.Pool implements it.
type Tunneler interface {
	Connect(ctx context.Context, alias string) (db.DialFunc, error)
	SetPassphrase(path, passphrase string)
	TrustHost(e *tunnel.UnknownHostError) error
}

type connKey struct {
	conn     string
	database string
}

// Manager opens connections lazily and reuses them. Safe for concurrent use.
type Manager struct {
	drivers map[string]db.Driver
	secrets secret.Store

	// Persist, if set, saves connections added with AddConnection.
	Persist func(config.Connection) error
	// PersistUpdate and PersistRemove, if set, save UpdateConnection and
	// RemoveConnection.
	PersistUpdate func(old string, c config.Connection) error
	PersistRemove func(name string) error
	// Tunnels, if set, opens the SSH tunnels of connections with ssh set.
	Tunnels Tunneler

	mu        sync.Mutex
	conns     []config.Connection
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

// Connections returns the configured connections in order.
func (m *Manager) Connections() []config.Connection {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.conns)
}

// Connection returns the named connection's configuration.
func (m *Manager) Connection(name string) (config.Connection, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.conns {
		if c.Name == name {
			return c, true
		}
	}
	return config.Connection{}, false
}

// AddConnection validates, persists and registers a new connection, then
// remembers its password (saving it to the keychain when save is set).
func (m *Manager) AddConnection(c config.Connection, password string, save bool) error {
	c, err := config.Normalize(c)
	if err != nil {
		return err
	}
	if _, ok := m.drivers[c.Driver]; !ok {
		return fmt.Errorf("connection %q: driver %q not available", c.Name, c.Driver)
	}
	if _, ok := m.Connection(c.Name); ok {
		return fmt.Errorf("connection %q already exists", c.Name)
	}
	if m.Persist != nil {
		if err := m.Persist(c); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.conns = append(m.conns, c)
	m.mu.Unlock()
	if password == "" {
		return nil
	}
	return m.SetPassword(c.Name, password, save)
}

// UpdateConnection replaces the connection called old with c (which may be
// renamed). Keys the form does not edit (readonly, params) are kept. Open
// connections are closed, and a saved password follows a rename. A non-empty
// password replaces the known one.
func (m *Manager) UpdateConnection(old string, c config.Connection, password string, save bool) error {
	c, err := config.Normalize(c)
	if err != nil {
		return err
	}
	if _, ok := m.drivers[c.Driver]; !ok {
		return fmt.Errorf("connection %q: driver %q not available", c.Name, c.Driver)
	}
	cur, ok := m.Connection(old)
	if !ok {
		return fmt.Errorf("unknown connection %q", old)
	}
	if _, taken := m.Connection(c.Name); taken && c.Name != old {
		return fmt.Errorf("connection %q already exists", c.Name)
	}
	c.ReadOnly, c.Params = cur.ReadOnly, cur.Params
	if m.PersistUpdate != nil {
		if err := m.PersistUpdate(old, c); err != nil {
			return err
		}
	}
	m.mu.Lock()
	for i := range m.conns {
		if m.conns[i].Name == old {
			m.conns[i] = c
		}
	}
	m.resetLocked(old)
	pw, known := m.passwords[old]
	delete(m.passwords, old)
	if known {
		m.passwords[c.Name] = pw
	}
	m.mu.Unlock()
	var errs []error
	if c.Name != old {
		if saved, err := m.secrets.Get(old); err == nil {
			errs = append(errs, m.secrets.Set(c.Name, saved), m.secrets.Delete(old))
		}
	}
	if password != "" {
		errs = append(errs, m.SetPassword(c.Name, password, save))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("connection saved, but the keychain was not updated: %w", errors.Join(ErrKeychain, err))
	}
	return nil
}

// RemoveConnection forgets the connection, closes it and deletes its saved
// password.
func (m *Manager) RemoveConnection(name string) error {
	if _, ok := m.Connection(name); !ok {
		return fmt.Errorf("unknown connection %q", name)
	}
	if m.PersistRemove != nil {
		if err := m.PersistRemove(name); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.conns = slices.DeleteFunc(m.conns, func(c config.Connection) bool { return c.Name == name })
	m.resetLocked(name)
	delete(m.passwords, name)
	m.mu.Unlock()
	if err := m.secrets.Delete(name); err != nil {
		return fmt.Errorf("connection removed, but its keychain password was not: %w", errors.Join(ErrKeychain, err))
	}
	return nil
}

// SetReadOnly switches name between read-only and read-write for this run
// only (connections.toml is not changed). Open connections are closed so the
// server-side read-only setting follows on the next use.
func (m *Manager) SetReadOnly(name string, ro bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.conns {
		if m.conns[i].Name == name {
			m.conns[i].ReadOnly = &ro
			m.resetLocked(name)
			return nil
		}
	}
	return fmt.Errorf("unknown connection %q", name)
}

// SetPassphrase remembers an SSH key passphrase for this run.
func (m *Manager) SetPassphrase(path, passphrase string) {
	if m.Tunnels != nil {
		m.Tunnels.SetPassphrase(path, passphrase)
	}
}

// TrustHost adds an SSH host key to known_hosts.
func (m *Manager) TrustHost(e *tunnel.UnknownHostError) error {
	if m.Tunnels == nil {
		return errors.New("ssh tunnels are not available")
	}
	return m.Tunnels.TrustHost(e)
}

// Reset closes every open connection of name; the next Conn reconnects.
func (m *Manager) Reset(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetLocked(name)
}

func (m *Manager) resetLocked(name string) {
	for k, c := range m.open {
		if k.conn == name {
			delete(m.open, k)
			// A pgx pool's Close waits for borrowed sessions, and an idle
			// console keeps one until its next run: retire the pool without
			// blocking the caller (often the UI).
			go func() { _ = c.Close() }()
		}
	}
}

// Conn returns an open connection to database ("" means the configured default).
func (m *Manager) Conn(ctx context.Context, name, database string) (db.Conn, error) {
	cfg, drv, err := m.lookup(name)
	if err != nil {
		return nil, err
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
	var dial db.DialFunc
	if cfg.SSH != "" {
		if m.Tunnels == nil {
			return nil, fmt.Errorf("connection %q: ssh tunnels are not available", name)
		}
		if dial, err = m.Tunnels.Connect(ctx, cfg.SSH); err != nil {
			return nil, fmt.Errorf("connect %s via ssh %s: %w", name, cfg.SSH, err)
		}
	}
	c, err = drv.Open(ctx, db.ConnConfig{
		Host: cfg.Host, Port: cfg.Port, User: cfg.User, Password: pw,
		Database: database, Params: cfg.Params, ReadOnly: cfg.IsReadOnly(), Dial: dial,
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
	m.resetLocked(name)
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
	c, ok := m.Connection(name)
	if !ok {
		return config.Connection{}, nil, fmt.Errorf("unknown connection %q", name)
	}
	drv, ok := m.drivers[c.Driver]
	if !ok {
		return c, nil, fmt.Errorf("connection %q: driver %q not available", name, c.Driver)
	}
	return c, drv, nil
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
