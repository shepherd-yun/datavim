// Package session owns passwords and open connections for configured servers.
package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/db/tunnel"
	"github.com/shepherd-yun/datavim/internal/history"
	"github.com/shepherd-yun/datavim/internal/rdb"
	"github.com/shepherd-yun/datavim/internal/secret"
	"github.com/shepherd-yun/datavim/internal/sqlx"
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

// RedisOpener opens a Redis client; rdb.Open implements it.
type RedisOpener func(ctx context.Context, cfg rdb.Config) (rdb.Client, error)

// RedisSessionOpener opens a dedicated Redis connection; rdb.OpenSession
// implements it.
type RedisSessionOpener func(ctx context.Context, cfg rdb.Config) (rdb.Session, error)

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
	// Audit is called after every write that reached a server (session's
	// audit wrappers); nil records nothing. It must not block for long.
	Audit func(history.AuditEntry)
	// Tunnels, if set, opens the SSH tunnels of connections with ssh set.
	Tunnels Tunneler
	// RedisOpen, if set, opens redis connections; nil makes them unavailable.
	RedisOpen RedisOpener
	// RedisSessionOpen, if set, opens redis console sessions.
	RedisSessionOpen RedisSessionOpener
	// KafkaOpen and KafkaReaderOpen, if set, open kafka clients and
	// message readers; nil makes kafka connections unavailable.
	KafkaOpen       KafkaOpener
	KafkaReaderOpen KafkaReaderOpener
	// RegistryOpen makes Schema Registry clients; nil is kafka.OpenRegistry.
	RegistryOpen RegistryOpener
	// ConnectOpen makes Kafka Connect clients; nil is kafka.OpenConnect.
	ConnectOpen ConnectOpener
	// ESOpen, if set, opens elasticsearch clients; nil makes elasticsearch
	// connections unavailable.
	ESOpen ESOpener

	mu        sync.Mutex
	conns     []config.Connection
	passwords map[string]string
	open      map[connKey]db.Conn
	redis     map[connKey]rdb.Client
	kafka     map[string]*guardedKafka
	kafkaGen  map[string]int // counts the resets of each connection
	es        map[string]*esConn
	esGen     map[string]int // counts the resets of each connection
	// registries are the kafka connections' Schema Registry clients.
	registries map[string]*guardedRegistry
	// connects are the kafka connections' Kafka Connect clients.
	connects map[connectKey]*guardedConnect
	// savedRO is the readonly in connections.toml of the connections
	// SetReadOnly switched for this run.
	savedRO map[string]*bool
}

