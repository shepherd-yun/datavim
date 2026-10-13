// Package runner checks console statements against the safety rules and
// executes them one by one.
package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// Verdict says whether a batch may run.
type Verdict int

const (
	Allow Verdict = iota
	Reject
	Confirm
)

// Check is the guard's decision for a batch of statements.
type Check struct {
	Verdict Verdict
	Reason  string // Reject: why; Confirm: what is risky
	Expect  string // Confirm: the text the user must type to go on
	SQL     string // the statement that triggered the check
}

// Guard applies the v0.2 safety rules (spec §5.3): writes and DDL are
// rejected on read-only connections; an UPDATE or DELETE without WHERE needs
// its table name typed. A rejection wins over any confirmation.
func Guard(stmts []sqlx.Statement, d sqlx.Dialect, readOnly bool) Check {
	var confirm *Check
	for _, s := range stmts {
		info := sqlx.Analyze(s.Text, d)
		if readOnly && (info.Kind == sqlx.Write || info.Kind == sqlx.DDL) {
			return Check{
				Verdict: Reject, SQL: s.Text,
				Reason: fmt.Sprintf("read-only connection: %s statements are blocked (set readonly = false in connections.toml to allow them)", info.Kind),
			}
		}
		if readOnly && sqlx.UnlocksReadOnly(s.Text, d) {
			return Check{
				Verdict: Reject, SQL: s.Text,
				Reason: "read-only connection: switching the session to read-write is blocked (set readonly = false in connections.toml)",
			}
		}
		if info.MissingWhere && confirm == nil {
			target, expect := info.Target, info.Target
			if target == "" {
				target, expect = "the table", "yes"
			}
			confirm = &Check{
				Verdict: Confirm, SQL: s.Text, Expect: expect,
				Reason: fmt.Sprintf("This statement has no WHERE clause and changes every row of %s.", target),
			}
		}
	}
	if confirm != nil {
		return *confirm
	}
	return Check{Verdict: Allow}
}

// Result is the outcome of one statement.
type Result struct {
	SQL       string
	Rows      *db.ResultSet // nil for statements without a result set
	Truncated bool          // more than maxRows rows existed; only maxRows are kept
	Affected  int64
	Duration  time.Duration
	Err       error
}

// Run executes stmts in order on q, stopping after the first error or once
// ctx is cancelled. Result sets keep at most maxRows rows.
func Run(ctx context.Context, q db.Querier, stmts []sqlx.Statement, d sqlx.Dialect, maxRows int) []Result {
	var out []Result
	for _, s := range stmts {
		if ctx.Err() != nil {
			break
		}
		r := RunOne(ctx, q, s.Text, d, maxRows)
		out = append(out, r)
		if r.Err != nil {
			break
		}
	}
	return out
}

// RunOne executes one statement.
func RunOne(ctx context.Context, q db.Querier, sql string, d sqlx.Dialect, maxRows int) Result {
	start := time.Now()
	r := Result{SQL: sql}
	if sqlx.Analyze(sql, d).ReturnsRows {
		rs := &db.ResultSet{}
		r.Err = q.Stream(ctx, sql,
			func(cols []db.ColumnInfo) { rs.Columns = cols },
			func(row []db.Value) error {
				if len(rs.Rows) >= maxRows {
					r.Truncated = true
					return db.ErrStop
				}
				rs.Rows = append(rs.Rows, row)
				return nil
			})
		if r.Err == nil {
			r.Rows = rs
		}
	} else {
		res, err := q.Exec(ctx, sql)
		r.Affected, r.Err = res.RowsAffected, err
	}
	r.Duration = time.Since(start)
	return r
}
