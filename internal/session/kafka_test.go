package session

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

// kafkaReadMethods are the kafka.Client methods that never change the
// cluster. Every method must be here or in kafkaWriteMethods.
var kafkaReadMethods = []string{
	"Cluster", "Topics", "TopicDetail", "TopicConfigs", "Supports", "Close",
	"Groups", "GroupDetail", "GroupsLag", "TopicGroups", "OffsetsAt",
}

// fakeKafkaClient records the write calls that reach it.
type fakeKafkaClient struct {
	kafka.Client
	mu     sync.Mutex
	calls  []string
	closed bool
}

func (c *fakeKafkaClient) record(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, name)
}

func (c *fakeKafkaClient) Produce(context.Context, string, []kafka.ProduceRecord) ([]kafka.ProduceResult, error) {
	c.record("Produce")
	return nil, nil
}

func (c *fakeKafkaClient) CreateTopic(context.Context, kafka.NewTopic) error {
	c.record("CreateTopic")
	return nil
}

func (c *fakeKafkaClient) DeleteTopic(context.Context, string) error {
	c.record("DeleteTopic")
	return nil
}

func (c *fakeKafkaClient) ResetOffsets(context.Context, string, []kafka.OffsetCommit) ([]kafka.CommitResult, error) {
	c.record("ResetOffsets")
	return nil, nil
}

