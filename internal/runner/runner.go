// Package runner checks console statements against the safety rules and
// executes them one by one.
package runner

import (
	"context"
	"fmt"
	"strings"
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

// Policy is what the guard knows about the connection.
type Policy struct {
	ReadOnly bool // writes and DDL are rejected
	Prod     bool // writes and DDL need confirming
}

// Check is the guard's decision for a batch of statements.
type Check struct {
	Verdict Verdict
	Reason  string   // Reject: why; Confirm: what is risky
	SQL     string   // the statement that triggered the check
	Title   string   // Confirm: dialog title
	Body    []string // Confirm: dialog text
	Expect  string   // Confirm: the text the user must type; empty means pressing y
}

// Guard applies the safety rules of spec §5.3. A rejection wins over any
// confirmation, and a missing WHERE (type the table name) wins over the prod
// confirmation (press y).
func Guard(stmts []sqlx.Statement, d sqlx.Dialect, p Policy) Check {
	var missing *Check
	var writes []string
	firstWrite := ""
	// Without a server-side read-only mode the client is the only guard:
	// it reads every statement of a batch and blocks what it cannot tell
	// apart from a write (spec: SQL Server and Oracle).
	strict := !d.Features().ServerReadOnly
	for _, s := range stmts {
		infos := []sqlx.Info{sqlx.Analyze(s.Text, d)}
		if strict {
			infos = sqlx.AnalyzeBatch(s.Text, d)
		}
		for _, info := range infos {
			if c, blocked := readOnlyCheck(s.Text, info, strict, d, p); blocked {
				return c
			}
		}
		if p.ReadOnly && sqlx.UnlocksReadOnly(s.Text, d) {
			return Check{
				Verdict: Reject, SQL: s.Text,
				Reason: "read-only connection: switching the session to read-write is blocked (use :set noreadonly)",
			}
		}
		for _, info := range infos {
			write := info.Kind == sqlx.Write || info.Kind == sqlx.DDL
			if info.MissingWhere && missing == nil {
				target, expect := info.Target, info.Target
				if target == "" {
					target, expect = "the table", "yes"
				}
				reason := fmt.Sprintf("This statement has no WHERE clause and changes every row of %s.", target)
				missing = &Check{
					Verdict: Confirm, SQL: s.Text, Expect: expect, Reason: reason,
					Title: "Statement has no WHERE clause", Body: []string{reason, "", s.Text},
				}
			}
			if p.Prod && write {
				if firstWrite == "" {
					firstWrite = s.Text
				}
				text := s.Text
				if info.SQL != "" {
					text = info.SQL
				}
				writes = append(writes, summary(text, info))
			}
		}
	}
	switch {
	case missing != nil && p.Prod:
		missing.Title = "PROD · " + missing.Title
		missing.Body = append(missing.Body, "", "This is a PROD connection.")
		return *missing
	case missing != nil:
		return *missing
	case len(writes) > 0:
		reason := fmt.Sprintf("%d statement(s) will change a PROD database:", len(writes))
		return Check{
			Verdict: Confirm, SQL: firstWrite, Reason: reason, Title: "Write to PROD",
			Body: append([]string{reason, ""}, writes...),
		}
	}
	return Check{Verdict: Allow}
}

// readOnlyCheck rejects a write on a read-only connection and, when strict,
// any statement the dialect's allowlist does not know to be harmless.
func readOnlyCheck(stmt string, info sqlx.Info, strict bool, d sqlx.Dialect, p Policy) (Check, bool) {
	if !p.ReadOnly {
		return Check{}, false
	}
	write := info.Kind == sqlx.Write || info.Kind == sqlx.DDL
	text := stmt
	if info.SQL != "" {
		text = info.SQL
	}
	switch {
	case write && !strict:
		return Check{
			Verdict: Reject, SQL: stmt,
			Reason: fmt.Sprintf("read-only connection: %s statements are blocked (:set noreadonly allows them for this session)", info.Kind),
		}, true
	case write || strict && info.Kind == sqlx.Other && !sqlx.SafeOther(text, d):
		word := strings.ToUpper(sqlx.FirstWord(text, d))
		if write {
			word = info.Kind.String()
		}
		return Check{
			Verdict: Reject, SQL: stmt,
			Reason: fmt.Sprintf("read-only connection: %s statements are blocked on %s (:set noreadonly allows them for this session)", word, d.Driver()),
		}, true
	}
	return Check{}, false
}

// summary is one line of the prod confirmation: kind, table and statement.
func summary(sql string, info sqlx.Info) string {
	s := strings.Join(strings.Fields(sql), " ")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:59]) + "…"
	}
	if info.Target != "" {
		return fmt.Sprintf("%s %s: %s", info.Kind, info.Target, s)
	}
	return fmt.Sprintf("%s: %s", info.Kind, s)
}

// Result is the outcome of one statement.
type Result struct {
	SQL       string
	Start     int           // byte offset of the statement in the text it came from
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
		r.Start = s.Start
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
