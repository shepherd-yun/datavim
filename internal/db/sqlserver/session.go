package sqlserver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"

	"github.com/shepherd-yun/datavim/internal/db"
)

type session struct{ c *sql.Conn }

// Session reserves one connection. Cancelling a call makes the driver send
// an attention: the server stops the statement and the session lives on.
func (c *conn) Session(ctx context.Context) (db.Session, error) {
	sc, err := c.db.Conn(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	return &session{c: sc}, nil
}

// run reports cancellation as ctx.Err() and a dead connection as db.ErrConnLost.
func (s *session) run(ctx context.Context, fn func(context.Context) error) error {
	err := fn(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return wrap(err)
}

func (s *session) Query(ctx context.Context, query string, args ...any) (*db.ResultSet, error) {
	return db.Collect(func(onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
		return s.run(ctx, func(ctx context.Context) error { return stream(ctx, s.c, query, args, onColumns, onRow) })
	})
}

func (s *session) Stream(ctx context.Context, query string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	return s.run(ctx, func(ctx context.Context) error { return stream(ctx, s.c, query, nil, onColumns, onRow) })
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
// session state (#temp tables, USE, an open transaction) never leaks into
// pooled queries. Reporting ErrBadConn from Raw tells database/sql to drop it.
func (s *session) Close() error {
	err := s.c.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) {
		return nil
	}
	return err
}
