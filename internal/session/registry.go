package session

import (
	"context"
	"errors"
	"net/url"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

// RegistryOpener makes a Schema Registry client; kafka.OpenRegistry
// implements it.
type RegistryOpener func(cfg kafka.RegistryConfig) (kafka.Registry, error)

// ErrNoRegistry is a Schema Registry call on a connection without schema_registry_url.
var ErrNoRegistry = errors.New("no schema_registry_url for this connection")

// registryWriteMethods are the kafka.Registry methods that change the
// registry; guardedRegistry checks each against the read-only flag.
var registryWriteMethods = []string{"Register", "SetCompatibility", "DeleteVersion"}

// guardedRegistry checks writes like guardedKafka, and turns a refused
// password into the question for a new one.
type guardedRegistry struct {
	kafka.Registry
	g guardedKafka // for check()
}

func (r *guardedRegistry) fail(err error) error {
	if !errors.Is(err, kafka.ErrRegistryAuth) {
		return err
	}
	cfg, ok := r.g.m.Connection(r.g.name)
	if !ok || cfg.Params["schema_registry_user"] == "" || urlHasPassword(cfg.Params["schema_registry_url"]) {
		return err // nothing to ask: the password is written in the URL
	}
	m := r.g.m
	m.mu.Lock()
	if m.registries[r.g.name] == r {
		delete(m.registries, r.g.name) // made with the refused password
	}
	m.mu.Unlock()
	return m.refusedSecret(r.g.name, SecretSchemaRegistry)
}

func (r *guardedRegistry) Subjects(ctx context.Context) ([]kafka.Subject, error) {
	out, err := r.Registry.Subjects(ctx)
	return out, r.fail(err)
}

func (r *guardedRegistry) Versions(ctx context.Context, subject string) ([]int, error) {
	out, err := r.Registry.Versions(ctx, subject)
	return out, r.fail(err)
}

func (r *guardedRegistry) Schema(ctx context.Context, subject string, version int) (kafka.Schema, error) {
	out, err := r.Registry.Schema(ctx, subject, version)
	return out, r.fail(err)
}

func (r *guardedRegistry) SchemaByID(ctx context.Context, id int) (kafka.Schema, error) {
	out, err := r.Registry.SchemaByID(ctx, id)
	return out, r.fail(err)
}

func (r *guardedRegistry) Resolve(ctx context.Context, s kafka.Schema) (map[string]kafka.Schema, error) {
	out, err := r.Registry.Resolve(ctx, s)
	return out, r.fail(err)
}

func (r *guardedRegistry) Compatibility(ctx context.Context, subject string) (string, bool, error) {
	level, global, err := r.Registry.Compatibility(ctx, subject)
	return level, global, r.fail(err)
}

func (r *guardedRegistry) Check(ctx context.Context, subject string, s kafka.Schema) (bool, []string, error) {
	ok, msgs, err := r.Registry.Check(ctx, subject, s)
	return ok, msgs, r.fail(err)
}

func (r *guardedRegistry) Register(ctx context.Context, subject string, s kafka.Schema) (kafka.Schema, error) {
	if err := r.g.check(); err != nil {
		return kafka.Schema{}, err
	}
	out, err := r.Registry.Register(ctx, subject, s)
	return out, r.fail(err)
}

func (r *guardedRegistry) SetCompatibility(ctx context.Context, subject, level string) error {
	if err := r.g.check(); err != nil {
		return err
	}
	return r.fail(r.Registry.SetCompatibility(ctx, subject, level))
}

func (r *guardedRegistry) DeleteVersion(ctx context.Context, subject string, version int) error {
	if err := r.g.check(); err != nil {
		return err
	}
	return r.fail(r.Registry.DeleteVersion(ctx, subject, version))
}

// SchemaRegistry returns the connection's Schema Registry client, made on
// first use: the password of schema_registry_user is asked for when
// unknown (*SecretRequiredError), and HTTP goes through the SSH tunnel.
func (m *Manager) SchemaRegistry(ctx context.Context, name string) (kafka.Registry, error) {
	cfg, err := m.kafkaConn(name, m.KafkaOpen != nil)
	if err != nil {
		return nil, err
	}
	raw := cfg.Params["schema_registry_url"]
	if raw == "" {
		return nil, ErrNoRegistry
	}
	m.mu.Lock()
	r, ok := m.registries[name]
	gen := m.kafkaGen[name]
	m.mu.Unlock()
	if ok {
		return r, nil
	}
	rc := kafka.RegistryConfig{
		URL: raw, User: cfg.Params["schema_registry_user"],
		TLSCA: cfg.Params["tls_ca"], TLSCert: cfg.Params["tls_cert"], TLSKey: cfg.Params["tls_key"],
		TLSSkipVerify: param(cfg, "tls_skip_verify"),
	}
	if rc.User != "" && !urlHasPassword(raw) {
		if rc.Password, err = m.secretFor(name, SecretSchemaRegistry); err != nil {
			return nil, err
		}
	}
	if rc.Dial, err = m.dial(ctx, cfg); err != nil {
		return nil, err
	}
	open := m.RegistryOpen
	if open == nil {
		open = kafka.OpenRegistry
	}
	reg, err := open(rc)
	if err != nil {
		return nil, err
	}
	g := &guardedRegistry{Registry: reg, g: guardedKafka{m: m, name: name}}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.registries[name]; ok {
		return existing, nil
	}
	if m.kafkaGen[name] == gen {
		m.registries[name] = g
	}
	return g, nil
}

func urlHasPassword(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return false
	}
	_, ok := u.User.Password()
	return ok
}
