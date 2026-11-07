// Package sqlite is the driver for SQLite files, local or on an SSH server.
// The layer here (Conn, Session, MetaReader, Dialect, DDLWriter) only knows
// the engine interface: modernc.org/sqlite for local files, a sqlite3 process
// next to the file for remote ones.
package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// anyType is the DatabaseType of a column without a declared type, whose
// cells can hold anything.
const anyType = "ANY"

// runner executes one statement.
type runner interface {
	stream(ctx context.Context, stmt string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error
	exec(ctx context.Context, stmt string) (db.ExecResult, error)
}

// engine runs statements on a pool of connections to one file.
type engine interface {
	runner
	session(ctx context.Context) (engineSession, error)
	close() error
}

// engineSession is one dedicated connection.
type engineSession interface {
	runner
	ping(ctx context.Context) error
	close() error
}

// Driver opens SQLite files.
type Driver struct{}

func (Driver) Name() string { return "sqlite" }

// DatabaseScoped is true: a Conn is one file.
func (Driver) DatabaseScoped() bool { return true }

// FileBased marks the driver as a db.FileDriver.
func (Driver) FileBased() {}

// Open opens the existing file cfg.Database and fails if there is none: never
// creates one, so a mistyped path cannot produce an empty database.
func (Driver) Open(ctx context.Context, cfg db.ConnConfig) (db.Conn, error) {
	if cfg.Database == "" {
		return nil, errors.New("sqlite: a file path is required")
	}
	var eng engine
	var err error
	if cfg.Exec != nil {
		eng, err = openCLI(ctx, cfg)
	} else {
		eng, err = openLocal(ctx, cfg)
	}
	if err != nil {
		return nil, err
	}
	c := &conn{eng: eng}
	// reads the file's header, so a file that is no database fails here
	if _, err := c.Query(ctx, "SELECT 1 FROM sqlite_master LIMIT 1"); err != nil {
		_ = eng.close()
		return nil, err
	}
	return c, nil
}

type conn struct{ eng engine }

func (c *conn) Dialect() db.Dialect     { return dialect{} }
func (c *conn) Meta() db.MetaReader     { return meta{c: c} }
func (c *conn) Server() db.ServerReader { return server{} }
func (c *conn) Close() error            { return c.eng.close() }

func (c *conn) Query(ctx context.Context, query string, args ...any) (*db.ResultSet, error) {
	return db.Collect(func(onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
		return c.streamArgs(ctx, query, args, onColumns, onRow)
	})
}

func (c *conn) Stream(ctx context.Context, query string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	return c.streamArgs(ctx, query, nil, onColumns, onRow)
}

func (c *conn) streamArgs(ctx context.Context, query string, args []any, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	p, err := prepare(query, args)
	if err != nil {
		return err
	}
	if len(p.stmts) <= 1 {
		return streamAll(ctx, c.eng, p, onColumns, onRow)
	}
	s, err := c.eng.session(ctx) // several statements share one connection
	if err != nil {
		return wrap(err)
	}
	defer func() { _ = s.close() }()
	return streamAll(ctx, s, p, onColumns, onRow)
}

func (c *conn) Exec(ctx context.Context, query string, args ...any) (db.ExecResult, error) {
	p, err := prepare(query, args)
	if err != nil {
		return db.ExecResult{}, err
	}
	if len(p.stmts) <= 1 {
		return execAll(ctx, c.eng, p)
	}
	s, err := c.eng.session(ctx)
	if err != nil {
		return db.ExecResult{}, wrap(err)
	}
	defer func() { _ = s.close() }()
	return execAll(ctx, s, p)
}

func (c *conn) Session(ctx context.Context) (db.Session, error) {
	s, err := c.eng.session(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	return &session{s: s}, nil
}

// session keeps a console's state on one engine connection.
type session struct{ s engineSession }

func (s *session) Query(ctx context.Context, query string, args ...any) (*db.ResultSet, error) {
	return db.Collect(func(onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
		p, err := prepare(query, args)
		if err != nil {
			return err
		}
		return streamAll(ctx, s.s, p, onColumns, onRow)
	})
}

func (s *session) Stream(ctx context.Context, query string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	p, err := prepare(query, nil)
	if err != nil {
		return err
	}
	return streamAll(ctx, s.s, p, onColumns, onRow)
}

func (s *session) Exec(ctx context.Context, query string, args ...any) (db.ExecResult, error) {
	p, err := prepare(query, args)
	if err != nil {
		return db.ExecResult{}, err
	}
	return execAll(ctx, s.s, p)
}

func (s *session) Ping(ctx context.Context) error { return wrap(s.s.ping(ctx)) }
func (s *session) Close() error                   { return s.s.close() }

// prepared is a query cut into statements. Without arguments the offsets of
// errors point into the text the caller has.
type prepared struct {
	stmts []sqlx.Statement
	bound bool
}

func prepare(query string, args []any) (prepared, error) {
	bound := len(args) > 0
	if bound {
		var err error
		if query, err = bindArgs(query, args); err != nil {
			return prepared{}, err
		}
	}
	return prepared{stmts: sqlx.Split(query, sqlx.SQLite), bound: bound}, nil
}

// fail places and marks an error of statement st.
func (p prepared) fail(ctx context.Context, err error, st sqlx.Statement) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, db.ErrConnLost):
		return err
	case ctx.Err() != nil:
		return ctx.Err()
	case !p.bound:
		err = locate(err, st)
	}
	return wrap(err)
}

// execAll runs every statement and reports the last one's result; it stops
// at the first error.
func execAll(ctx context.Context, r runner, p prepared) (db.ExecResult, error) {
	var res db.ExecResult
	for _, st := range p.stmts {
		var err error
		if res, err = r.exec(ctx, st.Text); err != nil {
			return db.ExecResult{}, p.fail(ctx, err, st)
		}
		if sqlx.Analyze(st.Text, sqlx.SQLite).Kind != sqlx.Write {
			res.RowsAffected = 0 // changes() still holds the last write's count
		}
	}
	return res, nil
}

// streamAll runs every statement and reads the last one's rows.
func streamAll(ctx context.Context, r runner, p prepared, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	if len(p.stmts) == 0 {
		onColumns(nil)
		return nil
	}
	last := len(p.stmts) - 1
	for _, st := range p.stmts[:last] {
		if _, err := r.exec(ctx, st.Text); err != nil {
			return p.fail(ctx, err, st)
		}
	}
	return p.fail(ctx, r.stream(ctx, p.stmts[last].Text, onColumns, onRow), p.stmts[last])
}

// wrap marks dead-connection errors with db.ErrConnLost.
func wrap(err error) error {
	if err == nil || errors.Is(err, db.ErrConnLost) || !db.IsConnLost(err) {
		return err
	}
	return fmt.Errorf("%w: %w", db.ErrConnLost, err)
}

// declType is the type reported for a declared type; "" has none.
func declType(t string) string {
	if t = strings.ToUpper(strings.TrimSpace(t)); t != "" {
		return t
	}
	return anyType
}
