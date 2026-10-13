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
	// ReadOnly makes the server reject writes for every connection opened.
	ReadOnly bool
}

// Driver opens connections for one database engine.
type Driver interface {
	Name() string
	// DatabaseScoped reports whether a Conn can only see the database it was
	// opened with (true for PostgreSQL, false for MySQL).
	DatabaseScoped() bool
	Open(ctx context.Context, cfg ConnConfig) (Conn, error)
}

// ErrStop, returned by a Stream row callback, abandons the rest of the result.
var ErrStop = errors.New("stop streaming")

// Querier runs SQL. Conn and Session both implement it.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (*ResultSet, error)
	Exec(ctx context.Context, sql string, args ...any) (ExecResult, error)
	// Stream runs a query, calling onColumns once (even for an empty result)
	// and onRow for each row. If onRow returns ErrStop the rest of the result
	// is abandoned and Stream returns nil.
	Stream(ctx context.Context, sql string, onColumns func([]ColumnInfo), onRow func([]Value) error) error
}

// Conn is an open, pooled connection. It is safe for concurrent use.
type Conn interface {
	Querier
	// Session reserves a dedicated server connection (see Session).
	Session(ctx context.Context) (Session, error)
	Meta() MetaReader
	Dialect() Dialect
	Close() error
}

// Session is one dedicated server connection, as used by a console: session
// state (variables, temporary tables, USE, search_path) persists across calls.
// Cancelling the context of a running call stops the statement on the server
// and returns ctx.Err(); the session stays usable. Errors caused by a dead
// connection wrap ErrConnLost. Not safe for concurrent use.
type Session interface {
	Querier
	Ping(ctx context.Context) error
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
