package edit

import (
	"context"
	"fmt"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

const rollbackTimeout = 5 * time.Second

// StatementError is a statement that failed or did not change exactly one
// row; the transaction was rolled back.
type StatementError struct {
	Index int // 0-based
	SQL   string
	Err   error
}

func (e *StatementError) Error() string {
	return fmt.Sprintf("statement %d: %v · rolled back, nothing was changed", e.Index+1, e.Err)
}

func (e *StatementError) Unwrap() error { return e.Err }

// CountError is a statement that changed a number of rows other than one.
type CountError struct{ Affected int64 }

func (e *CountError) Error() string { return fmt.Sprintf("changed %d rows, expected 1", e.Affected) }

// Apply runs stmts in one transaction on s (a db.Session). Every statement
// must change exactly one row; otherwise, or when one fails, everything is
// rolled back and a *StatementError names the statement.
func Apply(ctx context.Context, s db.Querier, d sqlx.Dialect, stmts []string) error {
	if _, err := s.Exec(ctx, sqlx.BeginSQL(d)); err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	for i, q := range stmts {
		res, err := s.Exec(ctx, q)
		if err == nil && res.RowsAffected != 1 {
			err = &CountError{Affected: res.RowsAffected}
		}
		if err != nil {
			rollback(s)
			return &StatementError{Index: i, SQL: q, Err: err}
		}
	}
	if _, err := s.Exec(ctx, "COMMIT"); err != nil {
		rollback(s)
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// rollback ends the transaction even when ctx was cancelled.
func rollback(s db.Querier) {
	ctx, cancel := context.WithTimeout(context.Background(), rollbackTimeout)
	defer cancel()
	_, _ = s.Exec(ctx, "ROLLBACK")
}
