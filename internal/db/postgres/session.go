package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

type session struct{ c *pgx.Conn }

// Session hijacks a pooled connection so its state never returns to the pool.
func (c *conn) Session(ctx context.Context) (db.Session, error) {
	pc, err := c.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &session{c: pc.Hijack()}, nil
}

// run reports cancellation as ctx.Err() and a dead connection as db.ErrConnLost.
func (s *session) run(ctx context.Context, fn func(context.Context) error) error {
	err := fn(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && s.c.IsClosed() {
		return fmt.Errorf("%w: %w", db.ErrConnLost, err)
	}
	return err
}

func (s *session) stream(ctx context.Context, sql string, args []any,
	onColumns func([]db.ColumnInfo), onRow func([]db.Value) error,
) error {
	return s.run(ctx, func(ctx context.Context) error { return stream(ctx, s.c, sql, args, onColumns, onRow) })
}

func (s *session) Query(ctx context.Context, sql string, args ...any) (*db.ResultSet, error) {
	return db.Collect(func(onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
		return s.stream(ctx, sql, args, onColumns, onRow)
	})
}

func (s *session) Stream(ctx context.Context, sql string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	return s.stream(ctx, sql, nil, onColumns, onRow)
}

func (s *session) Exec(ctx context.Context, sql string, args ...any) (db.ExecResult, error) {
	var res db.ExecResult
	err := s.run(ctx, func(ctx context.Context) error {
		tag, err := s.c.Exec(ctx, sql, args...)
		res.RowsAffected = tag.RowsAffected()
		return err
	})
	return res, err
}

func (s *session) Ping(ctx context.Context) error { return s.run(ctx, s.c.Ping) }

func (s *session) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), cancelGrace)
	defer cancel()
	return s.c.Close(ctx)
}