// NewManager builds a Manager for the configured connections.
func NewManager(conns []config.Connection, secrets secret.Store, drivers ...db.Driver) *Manager {
	m := &Manager{
		conns:      conns,
		drivers:    make(map[string]db.Driver, len(drivers)),
		secrets:    secrets,
		passwords:  map[string]string{},
		open:       map[connKey]db.Conn{},
		redis:      map[connKey]rdb.Client{},
		kafka:      map[string]*guardedKafka{},
		kafkaGen:   map[string]int{},
		es:         map[string]*esConn{},
		esGen:      map[string]int{},
		registries: map[string]*guardedRegistry{},
		connects:   map[connectKey]*guardedConnect{},
		savedRO:    map[string]*bool{},
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
	if !m.driverAvailable(c.Driver) {
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
// renamed). c carries the params: the form keeps the ones it does not
// show. A readonly equal
// to the current one keeps a SetReadOnly switch for this run and saves the
// file's value; a different one is saved and ends the switch. Open
// connections are closed, and a saved password follows a rename. A non-empty
// password replaces the known one.
func (m *Manager) UpdateConnection(old string, c config.Connection, password string, save bool) error {
	c, err := config.Normalize(c)
	if err != nil {
		return err
	}
	if !m.driverAvailable(c.Driver) {
		return fmt.Errorf("connection %q: driver %q not available", c.Name, c.Driver)
	}
	cur, ok := m.Connection(old)
	if !ok {
		return fmt.Errorf("unknown connection %q", old)
	}
	if _, taken := m.Connection(c.Name); taken && c.Name != old {
		return fmt.Errorf("connection %q already exists", c.Name)
	}
	saved := c
	m.mu.Lock()
	fileRO, switched := m.savedRO[old]
	m.mu.Unlock()
	if switched = switched && sameBool(c.ReadOnly, cur.ReadOnly); switched {
		saved.ReadOnly = fileRO
	}
	if m.PersistUpdate != nil {
		if err := m.PersistUpdate(old, saved); err != nil {
			return err
		}
	}
	m.mu.Lock()
	delete(m.savedRO, old)
	if switched {
		m.savedRO[c.Name] = fileRO
	}
	for i := range m.conns {
		if m.conns[i].Name == old {
			m.conns[i] = c
		}
	}
	m.resetLocked(old)
	renamed := accounts(c.Name)
	for i, acct := range accounts(old) {
		pw, known := m.passwords[acct]
		delete(m.passwords, acct)
		if known {
			m.passwords[renamed[i]] = pw
		}
	}
	m.mu.Unlock()
	var errs []error
	if c.Name != old {
		for i, acct := range accounts(old) {
			if saved, err := m.secrets.Get(acct); err == nil {
				errs = append(errs, m.secrets.Set(renamed[i], saved), m.secrets.Delete(acct))
			}
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
// password and secrets.
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
	delete(m.savedRO, name)
	m.resetLocked(name)
	var errs []error
	for _, acct := range accounts(name) {
		delete(m.passwords, acct)
	}
	m.mu.Unlock()
	for _, acct := range accounts(name) {
		errs = append(errs, m.secrets.Delete(acct))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("connection removed, but its keychain password was not: %w", errors.Join(ErrKeychain, err))
	}
	return nil
}

// SetReadOnly switches name between read-only and read-write for this run
// only (connections.toml is not changed). Open SQL and Redis connections
// are closed so the server-side read-only setting follows on the next use;
// Kafka clients stay, their guard reads the flag on every write.
func (m *Manager) SetReadOnly(name string, ro bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.conns {
		if m.conns[i].Name == name {
			if _, ok := m.savedRO[name]; !ok {
				m.savedRO[name] = m.conns[i].ReadOnly
			}
			m.conns[i].ReadOnly = &ro
			m.resetServersLocked(name)
			return nil
		}
	}
	return fmt.Errorf("unknown connection %q", name)
}

func sameBool(a, b *bool) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
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
	m.resetServersLocked(name)
	m.kafkaGen[name]++
	delete(m.registries, name)
	for k := range m.connects {
		if k.conn == name {
			delete(m.connects, k)
		}
	}
	if c, ok := m.kafka[name]; ok {
		delete(m.kafka, name)
		go c.Close()
	}
	m.esGen[name]++
	if c, ok := m.es[name]; ok {
		delete(m.es, name)
		c.raw.Close()
	}
}

// resetServersLocked closes the SQL and Redis connections of name.
func (m *Manager) resetServersLocked(name string) {
	for k, c := range m.open {
		if k.conn == name {
			delete(m.open, k)
			// A pgx pool's Close waits for borrowed sessions, and an idle
			// console keeps one until its next run: retire the pool without
			// blocking the caller (often the UI).
			go func() { _ = c.Close() }()
		}
	}
	for k, c := range m.redis {
		if k.conn == name {
			delete(m.redis, k)
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
	dial, err := m.dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	raw, err := drv.Open(ctx, db.ConnConfig{
		Host: cfg.Host, Port: cfg.Port, User: cfg.User, Password: pw,
		Database: database, Params: cfg.Params, ReadOnly: cfg.IsReadOnly(), Dial: dial,
	})
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", name, err)
	}
	c = &auditedConn{Conn: raw, a: sqlAudit{m: m, name: name, database: database, dialect: sqlx.DialectOf(cfg.Driver)}}

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.open[key]; ok {
		_ = c.Close() // lost a race with another opener
		return existing, nil
	}
	m.open[key] = c
	return c, nil
}

// Redis returns an open client for db number n (n < 0 means the configured
// default). With no known password it connects without one and asks for a
// password (ErrPasswordRequired) only when the server answers NOAUTH.
func (m *Manager) Redis(ctx context.Context, name string, n int) (rdb.Client, error) {
	cfg, n, err := m.redisConn(name, n, m.RedisOpen != nil)
	if err != nil {
		return nil, err
	}
	key := connKey{conn: name, database: strconv.Itoa(n)}
	m.mu.Lock()
	c, ok := m.redis[key]
	m.mu.Unlock()
	if ok {
		return c, nil
	}

	rc, err := m.redisConfig(ctx, cfg, n)
	if err != nil {
		return nil, err
	}
	raw, err := m.RedisOpen(ctx, rc)
	if err := redisOpenErr(name, err); err != nil {
		return nil, err
	}
	c = &auditedRedis{Client: raw, m: m, name: name, n: n}

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.redis[key]; ok {
		_ = c.Close() // lost a race with another opener
		return existing, nil
	}
	m.redis[key] = c
	return c, nil
}

// RedisSession opens a new dedicated connection on db n (n < 0 means the
// configured default) for a console. It is not cached: the caller closes
// it. Passwords, tunnels and TLS work as for Redis.
func (m *Manager) RedisSession(ctx context.Context, name string, n int) (rdb.Session, error) {
	cfg, n, err := m.redisConn(name, n, m.RedisSessionOpen != nil)
	if err != nil {
		return nil, err
	}
	rc, err := m.redisConfig(ctx, cfg, n)
	if err != nil {
		return nil, err
	}
	s, err := m.RedisSessionOpen(ctx, rc)
	if err := redisOpenErr(name, err); err != nil {
		return nil, err
	}
	return &auditedRedisSession{Session: s, m: m, name: name}, nil
}

// redisConn checks that name is a usable redis connection and resolves
// n < 0 to its default db.
func (m *Manager) redisConn(name string, n int, available bool) (config.Connection, int, error) {
	cfg, ok := m.Connection(name)
	if !ok {
		return cfg, n, fmt.Errorf("unknown connection %q", name)
	}
	if cfg.Driver != "redis" {
		return cfg, n, fmt.Errorf("connection %q is not a redis connection", name)
	}
	if !available {
		return cfg, n, fmt.Errorf("connection %q: driver %q not available", name, cfg.Driver)
	}
	if n < 0 {
		n = config.RedisDB(cfg)
	}
	return cfg, n, nil
}

// redisConfig is the rdb.Config for db n: the known password or none (the
// server's NOAUTH asks for one), the SSH tunnel and the TLS params.
func (m *Manager) redisConfig(ctx context.Context, cfg config.Connection, n int) (rdb.Config, error) {
	pw, err := m.password(cfg.Name)
	if errors.Is(err, ErrPasswordRequired) {
		pw, err = "", nil // try without one; NOAUTH asks
	}
	if err != nil {
		return rdb.Config{}, err
	}
	dial, err := m.dial(ctx, cfg)
	if err != nil {
		return rdb.Config{}, err
	}
	return rdb.Config{
		Host: cfg.Host, Port: cfg.Port, User: cfg.User, Password: pw, DB: n,
		TLS: param(cfg, "tls"), TLSSkipVerify: param(cfg, "tls_skip_verify"), Dial: dial,
	}, nil
}

func redisOpenErr(name string, err error) error {
	if errors.Is(err, rdb.ErrAuthRequired) {
		return ErrPasswordRequired
	}
	if err != nil {
		return fmt.Errorf("connect %s: %w", name, err)
	}
	return nil
}

// param reports whether params[key] is "true".
func param(c config.Connection, key string) bool { return c.Params[key] == "true" }

// dial opens the connection's SSH tunnel, or returns nil without ssh.
func (m *Manager) dial(ctx context.Context, cfg config.Connection) (db.DialFunc, error) {
	if cfg.SSH == "" {
		return nil, nil
	}
	if m.Tunnels == nil {
		return nil, fmt.Errorf("connection %q: ssh tunnels are not available", cfg.Name)
	}
	dial, err := m.Tunnels.Connect(ctx, cfg.SSH)
	if err != nil {
		return nil, fmt.Errorf("connect %s via ssh %s: %w", cfg.Name, cfg.SSH, err)
	}
	return dial, nil
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
	for k, c := range m.redis {
		_ = c.Close()
		delete(m.redis, k)
	}
	for k, c := range m.kafka {
		c.Close()
		delete(m.kafka, k)
	}
	for k, c := range m.es {
		c.raw.Close()
		delete(m.es, k)
	}
	clear(m.registries)
	clear(m.connects)
}

func (m *Manager) driverAvailable(name string) bool {
	switch name {
	case "redis":
		return m.RedisOpen != nil
	case "kafka":
		return m.KafkaOpen != nil
	case "elasticsearch":
		return m.ESOpen != nil
	}
	_, ok := m.drivers[name]
	return ok
}

func (m *Manager) lookup(name string) (config.Connection, db.Driver, error) {
	c, ok := m.Connection(name)
	if !ok {
		return config.Connection{}, nil, fmt.Errorf("unknown connection %q", name)
	}
	if c.Driver == "redis" || c.Driver == "kafka" || c.Driver == "elasticsearch" {
		return c, nil, fmt.Errorf("connection %q is a %s connection", name, c.Driver)
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
