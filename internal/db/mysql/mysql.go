// Package mysql implements db.Driver for MySQL using go-sql-driver/mysql.
package mysql

import (
	"context"
	"database/sql"
	"encoding/hex"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// Driver opens MySQL connections.
type Driver struct{}

func (Driver) Name() string         { return "mysql" }
func (Driver) DatabaseScoped() bool { return false }

// Open connects and pings the server.
func (Driver) Open(ctx context.Context, cfg db.ConnConfig) (db.Conn, error) {
	mc, err := dsnConfig(cfg)
	if err != nil {
		return nil, err
	}
	connector, err := gomysql.NewConnector(mc)
	if err != nil {
		return nil, err
	}
	sqldb := sql.OpenDB(connector)
	sqldb.SetMaxOpenConns(4)
	if err := sqldb.PingContext(ctx); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	return &conn{db: sqldb}, nil
}

func dsnConfig(cfg db.ConnConfig) (*gomysql.Config, error) {
	mc := gomysql.NewConfig()
	mc.User = cfg.User
	mc.Passwd = cfg.Password
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	mc.DBName = cfg.Database
	mc.InterpolateParams = true
	mc.Timeout = 10 * time.Second
	if len(cfg.Params) == 0 {
		return mc, nil
	}
	// Round-trip through the DSN so special keys (tls, charset, ...) are parsed
	// by the driver exactly as in a hand-written DSN.
	q := url.Values{}
	for k, v := range cfg.Params {
		q.Set(k, v)
	}
	dsn := mc.FormatDSN()
	sep := "?"
	if strings.Contains(dsn[strings.LastIndex(dsn, "/"):], "?") {
		sep = "&"
	}
	return gomysql.ParseDSN(dsn + sep + q.Encode())
}

// displayText shows non-UTF-8 (binary) values as hex.
func displayText(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return "0x" + strings.ToUpper(hex.EncodeToString([]byte(s)))
}

type conn struct{ db *sql.DB }

func (c *conn) Dialect() db.Dialect { return dialect{} }
func (c *conn) Meta() db.MetaReader { return meta{c: c} }
func (c *conn) Close() error        { return c.db.Close() }

func (c *conn) Exec(ctx context.Context, query string, args ...any) (db.ExecResult, error) {
	res, err := c.db.ExecContext(ctx, query, args...)
	if err != nil {
		return db.ExecResult{}, err
	}
	n, err := res.RowsAffected()
	return db.ExecResult{RowsAffected: n}, err
}

func (c *conn) Query(ctx context.Context, query string, args ...any) (*db.ResultSet, error) {
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	rs := &db.ResultSet{Columns: make([]db.ColumnInfo, len(types))}
	for i, ct := range types {
		rs.Columns[i] = db.ColumnInfo{Name: ct.Name(), DatabaseType: ct.DatabaseTypeName()}
	}
	dest := make([]sql.NullString, len(types))
	ptrs := make([]any, len(types))
	for i := range dest {
		ptrs[i] = &dest[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]db.Value, len(dest))
		for i, v := range dest {
			if v.Valid {
				row[i] = db.Value{Text: displayText(v.String)}
			} else {
				row[i] = db.Value{Null: true}
			}
		}
		rs.Rows = append(rs.Rows, row)
	}
	return rs, rows.Err()
}

type dialect struct{}

func (dialect) QuoteIdent(s string) string { return db.Quote(s, '`') }

func (d dialect) QualifiedName(t db.TableRef) string {
	if t.Database == "" {
		return d.QuoteIdent(t.Name)
	}
	return d.QuoteIdent(t.Database) + "." + d.QuoteIdent(t.Name)
}

func (dialect) Paginate(query string, limit, offset int) string {
	return db.LimitOffset(query, limit, offset)
}
