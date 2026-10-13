// Package db is the only seam between sqlvim and database servers.
package db

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a requested object does not exist.
var ErrNotFound = errors.New("not found")

// ConnConfig is what a driver needs to open a connection.
type ConnConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
	Params   map[string]string
}

// Driver opens connections for one database engine.
type Driver interface {
	Name() string
	// DatabaseScoped reports whether a Conn can only see the database it was
	// opened with (true for PostgreSQL, false for MySQL).
	DatabaseScoped() bool
	Open(ctx context.Context, cfg ConnConfig) (Conn, error)
}

// Conn is an open, pooled connection. It is safe for concurrent use.
type Conn interface {
	Query(ctx context.Context, sql string, args ...any) (*ResultSet, error)
	Exec(ctx context.Context, sql string, args ...any) (ExecResult, error)
	Meta() MetaReader
	Dialect() Dialect
	Close() error
}

// MetaReader reads catalog information. Per-table methods return empty
// results for a missing table, except DDL which returns ErrNotFound.
type MetaReader interface {
	Databases(ctx context.Context) ([]string, error)
	// Schemas returns nil for engines without schemas (MySQL).
	Schemas(ctx context.Context, database string) ([]string, error)
	Tables(ctx context.Context, ns Namespace) ([]Table, error)
	Columns(ctx context.Context, t TableRef) ([]Column, error)
	Indexes(ctx context.Context, t TableRef) ([]Index, error)
	ForeignKeys(ctx context.Context, t TableRef) ([]ForeignKey, error)
	DDL(ctx context.Context, t TableRef) (string, error)
}

// Dialect hides SQL syntax differences between engines.
type Dialect interface {
	QuoteIdent(s string) string
	QualifiedName(t TableRef) string
	Paginate(sql string, limit, offset int) string
}
