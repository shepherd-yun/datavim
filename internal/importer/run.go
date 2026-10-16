package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

const (
	// PreviewRows is how many rows the import tab shows.
	PreviewRows = 20
	// MaxErrors is how many problems Check reports.
	MaxErrors = 50
	// Savepoint is taken before every batch, so that a rejected batch can
	// be replayed row by row to find the row the server rejects.
	Savepoint = "sqlvim_import"

	cleanupTimeout = 5 * time.Second
)

// Sample is the start of a file.
type Sample struct {
	Header []string
	Rows   [][]string
	Lines  []int
	// BadUTF8: some sampled text is not UTF-8, so the file is likely in
	// another encoding.
	BadUTF8 bool
}

// Read returns the header and the first n rows of path. With scan set it
// reads to the end, passing every row (the first n too) to scan.
func Read(ctx context.Context, path string, o Options, n int, scan func([]string)) (Sample, error) {
	src, err := Open(path, o)
	if err != nil {
		return Sample{}, err
	}
	defer func() { _ = src.Close() }()
	s := Sample{Header: src.Header(), BadUTF8: !validUTF8(src.Header())}
	for len(s.Rows) < n || scan != nil {
		if err := ctx.Err(); err != nil {
			return Sample{}, err
		}
		row, err := src.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Sample{}, err
		}
		if len(s.Rows) < n {
			s.Rows, s.Lines = append(s.Rows, row), append(s.Lines, src.Line())
			s.BadUTF8 = s.BadUTF8 || !validUTF8(row)
		}
		if scan != nil {
			scan(row)
		}
	}
	return s, nil
}

func validUTF8(cells []string) bool {
	for _, c := range cells {
		if !utf8.ValidString(c) {
			return false
		}
	}
	return true
}

// CheckResult is what a dry run found.
type CheckResult struct {
	Rows   int         // rows that would be inserted
	Errors []*RowError // the first MaxErrors problems
	Failed int         // rows with a problem
}

// Check converts every row of path as p says, without a database. done
// counts the rows read.
func Check(ctx context.Context, path string, o Options, p Plan, done *atomic.Int64) (CheckResult, error) {
	src, err := Open(path, o)
	if err != nil {
		return CheckResult{}, err
	}
	defer func() { _ = src.Close() }()
	var r CheckResult
	for {
		if err := ctx.Err(); err != nil {
			return CheckResult{}, err
		}
		row, err := src.Next()
		if errors.Is(err, io.EOF) {
			return r, nil
		}
		if err != nil {
			return CheckResult{}, err
		}
		done.Add(1)
		_, ok, err := p.Row(src.Line(), row)
		var re *RowError
		switch {
		case errors.As(err, &re):
			r.Failed++
			if len(r.Errors) < MaxErrors {
				r.Errors = append(r.Errors, re)
			}
		case err != nil:
			return CheckResult{}, err
		case ok:
			r.Rows++
		}
	}
}

// Job is one import.
type Job struct {
	Path    string
	Options Options
	Plan    Plan
	// Create holds the statements that create the table; none when it exists.
	Create []string
	// Drop removes the created table again after a failure on MySQL, whose
	// CREATE TABLE commits at once.
	Drop string
	// Rows is how many rows Check counted. With a generated column the last
	// number is checked against the column's type before anything is written.
	Rows int
}

// LineError is a batch the server rejected: at Line when replaying the
// batch row by row found the row, else somewhere in lines First…Last.
type LineError struct {
	Line, First, Last int
	Err               error
}

func (e *LineError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %v", e.Line, e.Err)
	}
	return fmt.Sprintf("lines %d–%d: %v", e.First, e.Last, e.Err)
}

func (e *LineError) Unwrap() error { return e.Err }

// LeftoverError is a failed import on MySQL whose new table could not be
// dropped again.
type LeftoverError struct {
	Table   string // as people write it: app.orders
	Err     error  // why the import failed
	DropErr error
}

func (e *LeftoverError) Error() string {
	return fmt.Sprintf("%v · table %s was created but is empty; drop it manually", e.Err, e.Table)
}

func (e *LeftoverError) Unwrap() error { return e.Err }

// Run imports job on s (a db.Session) in one transaction and returns the
// rows inserted; done counts them as batches land. On any failure nothing
// stays: the transaction is rolled back and, on MySQL, a table the job
// created is dropped.
func Run(ctx context.Context, s db.Querier, job Job, done *atomic.Int64) (n int, err error) {
	d := sqlx.DialectOf(job.Plan.Driver)
	created := false
	if d == sqlx.MySQL {
		for _, q := range job.Create {
			if _, err := s.Exec(ctx, q); err != nil {
				return 0, err
			}
			created = true
		}
		defer func() {
			if err == nil || !created || job.Drop == "" {
				return
			}
			if derr := detached(s, job.Drop); derr != nil {
				err = &LeftoverError{Table: displayName(d, job.Plan.Table), Err: err, DropErr: derr}
			}
		}()
	}
	if _, err := s.Exec(ctx, sqlx.BeginSQL(d)); err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	n, err = insertAll(ctx, s, d, job, done)
	if err == nil {
		if _, err = s.Exec(ctx, "COMMIT"); err != nil {
			err = fmt.Errorf("commit: %w", err)
		}
	}
	if err != nil {
		_ = detached(s, "ROLLBACK")
		return 0, err
	}
	return n, nil
}

