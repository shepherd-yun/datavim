package oracle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/sijms/go-ora/v2/network"

	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// session is one connection. go-ora commits every statement unless a
// driver transaction is open, so a transaction the console starts (SET
// TRANSACTION, or the BeginSQL of staged edits) opens one, and COMMIT or
// ROLLBACK end it through the driver.
type session struct {
	c  *sql.Conn
	tx *sql.Tx
}

// Session reserves one connection. Cancelling a call makes the driver send
// a break: the server stops the statement and the session lives on.
func (c *conn) Session(ctx context.Context) (db.Session, error) {
	sc, err := c.db.Conn(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	return &session{c: sc}, nil
}

// querier is where statements run: the open transaction, else the connection.
func (s *session) querier() interface {
	queryer
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
} {
	if s.tx != nil {
		return s.tx
	}
	return s.c
}

// run reports cancellation as ctx.Err() and a dead connection as db.ErrConnLost.
func (s *session) run(ctx context.Context, fn func(context.Context) error) error {
	err := fn(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var oe *network.OracleError
	if errors.As(err, &oe) && oe.Bad() {
		return errors.Join(db.ErrConnLost, err)
	}
	return wrap(err)
}

func (s *session) Query(ctx context.Context, query string, args ...any) (*db.ResultSet, error) {
	return db.Collect(func(onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
		return s.run(ctx, func(ctx context.Context) error { return stream(ctx, s.querier(), query, args, onColumns, onRow) })
	})
}

func (s *session) Stream(ctx context.Context, query string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	return s.run(ctx, func(ctx context.Context) error { return stream(ctx, s.querier(), query, nil, onColumns, onRow) })
}

func (s *session) Exec(ctx context.Context, query string, args ...any) (db.ExecResult, error) {
	var res db.ExecResult
	err := s.run(ctx, func(ctx context.Context) error {
		q, start := prepare(query)
		switch sqlx.TransactionEffect(q, sqlx.Oracle) {
		case sqlx.TxBegin:
			if s.tx == nil {
				// not ctx: database/sql rolls a transaction back when the
				// context it began with ends
				tx, err := s.c.BeginTx(context.WithoutCancel(ctx), nil)
				if err != nil {
					return err
				}
				s.tx = tx
			}
		case sqlx.TxEnd:
			if s.tx != nil {
				tx := s.tx
				s.tx = nil
				if sqlx.FirstWord(q, sqlx.Oracle) == "COMMIT" || sqlx.FirstWord(q, sqlx.Oracle) == "commit" {
					return tx.Commit()
				}
				return tx.Rollback()
			}
		case sqlx.TxNone, sqlx.TxResume:
		}
		r, err := s.querier().ExecContext(ctx, q, args...)
		if err != nil {
			return locate(err, query, start)
		}
		if s.tx != nil && sqlx.Analyze(q, sqlx.Oracle).Kind == sqlx.DDL {
			// DDL committed the transaction: leave the driver's too
			tx := s.tx
			s.tx = nil
			_ = tx.Commit()
		}
		res.RowsAffected, err = r.RowsAffected()
		return err
	})
	return res, err
}

func (s *session) Ping(ctx context.Context) error { return s.run(ctx, s.c.PingContext) }

// Close rolls back an open transaction and discards the connection, so its
// session state (ALTER SESSION, private temporary tables) never leaks into
// pooled queries. Reporting ErrBadConn from Raw tells database/sql to drop it.
func (s *session) Close() error {
	if s.tx != nil {
		_ = s.tx.Rollback()
		s.tx = nil
	}
	err := s.c.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) {
		return nil
	}
	return err
}
