package session

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/kmsgx"
)

// ConnectOpener makes a Connect client; kafka.OpenConnect implements it.
type ConnectOpener func(cfg kafka.ConnectConfig) (kafka.Connect, error)

// ErrNoConnect is a Connect call on a connection without that cluster.
var ErrNoConnect = errors.New("no such kafka connect cluster for this connection")

// connectWriteMethods are the kafka.Connect methods that change a cluster;
// guardedConnect checks and records each.
var connectWriteMethods = []string{"Pause", "Resume", "Restart", "RestartTask", "SetConfig"}

// connectKey is one Connect cluster of one connection.
type connectKey struct{ conn, cluster string }

// guardedConnect checks writes like guardedKafka, records them, and turns
// a refused password into the question for a new one.
type guardedConnect struct {
	kafka.Connect
	g       guardedKafka // for check() and audit()
	cluster string
}

func (c *guardedConnect) fail(err error) error {
	if !errors.Is(err, kafka.ErrConnectAuth) {
		return err
	}
	m, name := c.g.m, c.g.name
	cfg, ok := m.Connection(name)
	if !ok || cfg.Params["connect_user"] == "" || c.urlHasPassword(cfg) {
		return err // nothing to ask: the password is written in the URL
	}
	m.mu.Lock()
	for k, v := range m.connects {
		if k.conn == name && v == c {
			delete(m.connects, k) // made with the refused password
		}
	}
	m.mu.Unlock()
	return m.refusedSecret(name, SecretConnect)
}

func (c *guardedConnect) urlHasPassword(cfg config.Connection) bool {
	cls, _ := config.KafkaConnectClusters(cfg)
	i := slices.IndexFunc(cls, func(cl config.ConnectCluster) bool { return cl.Name == c.cluster })
	return i >= 0 && urlHasPassword(cls[i].URL)
}

func (c *guardedConnect) write(op kmsgx.Op, name, detail string, fn func() error) error {
	if err := c.g.check(); err != nil {
		return err
	}
	start := time.Now()
	err := fn()
	c.g.audit(op, c.cluster+"/"+name, detail, start, err)
	return c.fail(err)
}

func (c *guardedConnect) Version(ctx context.Context) (string, error) {
	v, err := c.Connect.Version(ctx)
	return v, c.fail(err)
}

func (c *guardedConnect) Connectors(ctx context.Context) ([]kafka.Connector, error) {
	out, err := c.Connect.Connectors(ctx)
	return out, c.fail(err)
}

func (c *guardedConnect) Connector(ctx context.Context, name string) (kafka.Connector, error) {
	out, err := c.Connect.Connector(ctx, name)
	return out, c.fail(err)
}

func (c *guardedConnect) Validate(ctx context.Context, class string, cfg map[string]string) ([]kafka.ConfigProblem, error) {
	out, err := c.Connect.Validate(ctx, class, cfg)
	return out, c.fail(err)
}

func (c *guardedConnect) Pause(ctx context.Context, name string) error {
	return c.write(kmsgx.OpPauseConnector, name, "", func() error { return c.Connect.Pause(ctx, name) })
}

func (c *guardedConnect) Resume(ctx context.Context, name string) error {
	return c.write(kmsgx.OpResumeConnector, name, "", func() error { return c.Connect.Resume(ctx, name) })
}

func (c *guardedConnect) Restart(ctx context.Context, name string) error {
	return c.write(kmsgx.OpRestartConnector, name, "", func() error { return c.Connect.Restart(ctx, name) })
}

func (c *guardedConnect) RestartTask(ctx context.Context, name string, task int) error {
	return c.write(kmsgx.OpRestartTask, name, "task "+strconv.Itoa(task), func() error { return c.Connect.RestartTask(ctx, name, task) })
}

// SetConfig records the names of the keys it changes, never their values
// (passwords live in connector configs).
func (c *guardedConnect) SetConfig(ctx context.Context, name string, old, cfg map[string]string) error {
	return c.write(kmsgx.OpAlterConnectorConfig, name, strings.Join(changedKeys(old, cfg), ", "), func() error {
		return c.Connect.SetConfig(ctx, name, old, cfg)
	})
}

// changedKeys are the keys added, removed or changed from old to cfg, sorted.
func changedKeys(old, cfg map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(cfg)) {
		if v, ok := old[k]; !ok || v != cfg[k] {
			out = append(out, k)
		}
	}
	for k := range old {
		if _, ok := cfg[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// KafkaConnect returns the client of one Connect cluster of a kafka
// connection, made on first use: connect_user's password is asked for
// when unknown (*SecretRequiredError), and HTTP goes through the tunnel.
func (m *Manager) KafkaConnect(ctx context.Context, conn, cluster string) (kafka.Connect, error) {
	cfg, err := m.kafkaConn(conn, m.KafkaOpen != nil)
	if err != nil {
		return nil, err
	}
	cls, err := config.KafkaConnectClusters(cfg)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(cls, func(cl config.ConnectCluster) bool { return cl.Name == cluster })
	if i < 0 {
		return nil, ErrNoConnect
	}
	key := connectKey{conn: conn, cluster: cluster}
	m.mu.Lock()
	c, ok := m.connects[key]
	gen := m.kafkaGen[conn]
	m.mu.Unlock()
	if ok {
		return c, nil
	}
	cc := kafka.ConnectConfig{
		URL: cls[i].URL, User: cfg.Params["connect_user"],
		TLSCA: cfg.Params["tls_ca"], TLSCert: cfg.Params["tls_cert"], TLSKey: cfg.Params["tls_key"],
		TLSSkipVerify: param(cfg, "tls_skip_verify"),
	}
	if cc.User != "" && !urlHasPassword(cc.URL) {
		if cc.Password, err = m.secretFor(conn, SecretConnect); err != nil {
			return nil, err
		}
	}
	if cc.Dial, err = m.dial(ctx, cfg); err != nil {
		return nil, err
	}
	open := m.ConnectOpen
	if open == nil {
		open = kafka.OpenConnect
	}
	raw, err := open(cc)
	if err != nil {
		return nil, err
	}
	g := &guardedConnect{Connect: raw, g: guardedKafka{m: m, name: conn}, cluster: cluster}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.connects[key]; ok {
		return existing, nil
	}
	if m.kafkaGen[conn] == gen {
		m.connects[key] = g
	}
	return g, nil
}