func insertAll(ctx context.Context, s db.Querier, d sqlx.Dialect, job Job, done *atomic.Int64) (int, error) {
	if d == sqlx.Postgres {
		for _, q := range job.Create {
			if _, err := s.Exec(ctx, q); err != nil {
				return 0, err
			}
		}
	}
	gen := slices.IndexFunc(job.Plan.Columns, func(c Column) bool { return c.Generate })
	var next *big.Int
	if gen >= 0 {
		var err error
		if next, err = firstID(ctx, s, d, job.Plan, gen, job.Rows); err != nil {
			return 0, err
		}
	}
	src, err := Open(job.Path, job.Options)
	if err != nil {
		return 0, err
	}
	defer func() { _ = src.Close() }()
	size := BatchSize(len(job.Plan.Columns))
	var rows [][]Cell
	var lines []int
	n := 0
	flush := func() error {
		if len(rows) == 0 {
			return nil
		}
		if err := insertBatch(ctx, s, job.Plan, rows, lines); err != nil {
			return err
		}
		n += len(rows)
		done.Add(int64(len(rows)))
		rows, lines = rows[:0], lines[:0]
		return nil
	}
	for {
		row, err := src.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
		cells, ok, err := job.Plan.Row(src.Line(), row)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		if gen >= 0 {
			c := job.Plan.Columns[gen]
			if cells[gen], err = Convert(c.Target, next.String(), ""); err != nil {
				return 0, &RowError{Line: src.Line(), Column: c.Target.Name, Value: next.String(), Reason: err.Error()}
			}
			next.Add(next, big.NewInt(1))
		}
		rows, lines = append(rows, cells), append(lines, src.Line())
		if len(rows) == size {
			if err := flush(); err != nil {
				return 0, err
			}
		}
	}
	return n, flush()
}

// firstID locks the table against other inserts until the transaction ends
// and returns the generated column's largest value + 1. The last of rows
// numbers must fit the column's type.
func firstID(ctx context.Context, s db.Querier, d sqlx.Dialect, p Plan, gen, rows int) (*big.Int, error) {
	c := p.Columns[gen].Target
	table := sqlx.QualifiedName(d, p.Table)
	q := "SELECT COALESCE(MAX(" + sqlx.QuoteIdent(d, c.Name) + "), 0) FROM " + table
	if d == sqlx.Postgres {
		if _, err := s.Exec(ctx, "LOCK TABLE "+table+" IN SHARE ROW EXCLUSIVE MODE"); err != nil {
			return nil, err
		}
	} else {
		q += " FOR UPDATE"
	}
	rs, err := s.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	top, ok := new(big.Int), false
	if len(rs.Rows) == 1 && len(rs.Rows[0]) == 1 {
		top, ok = top.SetString(strings.TrimSpace(rs.Rows[0][0].Text), 10)
	}
	if !ok {
		return nil, fmt.Errorf("cannot read the largest %s of %s", c.Name, displayName(d, p.Table))
	}
	last := new(big.Int).Add(top, big.NewInt(int64(max(rows, 1))))
	if _, err := Convert(c, last.String(), ""); err != nil {
		return nil, fmt.Errorf("generated %s would reach %s, which %w", c.Name, last, err)
	}
	return top.Add(top, big.NewInt(1)), nil
}

// insertBatch inserts rows behind a savepoint. When the server rejects
// them it rolls back to the savepoint and replays them one by one to name
// the row it rejects.
func insertBatch(ctx context.Context, s db.Querier, p Plan, rows [][]Cell, lines []int) error {
	if _, err := s.Exec(ctx, "SAVEPOINT "+Savepoint); err != nil {
		return err
	}
	q, args := p.Insert(rows)
	_, err := s.Exec(ctx, q, args...)
	if err == nil {
		_, err = s.Exec(ctx, "RELEASE SAVEPOINT "+Savepoint)
		return err
	}
	if ctx.Err() != nil || db.IsConnLost(err) {
		return err
	}
	whole := &LineError{First: lines[0], Last: lines[len(lines)-1], Err: err}
	if _, rerr := s.Exec(ctx, "ROLLBACK TO SAVEPOINT "+Savepoint); rerr != nil {
		return whole
	}
	for i, row := range rows {
		q, args := p.Insert([][]Cell{row})
		if _, rowErr := s.Exec(ctx, q, args...); rowErr != nil {
			if ctx.Err() != nil || db.IsConnLost(rowErr) {
				return rowErr
			}
			return &LineError{Line: lines[i], Err: rowErr}
		}
	}
	return whole
}

// detached runs q even when the import's context was cancelled.
func detached(s db.Querier, q string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, err := s.Exec(ctx, q)
	return err
}

// displayName writes ref as people do: app.orders.
func displayName(d sqlx.Dialect, ref db.TableRef) string {
	ns := ref.Database
	if d == sqlx.Postgres {
		ns = ref.Schema
	}
	return strings.TrimPrefix(ns+"."+ref.Name, ".")
}
