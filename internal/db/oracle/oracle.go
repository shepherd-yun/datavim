// Package oracle implements db.Driver for Oracle Database using go-ora, a
// pure Go client: no Oracle Instant Client and no cgo.
package oracle

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	go_ora "github.com/sijms/go-ora/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// Driver opens Oracle connections.
type Driver struct{}

func (Driver) Name() string { return "oracle" }

// DatabaseScoped is true: a connection opens one service, and its catalog
// is that database's.
func (Driver) DatabaseScoped() bool { return true }

// defaultService is the pluggable database of Oracle Database Free.
const defaultService = "FREEPDB1"

// Open connects and pings the server. cfg.Database is the service name.
//
// cfg.ReadOnly is not applied on the server: Oracle's SET TRANSACTION READ
// ONLY lasts one transaction, which pooled queries do not share.
// runner.Guard is the only guard there.
func (Driver) Open(ctx context.Context, cfg db.ConnConfig) (db.Conn, error) {
	connector, ok := go_ora.NewConnector(connURL(cfg)).(*go_ora.OracleConnector)
	if !ok {
		return nil, errors.New("oracle: unexpected connector type")
	}
	if cfg.Dial != nil {
		connector.Dialer(dialer(cfg.Dial))
	}
	sqldb := sql.OpenDB(connector)
	sqldb.SetMaxOpenConns(4)
	if err := sqldb.PingContext(ctx); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	c := &conn{db: sqldb}
	if err := sqldb.QueryRowContext(ctx, "SELECT USER FROM dual").Scan(&c.user); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	return c, nil
}

// dialer opens go-ora's connections through an SSH tunnel.
type dialer db.DialFunc

func (d dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d(ctx, network, addr)
}

// connURL is go-ora's URL: oracle://user:password@host:port/service.
func connURL(cfg db.ConnConfig) string {
	service := cfg.Database
	if service == "" {
		service = defaultService
	}
	q := url.Values{}
	for k, v := range cfg.Params {
		q.Set(k, v)
	}
	u := url.URL{
		Scheme:   "oracle",
		User:     url.UserPassword(cfg.User, cfg.Password),
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:     "/" + service,
		RawQuery: q.Encode(),
	}
	return u.String()
}

type conn struct {
	db   *sql.DB
	user string // the schema unqualified names resolve in
}

func (c *conn) Dialect() db.Dialect { return dialect{} }
func (c *conn) Meta() db.MetaReader { return meta{c: c} }
func (c *conn) Close() error        { return c.db.Close() }

// wrap marks dead-connection errors with db.ErrConnLost.
func wrap(err error) error {
	if err != nil && !errors.Is(err, db.ErrConnLost) && db.IsConnLost(err) {
		return fmt.Errorf("%w: %w", db.ErrConnLost, err)
	}
	return err
}

// prepare drops a statement's trailing semicolon, which Oracle rejects
// (ORA-00911), but keeps the END; of a PL/SQL block and drops the "/"
// after it. start is where the sent text begins in sql.
func prepare(sql string) (string, int) {
	stmts := sqlx.Split(sql, sqlx.Oracle)
	if len(stmts) != 1 {
		return strings.TrimSpace(sql), len(sql) - len(strings.TrimLeft(sql, " \t\r\n"))
	}
	return stmts[0].Text, stmts[0].Start
}

func (c *conn) Exec(ctx context.Context, query string, args ...any) (db.ExecResult, error) {
	q, start := prepare(query)
	res, err := c.db.ExecContext(ctx, q, args...)
	if err != nil {
		return db.ExecResult{}, wrap(locate(err, query, start))
	}
	n, err := res.RowsAffected()
	return db.ExecResult{RowsAffected: n}, err
}

func (c *conn) Query(ctx context.Context, query string, args ...any) (*db.ResultSet, error) {
	rs, err := db.Collect(func(onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
		return stream(ctx, c.db, query, args, onColumns, onRow)
	})
	return rs, wrap(err)
}

func (c *conn) Stream(ctx context.Context, query string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	return wrap(stream(ctx, c.db, query, nil, onColumns, onRow))
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// stream reads rows as text. An onRow error cancels the query (the driver
// sends a break) so the rest of the result is not read.
func stream(ctx context.Context, qr queryer, query string, args []any,
	onColumns func([]db.ColumnInfo), onRow func([]db.Value) error,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	q, start := prepare(query)
	rows, err := qr.QueryContext(ctx, q, args...)
	if err != nil {
		return locate(err, query, start)
	}
	defer func() { _ = rows.Close() }()
	types, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	cols := make([]db.ColumnInfo, len(types))
	for i, ct := range types {
		cols[i] = db.ColumnInfo{Name: ct.Name(), DatabaseType: ct.DatabaseTypeName()}
	}
	onColumns(cols)
	dest := make([]any, len(types))
	ptrs := make([]any, len(types))
	for i := range dest {
		ptrs[i] = &dest[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		row := make([]db.Value, len(dest))
		for i, v := range dest {
			if v == nil {
				row[i] = db.Value{Null: true}
			} else {
				row[i] = db.Value{Text: text(cols[i].DatabaseType, v)}
			}
		}
		if err := onRow(row); err != nil {
			cancel()
			if errors.Is(err, db.ErrStop) {
				return nil
			}
			return err
		}
	}
	return locate(rows.Err(), query, start)
}

// text formats a scanned value so that sqlx.Literal can write it back:
// NUMBER as the server's digits, dates and timestamps in ISO order, RAW and
// BLOB as hex.
func text(dbType string, v any) string {
	switch v := v.(type) {
	case string:
		return v
	case []byte:
		return strings.ToUpper(hex.EncodeToString(v))
	case time.Time:
		switch strings.ToUpper(dbType) {
		case "DATE":
			return v.Format("2006-01-02 15:04:05")
		case "TIMESTAMPTZ", "TIMESTAMPTZ_DTY", "TIMESTAMPELTZ", "TIMESTAMPLTZ_DTY":
			return v.Format("2006-01-02 15:04:05.999999999 -07:00")
		}
		return v.Format("2006-01-02 15:04:05.999999999")
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 32)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		if v {
			return "TRUE"
		}
		return "FALSE"
	}
	return fmt.Sprint(v)
}

type dialect struct{}

func (dialect) QuoteIdent(s string) string { return sqlx.QuoteIdent(sqlx.Oracle, s) }

func (d dialect) QualifiedName(t db.TableRef) string { return sqlx.QualifiedName(sqlx.Oracle, t) }

func (dialect) Paginate(query string, limit, offset int) string {
	return fmt.Sprintf("%s OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", query, offset, limit)
}
