package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

// KafkaOpener opens a Kafka client; kafka.Open implements it.
type KafkaOpener func(ctx context.Context, cfg kafka.Config) (kafka.Client, error)

// KafkaReaderOpener opens a message Reader; kafka.OpenReader implements it.
type KafkaReaderOpener func(ctx context.Context, cfg kafka.Config, rc kafka.ReaderConfig) (kafka.Reader, error)

// ErrReadOnly is a Kafka write on a read-only connection.
var ErrReadOnly = errors.New("read-only connection · :set noreadonly allows changes for this session")

// kafkaWriteMethods are the kafka.Client methods that change the cluster.
// guardedKafka overrides every one of them; a test fails when kafka.Client
// gains a method that is in neither this list nor the tests' read list.
var kafkaWriteMethods = []string{"Produce", "CreateTopic", "DeleteTopic", "ResetOffsets"}

// guardedKafka is the one place Kafka writes are checked against the
// connection's read-only flag, read at call time so :set noreadonly
// applies to clients already handed out. Reads pass through.
type guardedKafka struct {
	kafka.Client
	m    *Manager
	name string
}

func (g *guardedKafka) check() error {
	c, ok := g.m.Connection(g.name)
	if !ok {
		return fmt.Errorf("unknown connection %q", g.name)
	}
	if c.IsReadOnly() {
		return ErrReadOnly
	}
	return nil
}

func (g *guardedKafka) Produce(ctx context.Context, topic string, recs []kafka.ProduceRecord) ([]kafka.ProduceResult, error) {
	if err := g.check(); err != nil {
		return nil, err
	}
	return g.Client.Produce(ctx, topic, recs)
}

func (g *guardedKafka) CreateTopic(ctx context.Context, t kafka.NewTopic) error {
	if err := g.check(); err != nil {
		return err
	}
	return g.Client.CreateTopic(ctx, t)
}

func (g *guardedKafka) DeleteTopic(ctx context.Context, name string) error {
	if err := g.check(); err != nil {
		return err
	}
	return g.Client.DeleteTopic(ctx, name)
}

func (g *guardedKafka) ResetOffsets(ctx context.Context, group string, commits []kafka.OffsetCommit) ([]kafka.CommitResult, error) {
	if err := g.check(); err != nil {
		return nil, err
	}
	return g.Client.ResetOffsets(ctx, group, commits)
}

// Kafka returns the connection's client, opening it on first use. Every
// write goes through the read-only check.
func (m *Manager) Kafka(ctx context.Context, name string) (kafka.Client, error) {
	cfg, err := m.kafkaConn(name, m.KafkaOpen != nil)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	c, ok := m.kafka[name]
	gen := m.kafkaGen[name]
	m.mu.Unlock()
	if ok {
		return c, nil
	}
	kc, err := m.kafkaConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	raw, err := m.KafkaOpen(ctx, kc)
	if err := kafkaOpenErr(name, err); err != nil {
		return nil, err
	}
	m.mu.Lock()
	existing, ok := m.kafka[name]
	stale := m.kafkaGen[name] != gen
	if !ok && !stale {
		g := &guardedKafka{Client: raw, m: m, name: name}
		m.kafka[name] = g
		m.mu.Unlock()
		return g, nil
	}
	m.mu.Unlock()
	raw.Close()
	if ok {
		return existing, nil // lost a race with another opener
	}
	// reset while opening (a new password, an edited connection): the
	// settings it was opened with are old
	return m.Kafka(ctx, name)
}

// KafkaReader opens a new Reader for a message view; the caller closes it.
func (m *Manager) KafkaReader(ctx context.Context, name string, rc kafka.ReaderConfig) (kafka.Reader, error) {
	cfg, err := m.kafkaConn(name, m.KafkaReaderOpen != nil)
	if err != nil {
		return nil, err
	}
	kc, err := m.kafkaConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	r, err := m.KafkaReaderOpen(ctx, kc, rc)
	if err := kafkaOpenErr(name, err); err != nil {
		return nil, err
	}
	return r, nil
}

func (m *Manager) kafkaConn(name string, available bool) (config.Connection, error) {
	cfg, ok := m.Connection(name)
	if !ok {
		return cfg, fmt.Errorf("unknown connection %q", name)
	}
	if cfg.Driver != "kafka" {
		return cfg, fmt.Errorf("connection %q is not a kafka connection", name)
	}
	if !available {
		return cfg, fmt.Errorf("connection %q: driver %q not available", name, cfg.Driver)
	}
	return cfg, nil
}

// kafkaConfig maps the connection to a kafka.Config: brokers, SASL with
// the known password (none known: ErrPasswordRequired), TLS params and the
// SSH tunnel.
func (m *Manager) kafkaConfig(ctx context.Context, cfg config.Connection) (kafka.Config, error) {
	kc := kafka.Config{
		Brokers: config.KafkaBrokers(cfg), SASL: cfg.Params["sasl"],
		TLS: param(cfg, "tls"), TLSSkipVerify: param(cfg, "tls_skip_verify"),
		TLSCA: cfg.Params["tls_ca"], TLSCert: cfg.Params["tls_cert"], TLSKey: cfg.Params["tls_key"],
		ClientID: cfg.Params["client_id"],
	}
	if kc.ClientID == "" {
		kc.ClientID = "sqlvim"
	}
	if kc.SASL != "" {
		pw, err := m.password(cfg.Name)
		if err != nil {
			return kafka.Config{}, err
		}
		kc.User, kc.Password = cfg.User, pw
	}
	dial, err := m.dial(ctx, cfg)
	if err != nil {
		return kafka.Config{}, err
	}
	kc.Dial = dial
	return kc, nil
}

func kafkaOpenErr(name string, err error) error {
	if errors.Is(err, kafka.ErrAuthFailed) {
		return ErrPasswordRequired
	}
	if err != nil {
		return fmt.Errorf("connect %s: %w", name, err)
	}
	return nil
}
