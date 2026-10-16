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
// row; the changes were rolled back.
type StatementError struct {
	Index int // 0-based
	SQL   string
	Err   error
	InTx  bool // rolled back to the savepoint: the transaction is still open
}

func (e *StatementError) Error() string {
	if e.InTx {
		return fmt.Sprintf("statement %d: %v · these changes were rolled back, the transaction is still open", e.Index+1, e.Err)
	}
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

// DDLError is a DDL statement that failed. RolledBack says whether the
// statements before it were undone (PostgreSQL) or stay applied (MySQL,
// whose DDL commits implicitly).
type DDLError struct {
	Index      int // 0-based
	SQL        string
	Err        error
	RolledBack bool
}

func (e *DDLError) Error() string {
	switch {
	case e.RolledBack:
		return fmt.Sprintf("statement %d: %v · rolled back, nothing was changed", e.Index+1, e.Err)
	case e.Index == 0:
		return fmt.Sprintf("statement 1: %v · nothing was changed", e.Err)
	}
	return fmt.Sprintf("statement %d: %v · the %d before it were applied", e.Index+1, e.Err, e.Index)
}

func (e *DDLError) Unwrap() error { return e.Err }

// ApplyDDL runs DDL statements on s (a db.Session). PostgreSQL runs them in
// one transaction; MySQL commits each DDL statement implicitly, so they run
// one by one and stop at the first failure. Unlike Apply it does not count
// rows: DDL reports none.
func ApplyDDL(ctx context.Context, s db.Querier, d sqlx.Dialect, stmts []string) error {
	tx := d.Features().TxDDL
	if tx {
		if _, err := s.Exec(ctx, sqlx.BeginSQL(d)); err != nil {
			return fmt.Errorf("begin transaction: %w", err)
		}
	}
	for i, q := range stmts {
		if _, err := s.Exec(ctx, q); err != nil {
			if tx {
				rollback(s)
			}
			return &DDLError{Index: i, SQL: q, Err: err, RolledBack: tx}
		}
	}
	if !tx {
		return nil
	}
	if _, err := s.Exec(ctx, "COMMIT"); err != nil {
		rollback(s)
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Savepoint is where ApplyInTx rolls back to.
const Savepoint = "sqlvim_edit"

// ApplyInTx runs stmts in the transaction already open on s (a db.Session)
// and leaves it open. Like Apply, every statement must change exactly one
// row; otherwise the transaction is rolled back to a savepoint taken
// first, keeping what ran in it before, and a *StatementError names the
// statement.
func ApplyInTx(ctx context.Context, s db.Querier, stmts []string) error {
	if _, err := s.Exec(ctx, "SAVEPOINT "+Savepoint); err != nil {
		return fmt.Errorf("savepoint: %w", err)
	}
	for i, q := range stmts {
		res, err := s.Exec(ctx, q)
		if err == nil && res.RowsAffected != 1 {
			err = &CountError{Affected: res.RowsAffected}
		}
		if err != nil {
			rollbackTo(s)
			return &StatementError{Index: i, SQL: q, Err: err, InTx: true}
		}
	}
	if _, err := s.Exec(ctx, "RELEASE SAVEPOINT "+Savepoint); err != nil {
		rollbackTo(s)
		return fmt.Errorf("release savepoint: %w", err)
	}
	return nil
}

// rollbackTo undoes ApplyInTx's statements even when ctx was cancelled.
// On PostgreSQL it also clears the failed state the error left.
func rollbackTo(s db.Querier) {
	ctx, cancel := context.WithTimeout(context.Background(), rollbackTimeout)
	defer cancel()
	_, _ = s.Exec(ctx, "ROLLBACK TO SAVEPOINT "+Savepoint)
}
