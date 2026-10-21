// Package ui is the Bubble Tea application.
package ui

import (
	"context"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/db/tunnel"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
)

// Backend is what the UI needs from the session layer.
type Backend interface {
	Connections() []config.Connection
	Connection(name string) (config.Connection, bool)
	Conn(ctx context.Context, name, database string) (db.Conn, error)
	// Redis returns a client for db number n of a redis connection; n < 0
	// is the configured default.
	Redis(ctx context.Context, name string, n int) (rdb.Client, error)
	// RedisSession opens a new dedicated connection on db n (n < 0 is the
	// configured default) for a console; the caller closes it.
	RedisSession(ctx context.Context, name string, n int) (rdb.Session, error)
	// Kafka returns the client of a kafka connection; its writes refuse
	// on a read-only connection.
	Kafka(ctx context.Context, name string) (kafka.Client, error)
	// KafkaReader opens a message reader for one view; the caller closes it.
	KafkaReader(ctx context.Context, name string, rc kafka.ReaderConfig) (kafka.Reader, error)
	// SchemaRegistry returns a kafka connection's Schema Registry client
	// (session.ErrNoRegistry without schema_registry_url); its writes
	// refuse on a read-only connection.
	SchemaRegistry(ctx context.Context, name string) (kafka.Registry, error)
	// KafkaConnect returns the client of one Kafka Connect cluster of a
	// kafka connection (session.ErrNoConnect for an unknown one); its
	// writes refuse on a read-only connection.
	KafkaConnect(ctx context.Context, conn, cluster string) (kafka.Connect, error)
	SetPassword(name, password string, save bool) error
	// SetSecret remembers and saves a secret other than the password
	// (session.SecretOAuth, session.SecretSchemaRegistry, session.SecretConnect).
	SetSecret(conn, kind, value string) error
	// Reset drops the open connections of name so the next Conn reconnects.
	Reset(name string)
	// AddConnection saves a new connection to connections.toml (and its
	// password to the keychain when save is set).
	AddConnection(c config.Connection, password string, save bool) error
	// UpdateConnection rewrites the connection called old in
	// connections.toml; an empty password keeps the current one.
	UpdateConnection(old string, c config.Connection, password string, save bool) error
	// RemoveConnection deletes a connection and its saved password.
	RemoveConnection(name string) error
	// SetReadOnly switches a connection between read-only and read-write
	// for this run only.
	SetReadOnly(name string, ro bool) error
	// SetPassphrase remembers an SSH key passphrase for this run.
	SetPassphrase(path, passphrase string)
	// TrustHost adds an SSH host key to known_hosts.
	TrustHost(e *tunnel.UnknownHostError) error
}
