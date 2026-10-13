// Package postgres implements db.Driver for PostgreSQL using pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// Driver opens PostgreSQL connections.
type Driver struct{}

func (Driver) Name() string         { return "postgres" }
func (Driver) DatabaseScoped() bool { return true }

// cancelGrace is how long a cancelled statement may take to stop before the
// connection is dropped.
const cancelGrace = 5 * time.Second

// Open connects and pings the server.
func (Driver) Open(ctx context.Context, cfg db.ConnConfig) (db.Conn, error) {
	pcfg, err := poolConfig(cfg)
	if err != nil {
		return nil, err
	}
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

func poolConfig(cfg db.ConnConfig) (*pgxpool.Config, error) {
	pcfg, err := pgxpool.ParseConfig(connString(cfg))
	if err != nil {
		return nil, err
	}
	pcfg.MaxConns = 4
	cc := pcfg.ConnConfig
	// Cancelling a context sends a cancel request and keeps the connection,
	// instead of pgx's default of closing it.
	cc.BuildContextWatcherHandler = func(pc *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: pc, DeadlineDelay: cancelGrace}
	}
	if cfg.ReadOnly {
		if cc.RuntimeParams == nil {
			cc.RuntimeParams = map[string]string{}
		}
		cc.RuntimeParams["default_transaction_read_only"] = "on"
	}
	return pcfg, nil
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

func (c *conn) Dialect() db.Dialect { return dialect{} }
func (c *conn) Meta() db.MetaReader { return meta{c: c} }
func (c *conn) Close() error        { c.pool.Close(); return nil }

func (c *conn) Exec(ctx context.Context, sql string, args ...any) (db.ExecResult, error) {
	tag, err := c.pool.Exec(ctx, sql, args...)
	if err != nil {
		return db.ExecResult{}, err
	}
	return db.ExecResult{RowsAffected: tag.RowsAffected()}, nil
}

type pgxQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (c *conn) Query(ctx context.Context, sql string, args ...any) (*db.ResultSet, error) {
	return db.Collect(func(onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
		return stream(ctx, c.pool, sql, args, onColumns, onRow)
	})
}

func (c *conn) Stream(ctx context.Context, sql string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	return stream(ctx, c.pool, sql, nil, onColumns, onRow)
}

// stream asks the server for text-format results so every value is shown
// exactly as psql would print it.
func stream(ctx context.Context, q pgxQuerier, sql string, args []any,
	onColumns func([]db.ColumnInfo), onRow func([]db.Value) error,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rows, err := q.Query(ctx, sql, append([]any{pgx.QueryResultFormats{pgx.TextFormatCode}}, args...)...)
	if err != nil {
		return err
	}
	defer rows.Close()

	typeMap := rows.Conn().TypeMap()
	fields := rows.FieldDescriptions()
	cols := make([]db.ColumnInfo, len(fields))
	for i, f := range fields {
		typ := fmt.Sprintf("oid:%d", f.DataTypeOID)
		if t, ok := typeMap.TypeForOID(f.DataTypeOID); ok {
			typ = t.Name
		}
		cols[i] = db.ColumnInfo{Name: f.Name, DatabaseType: typ}
	}
	onColumns(cols)
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
		if err := onRow(row); err != nil {
			if errors.Is(err, db.ErrStop) {
				cancel() // abandon the rest instead of reading it
				return nil
			}
			return err
		}
	}
	return rows.Err()
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