func (c *fakeKafkaClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

func (c *fakeKafkaClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// waitClosed waits a little for a Close run in the background.
func (c *fakeKafkaClient) waitClosed() bool {
	for range 100 {
		if c.isClosed() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

type fakeKafkaReader struct{ kafka.Reader }

type fakeKafkaOpener struct {
	mu         sync.Mutex
	cfgs       []kafka.Config
	clients    []*fakeKafkaClient
	readers    int
	authFailed bool
	err        error // returned by the next opens when set
	during     func() // runs inside the next open, once
}

func (f *fakeKafkaOpener) open(_ context.Context, cfg kafka.Config) (kafka.Client, error) {
	f.mu.Lock()
	during := f.during
	f.during = nil
	f.mu.Unlock()
	if during != nil {
		during()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfgs = append(f.cfgs, cfg)
	if f.authFailed {
		return nil, kafka.ErrAuthFailed
	}
	if f.err != nil {
		return nil, f.err
	}
	c := &fakeKafkaClient{}
	f.clients = append(f.clients, c)
	return c, nil
}

func (f *fakeKafkaOpener) openReader(_ context.Context, cfg kafka.Config, _ kafka.ReaderConfig) (kafka.Reader, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfgs = append(f.cfgs, cfg)
	f.readers++
	return &fakeKafkaReader{}, nil
}

func setupKafka(c config.Connection) (*Manager, *fakeKafkaOpener, *fakeSecrets) {
	c.Name, c.Driver = "k", "kafka"
	c, _ = config.Normalize(c)
	sec := &fakeSecrets{m: map[string]string{}}
	m := NewManager([]config.Connection{c}, sec)
	op := &fakeKafkaOpener{}
	m.KafkaOpen = op.open
	m.KafkaReaderOpen = op.openReader
	return m, op, sec
}

func TestKafkaClientMethodsClassified(t *testing.T) {
	typ := reflect.TypeFor[kafka.Client]()
	for i := range typ.NumMethod() {
		name := typ.Method(i).Name
		if !slices.Contains(kafkaReadMethods, name) && !slices.Contains(kafkaWriteMethods, name) {
			t.Errorf("kafka.Client.%s is neither a read nor a write method: add it to kafkaWriteMethods (and guard it) or to kafkaReadMethods", name)
		}
	}
	// Whether guardedKafka overrides each write method is not visible to
	// reflect (embedded methods are promoted): TestKafkaGuardRefusesWrites
	// calls every one on a read-only connection instead.
}

// callWrite calls the write method name on c with zero arguments.
func callWrite(c kafka.Client, name string) error {
	ctx := context.Background()
	switch name {
	case "Produce":
		_, err := c.Produce(ctx, "t", nil)
		return err
	case "CreateTopic":
		return c.CreateTopic(ctx, kafka.NewTopic{})
	case "DeleteTopic":
		return c.DeleteTopic(ctx, "t")
	case "ResetOffsets":
		_, err := c.ResetOffsets(ctx, "g", nil)
		return err
	}
	panic("no call for " + name)
}

func TestKafkaGuardRefusesWrites(t *testing.T) {
	on := true
	m, op, _ := setupKafka(config.Connection{ReadOnly: &on})
	c, err := m.Kafka(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range kafkaWriteMethods {
		if err := callWrite(c, name); !errors.Is(err, ErrReadOnly) {
			t.Errorf("%s on a read-only connection: err = %v", name, err)
		}
	}
	if calls := op.clients[0].calls; len(calls) != 0 {
		t.Fatalf("writes reached the client: %v", calls)
	}
	if err := m.SetReadOnly("k", false); err != nil {
		t.Fatal(err)
	}
	for _, name := range kafkaWriteMethods {
		if err := callWrite(c, name); err != nil {
			t.Errorf("%s after :set noreadonly: %v", name, err)
		}
	}
	if calls := op.clients[0].calls; !slices.Equal(calls, kafkaWriteMethods) {
		t.Fatalf("calls = %v", calls)
	}
	if op.clients[0].isClosed() {
		t.Fatal("switching read-only keeps the kafka client open")
	}
}

func TestKafkaPasswordRequired(t *testing.T) {
	m, op, sec := setupKafka(config.Connection{User: "app", Params: map[string]string{"sasl": "plain"}})
	if _, err := m.Kafka(context.Background(), "k"); !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("err = %v", err)
	}
	if len(op.cfgs) != 0 {
		t.Fatal("opened without a password")
	}
	sec.m["k"] = "pw"
	if _, err := m.Kafka(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	if op.cfgs[0].Password != "pw" || op.cfgs[0].User != "app" || op.cfgs[0].SASL != "plain" {
		t.Fatalf("cfg = %+v", op.cfgs[0])
	}
	// without SASL the user is not used and no password is asked
	m, _, _ = setupKafka(config.Connection{User: "app"})
	if _, err := m.Kafka(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
}

func TestKafkaAuthFailedAsks(t *testing.T) {
	m, op, sec := setupKafka(config.Connection{User: "app", Params: map[string]string{"sasl": "scram-sha-256"}})
	sec.m["k"] = "wrong"
	op.authFailed = true
	if _, err := m.Kafka(context.Background(), "k"); !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("err = %v", err)
	}
}

func TestKafkaCachesAndResets(t *testing.T) {
	m, op, _ := setupKafka(config.Connection{})
	ctx := context.Background()
	a, _ := m.Kafka(ctx, "k")
	b, _ := m.Kafka(ctx, "k")
	if a != b || len(op.clients) != 1 {
		t.Fatalf("cached: %v %v, %d opens", a, b, len(op.clients))
	}
	m.Reset("k")
	if !op.clients[0].waitClosed() {
		t.Fatal("Reset closes the client")
	}
	if _, err := m.Kafka(ctx, "k"); err != nil || len(op.clients) != 2 {
		t.Fatalf("reopen: %v, %d", err, len(op.clients))
	}
	m.CloseAll()
	if !op.clients[1].isClosed() {
		t.Fatal("CloseAll closes the client")
	}
}

func TestKafkaConfigFromParams(t *testing.T) {
	m, op, _ := setupKafka(config.Connection{Host: "b1,b2:9093", Params: map[string]string{
		"tls": "true", "tls_skip_verify": "true", "tls_ca": "/ca.pem", "tls_cert": "/c.pem", "tls_key": "/k.pem",
	}})
	if _, err := m.Kafka(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	cfg := op.cfgs[0]
	if !slices.Equal(cfg.Brokers, []string{"b1:9092", "b2:9093"}) || !cfg.TLS || !cfg.TLSSkipVerify ||
		cfg.TLSCA != "/ca.pem" || cfg.TLSCert != "/c.pem" || cfg.TLSKey != "/k.pem" || cfg.ClientID != "sqlvim" || cfg.SASL != "" {
		t.Fatalf("cfg = %+v", cfg)
	}
	m, op, _ = setupKafka(config.Connection{Params: map[string]string{"client_id": "me"}})
	_, _ = m.Kafka(context.Background(), "k")
	if op.cfgs[0].ClientID != "me" {
		t.Fatalf("client id = %q", op.cfgs[0].ClientID)
	}
}

func TestKafkaTunnel(t *testing.T) {
	m, op, _ := setupKafka(config.Connection{SSH: "bastion"})
	m.Tunnels = &fakeTunnels{}
	if _, err := m.Kafka(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	if op.cfgs[0].Dial == nil {
		t.Fatal("no tunnel dialer")
	}
}

func TestKafkaReaderNotCached(t *testing.T) {
	m, op, _ := setupKafka(config.Connection{})
	ctx := context.Background()
	a, _ := m.KafkaReader(ctx, "k", kafka.ReaderConfig{})
	b, _ := m.KafkaReader(ctx, "k", kafka.ReaderConfig{})
	if a == b || op.readers != 2 {
		t.Fatalf("readers: %d", op.readers)
	}
	if _, err := m.Kafka(ctx, "nope"); err == nil {
		t.Fatal("unknown connection")
	}
}

func TestKafkaWrongDriver(t *testing.T) {
	m, _, _ := setupKafka(config.Connection{})
	m.conns = append(m.conns, config.Connection{Name: "r", Driver: "redis"})
	if _, err := m.Kafka(context.Background(), "r"); err == nil {
		t.Fatal("a redis connection is not kafka")
	}
	if _, err := m.Conn(context.Background(), "k", ""); err == nil {
		t.Fatal("a kafka connection has no SQL Conn")
	}
}

func TestKafkaResetDuringOpen(t *testing.T) {
	m, op, _ := setupKafka(config.Connection{})
	// a password or connection change while the client is being opened
	op.during = func() { m.Reset("k") }
	c, err := m.Kafka(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(op.clients) != 2 || !op.clients[0].waitClosed() {
		t.Fatalf("the client opened before the reset is closed and opened again: %d clients", len(op.clients))
	}
	if c.(*guardedKafka).Client != op.clients[1] {
		t.Fatal("the client cached is the one opened after the reset")
	}
}
