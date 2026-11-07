package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the database/sql driver

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

const busyTimeoutMillis = 5000

// localEngine opens the file in this process.
type localEngine struct{ db *sql.DB }

func openLocal(ctx context.Context, cfg db.ConnConfig) (engine, error) {
	path, err := expandHome(cfg.Database)
	if err != nil {
		return nil, err
	}
	sqldb, err := sql.Open("sqlite", localDSN(path, cfg.ReadOnly))
	if err != nil {
		return nil, err
	}
	sqldb.SetMaxOpenConns(4)
	if err := sqldb.PingContext(ctx); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("open %s: %w", cfg.Database, err)
	}
	return &localEngine{db: sqldb}, nil
}

func expandHome(path string) (string, error) {
	rest, ok := strings.CutPrefix(path, "~/")
	if !ok {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rest), nil
}

// uriPath escapes what a file: URI gives a meaning to.
func uriPath(path string) string {
	return strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(path)
}

// localDSN opens an existing file only: mode=rw does not create it.
func localDSN(path string, readOnly bool) string {
	q := "?mode=rw"
	if readOnly {
		q = "?mode=ro"
	}
	q += "&_pragma=busy_timeout(" + strconv.Itoa(busyTimeoutMillis) + ")&_pragma=foreign_keys(1)"
	if readOnly {
		q += "&_pragma=query_only(1)"
	}
	return "file:" + uriPath(path) + q
}

func (e *localEngine) close() error { return e.db.Close() }

func (e *localEngine) exec(ctx context.Context, stmt string) (db.ExecResult, error) {
	return localExec(ctx, e.db, stmt)
}

func (e *localEngine) stream(ctx context.Context, stmt string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	return localStream(ctx, e.db, stmt, onColumns, onRow)
}

func (e *localEngine) session(ctx context.Context) (engineSession, error) {
	c, err := e.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	return &localSession{c: c}, nil
}

type sqlRunner interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func localExec(ctx context.Context, r sqlRunner, stmt string) (db.ExecResult, error) {
	res, err := r.ExecContext(ctx, stmt)
	if err != nil {
		return db.ExecResult{}, err
	}
	n, err := res.RowsAffected()
	return db.ExecResult{RowsAffected: n}, err
}

func localStream(ctx context.Context, r sqlRunner, stmt string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rows, err := r.QueryContext(ctx, stmt)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	types, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	cols := make([]db.ColumnInfo, len(types))
	dated := false
	for i, ct := range types {
		t := ct.DatabaseTypeName()
		cols[i] = db.ColumnInfo{Name: ct.Name(), DatabaseType: declType(t)}
		dated = dated || t == "DATE" || t == "DATETIME" || t == "TIMESTAMP"
	}
	if dated && rereadable(stmt) {
		_ = rows.Close()
		again, err := r.QueryContext(ctx, withoutDeclTypes(stmt, len(cols)))
		if err != nil {
			return err
		}
		rows = again // the deferred Close follows the variable
	}
	onColumns(cols)
	dest := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range dest {
		ptrs[i] = &dest[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		row := make([]db.Value, len(dest))
		for i, v := range dest {
			row[i] = cell(v)
		}
		if err := onRow(row); err != nil {
			cancel()
			if errors.Is(err, db.ErrStop) {
				return nil
			}
			return err
		}
	}
	return rows.Err()
}

// rereadable reports a plain read, which can run again inside another SELECT.
func rereadable(stmt string) bool {
	switch strings.ToUpper(sqlx.FirstWord(stmt, sqlx.SQLite)) {
	case "SELECT", "WITH", "VALUES":
		return sqlx.Analyze(stmt, sqlx.SQLite).Kind == sqlx.Read
	}
	return false
}

// withoutDeclTypes reads the same rows with no declared types. modernc turns
// the text of DATE, DATETIME and TIMESTAMP columns into time.Time with no way
// to switch it off, which would rewrite the cell ('2026-01-02' into
// '2026-01-02 00:00:00 +0000 UTC'). Unary + makes each column an expression,
// without changing its value or type. The newline keeps a trailing -- comment
// from swallowing the parenthesis. The CTE's name must not occur in stmt, or
// stmt would read the CTE instead of its table.
func withoutDeclTypes(stmt string, n int) string {
	names, exprs := make([]string, n), make([]string, n)
	for i := range n {
		names[i] = "c" + strconv.Itoa(i)
		exprs[i] = "+" + names[i]
	}
	cte := "_dv"
	for i, lower := 1, strings.ToLower(stmt); strings.Contains(lower, cte); i++ {
		cte = "_dv" + strconv.Itoa(i)
	}
	return "WITH " + cte + "(" + strings.Join(names, ",") + ") AS (" + stmt + "\n) SELECT " + strings.Join(exprs, ",") + " FROM " + cte
}

// cell is a scanned value as a db.Value.
func cell(v any) db.Value {
	switch v := v.(type) {
	case nil:
		return db.Value{Null: true}
	case int64:
		return db.Value{Text: strconv.FormatInt(v, 10)}
	case float64:
		return db.Value{Text: realText(v)}
	case string:
		return db.Value{Text: v}
	case []byte:
		return db.Value{Text: blobText(v), Blob: true}
	case time.Time: // only INSERT … RETURNING of a date column gets here
		return db.Value{Text: v.Format("2006-01-02 15:04:05.999999999Z07:00")}
	}
	return db.Value{Text: fmt.Sprint(v)}
}

// localSession is one reserved connection.
type localSession struct {
	c    *sql.Conn
	tx   sqlx.TxTracker
	inTx bool
	lost error
}

func (s *localSession) exec(ctx context.Context, stmt string) (db.ExecResult, error) {
	if s.lost != nil {
		return db.ExecResult{}, s.lost
	}
	res, err := localExec(ctx, s.c, stmt)
	return res, s.after(ctx, stmt, err)
}

func (s *localSession) stream(ctx context.Context, stmt string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	if s.lost != nil {
		return s.lost
	}
	return s.after(ctx, stmt, localStream(ctx, s.c, stmt, onColumns, onRow))
}

// after follows the transaction, and notices that a failed statement took it
// along, while the user still thinks it is open: sqlite3_interrupt rolls back
// the transaction of an interrupted INSERT, UPDATE or DELETE, and so does a
// conflict under OR ROLLBACK or a full disk.
func (s *localSession) after(ctx context.Context, stmt string, err error) error {
	if err == nil {
		switch s.tx.Effect(stmt, sqlx.SQLite, s.inTx) {
		case sqlx.TxBegin:
			s.inTx = true
		case sqlx.TxEnd:
			s.inTx = false
		case sqlx.TxNone, sqlx.TxResume:
		}
		return nil
	}
	if !s.inTx {
		return err
	}
	bg := context.WithoutCancel(ctx)
	if _, berr := s.c.ExecContext(bg, "BEGIN"); berr != nil { // it is still open
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	_, _ = s.c.ExecContext(bg, "ROLLBACK")
	s.inTx = false
	if ctx.Err() != nil {
		s.lost = fmt.Errorf("%w: the cancelled statement rolled the transaction back", db.ErrConnLost)
		return s.lost
	}
	return fmt.Errorf("%w: %w", err, db.ErrTxRolledBack)
}

func (s *localSession) ping(ctx context.Context) error {
	if s.lost != nil {
		return s.lost
	}
	return s.c.PingContext(ctx)
}

// close discards the connection, so its temporary tables and open transaction
// never reach pooled queries: reporting ErrBadConn from Raw drops it.
func (s *localSession) close() error {
	err := s.c.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) {
		return nil
	}
	return err
}
