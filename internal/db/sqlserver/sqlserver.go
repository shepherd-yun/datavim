// Package sqlserver implements db.Driver for Microsoft SQL Server using
// go-mssqldb.
package sqlserver

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
	"unicode/utf8"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// Driver opens SQL Server connections.
type Driver struct{}

func (Driver) Name() string { return "sqlserver" }

// DatabaseScoped is true: the sys.* catalog views only show the current
// database, so each database gets its own Conn, like PostgreSQL.
func (Driver) DatabaseScoped() bool { return true }

// Open connects and pings the server.
//
// cfg.ReadOnly is not applied on the server: SQL Server has no per-session
// read-only switch (ApplicationIntent=ReadOnly only routes to an
// availability group replica). runner.Guard is the only guard there.
func (Driver) Open(ctx context.Context, cfg db.ConnConfig) (db.Conn, error) {
	s := dsn(cfg)
	connector, err := mssql.NewConnector(s)
	if err != nil {
		return nil, hideDSN(err, s)
	}
	if cfg.Dial != nil {
		connector.Dialer = tunnelDialer{dial: cfg.Dial, host: cfg.Host}
	}
	sqldb := sql.OpenDB(connector)
	sqldb.SetMaxOpenConns(4)
	if err := sqldb.PingContext(ctx); err != nil {
		_ = sqldb.Close()
		return nil, hideDSN(err, s)
	}
	return &conn{db: sqldb}, nil
}

// tunnelDialer dials through an SSH tunnel. As a mssql.HostDialer it makes
// the driver leave the host name to the tunnel instead of resolving it here.
type tunnelDialer struct {
	dial db.DialFunc
	host string
}

func (d tunnelDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.dial(ctx, network, addr)
}

func (d tunnelDialer) HostName() string { return d.host }

// dsn is the driver's URL. cfg.Params pass through as they are (encrypt,
// TrustServerCertificate, tlsmin, …); keys are case-insensitive.
func dsn(cfg db.ConnConfig) string {
	database := cfg.Database
	if database == "" {
		database = "master"
	}
	q := url.Values{}
	q.Set("database", database)
	q.Set("dial timeout", "10")
	q.Set("app name", "datavim")
	for k, v := range cfg.Params {
		q.Set(k, v)
	}
	u := url.URL{
		Scheme:   "sqlserver",
		User:     url.UserPassword(cfg.User, cfg.Password),
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		RawQuery: q.Encode(),
	}
	return u.String()
}

// hideDSN keeps the password out of an error that may quote the DSN.
func hideDSN(err error, dsn string) error {
	u, perr := url.Parse(dsn)
	if perr != nil || u.User == nil {
		return err
	}
	p, ok := u.User.Password()
	if !ok || p == "" || !strings.Contains(err.Error(), p) && !strings.Contains(err.Error(), url.QueryEscape(p)) {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), dsn, "sqlserver://…")
	msg = strings.ReplaceAll(strings.ReplaceAll(msg, url.QueryEscape(p), "***"), p, "***")
	return errors.New(msg)
}

type conn struct{ db *sql.DB }

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

// Exec returns the rows the statement matched: SQL Server counts matched
// rows, as data edits require. After a SET NOCOUNT ON on a session it
// reports 0, which edit.Apply treats as a row not found and rolls back:
// the safe direction.
func (c *conn) Exec(ctx context.Context, query string, args ...any) (db.ExecResult, error) {
	res, err := c.db.ExecContext(ctx, query, args...)
	if err != nil {
		return db.ExecResult{}, wrap(locate(err, query))
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
// sends an attention) so the rest of the result is not read.
func stream(ctx context.Context, q queryer, query string, args []any,
	onColumns func([]db.ColumnInfo), onRow func([]db.Value) error,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return locate(err, query)
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
	return locate(rows.Err(), query)
}

// text formats a scanned value the way it can be written back as a
// literal: times without a T, decimals as the server's digits, binaries
// as 0x hex, GUIDs in their usual byte order.
func text(dbType string, v any) string {
	switch v := v.(type) {
	case []byte:
		switch dbType {
		case "UNIQUEIDENTIFIER":
			var u mssql.UniqueIdentifier
			if err := u.Scan(v); err == nil {
				return u.String()
			}
		case "DECIMAL", "NUMERIC", "MONEY", "SMALLMONEY":
			return string(v)
		}
		if sqlx.IsBinaryType(dbType) || dbType == "TIMESTAMP" || dbType == "ROWVERSION" || !utf8.Valid(v) {
			return "0x" + strings.ToUpper(hex.EncodeToString(v))
		}
		return string(v)
	case string:
		return v
	case bool:
		if v {
			return "1"
		}
		return "0"
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		bits := 64
		if dbType == "REAL" {
			bits = 32
		}
		return strconv.FormatFloat(v, 'g', -1, bits)
	case time.Time:
		switch dbType {
		case "DATE":
			return v.Format("2006-01-02")
		case "TIME":
			return v.Format("15:04:05.9999999")
		case "DATETIMEOFFSET":
			return v.Format("2006-01-02 15:04:05.9999999 -07:00")
		}
		return v.Format("2006-01-02 15:04:05.9999999")
	}
	return fmt.Sprint(v)
}

type dialect struct{}

func (dialect) QuoteIdent(s string) string { return sqlx.QuoteIdent(sqlx.SQLServer, s) }

// QualifiedName is [schema].[table]: a Conn is bound to its database, and
// CREATE VIEW refuses a database prefix.
func (d dialect) QualifiedName(t db.TableRef) string {
	if t.Schema == "" {
		return d.QuoteIdent(t.Name)
	}
	return d.QuoteIdent(t.Schema) + "." + d.QuoteIdent(t.Name)
}

// Paginate needs an ORDER BY for OFFSET … FETCH; without one any order will do.
func (dialect) Paginate(query string, limit, offset int) string {
	if !sqlx.HasOrderBy(query, sqlx.SQLServer) {
		query += " ORDER BY (SELECT NULL)"
	}
	return fmt.Sprintf("%s OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", query, offset, limit)
}
