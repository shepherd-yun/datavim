// Package postgres implements db.Driver for PostgreSQL using pgx.
package postgres

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// Driver opens PostgreSQL connections.
type Driver struct{}

func (Driver) Name() string         { return "postgres" }
func (Driver) DatabaseScoped() bool { return true }

// Open connects and pings the server.
func (Driver) Open(ctx context.Context, cfg db.ConnConfig) (db.Conn, error) {
	pcfg, err := pgxpool.ParseConfig(connString(cfg))
	if err != nil {
		return nil, err
	}
	pcfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &conn{pool: pool, database: pcfg.ConnConfig.Database}, nil
}

func connString(cfg db.ConnConfig) string {
	database := cfg.Database
	if database == "" {
		database = "postgres"
	}
	q := url.Values{}
	q.Set("connect_timeout", "10")
	for k, v := range cfg.Params {
		q.Set(k, v)
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.User, cfg.Password),
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:     "/" + database,
		RawQuery: q.Encode(),
	}
	return u.String()
}

type conn struct {
	pool     *pgxpool.Pool
	database string
}

func (c *conn) Dialect() db.Dialect   { return dialect{} }
func (c *conn) Meta() db.MetaReader   { return meta{c: c} }
func (c *conn) Close() error          { c.pool.Close(); return nil }

func (c *conn) Exec(ctx context.Context, sql string, args ...any) (db.ExecResult, error) {
	tag, err := c.pool.Exec(ctx, sql, args...)
	if err != nil {
		return db.ExecResult{}, err
	}
	return db.ExecResult{RowsAffected: tag.RowsAffected()}, nil
}

// Query asks the server for text-format results so every value is shown
// exactly as psql would print it.
func (c *conn) Query(ctx context.Context, sql string, args ...any) (*db.ResultSet, error) {
	rows, err := c.pool.Query(ctx, sql, append([]any{pgx.QueryResultFormats{pgx.TextFormatCode}}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	typeMap := rows.Conn().TypeMap()
	fields := rows.FieldDescriptions()
	rs := &db.ResultSet{Columns: make([]db.ColumnInfo, len(fields))}
	for i, f := range fields {
		typ := fmt.Sprintf("oid:%d", f.DataTypeOID)
		if t, ok := typeMap.TypeForOID(f.DataTypeOID); ok {
			typ = t.Name
		}
		rs.Columns[i] = db.ColumnInfo{Name: f.Name, DatabaseType: typ}
	}
	for rows.Next() {
		raw := rows.RawValues()
		row := make([]db.Value, len(raw))
		for i, b := range raw {
			if b == nil {
				row[i] = db.Value{Null: true}
			} else {
				row[i] = db.Value{Text: string(b)}
			}
		}
		rs.Rows = append(rs.Rows, row)
	}
	return rs, rows.Err()
}

type dialect struct{}

func (dialect) QuoteIdent(s string) string { return db.Quote(s, '"') }

func (d dialect) QualifiedName(t db.TableRef) string {
	if t.Schema == "" {
		return d.QuoteIdent(t.Name)
	}
	return d.QuoteIdent(t.Schema) + "." + d.QuoteIdent(t.Name)
}

func (dialect) Paginate(sql string, limit, offset int) string {
	return db.LimitOffset(sql, limit, offset)
}
