package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strconv"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// killGrace is how long a killed statement may take to stop before the
// connection is dropped.
const killGrace = 5 * time.Second

type session struct {
	c    *sql.Conn
	pool *sql.DB
	id   int64
}

// Session reserves one connection and remembers its id for KILL QUERY.
func (c *conn) Session(ctx context.Context) (db.Session, error) {
	sc, err := c.db.Conn(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	var id int64
	if err := sc.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		_ = sc.Close()
		return nil, wrap(err)
	}
	return &session{c: sc, pool: c.db, id: id}, nil
}

// kill stops the statement running on the session from another connection.
func (s *session) kill() {
	ctx, cancel := context.WithTimeout(context.Background(), killGrace)
	defer cancel()
	_, _ = s.pool.ExecContext(ctx, "KILL QUERY "+strconv.FormatInt(s.id, 10))
}

// run hides cancellation from the driver (which would close the connection)
// and sends KILL QUERY instead, so session state survives. If the server does
// not stop within killGrace the driver context is cancelled after all.
func (s *session) run(ctx context.Context, fn func(context.Context) error) error {
	dctx, drop := context.WithCancel(context.WithoutCancel(ctx))
	defer drop()
	stop := context.AfterFunc(ctx, func() {
		s.kill()
		time.AfterFunc(killGrace, drop)
	})
	defer stop()
	err := fn(dctx)
	if ctx.Err() != nil {
		return ctx.Err() // SLEEP() returns normally when killed; report the cancel anyway
	}
	return wrap(err)
}

func (s *session) stream(ctx context.Context, query string, args []any,
	onColumns func([]db.ColumnInfo), onRow func([]db.Value) error,
) error {
	return s.run(ctx, func(ctx context.Context) error {
		return stream(ctx, s.c, query, args, onColumns, onRow, s.kill)
	})
}

func (s *session) Query(ctx context.Context, query string, args ...any) (*db.ResultSet, error) {
	return db.Collect(func(onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
		return s.stream(ctx, query, args, onColumns, onRow)
	})
}

func (s *session) Stream(ctx context.Context, query string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	return s.stream(ctx, query, nil, onColumns, onRow)
}

func (s *session) Exec(ctx context.Context, query string, args ...any) (db.ExecResult, error) {
	var res db.ExecResult
	err := s.run(ctx, func(ctx context.Context) error {
		r, err := s.c.ExecContext(ctx, query, args...)
		if err != nil {
			return locate(err, query)
		}
		res.RowsAffected, err = r.RowsAffected()
		return err
	})
	return res, err
}

func (s *session) Ping(ctx context.Context) error { return s.run(ctx, s.c.PingContext) }

// Close discards the connection instead of returning it to the pool, so its
// session state (temporary tables, variables) never leaks into pooled queries.
// Reporting ErrBadConn from Raw is how database/sql is told to drop it.
func (s *session) Close() error {
	err := s.c.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) {
		return nil
	}
	return err
}
