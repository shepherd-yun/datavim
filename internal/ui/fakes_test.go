package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/session"
)

type fakeConn struct {
	mu      sync.Mutex
	queries []string
	rows    int // rows returned by every Query
}

func (c *fakeConn) Query(_ context.Context, q string, _ ...any) (*db.ResultSet, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = append(c.queries, q)
	rs := &db.ResultSet{Columns: []db.ColumnInfo{{Name: "id"}, {Name: "name"}}}
	for i := range c.rows {
		rs.Rows = append(rs.Rows, []db.Value{{Text: strconv.Itoa(i + 1)}, {Text: fmt.Sprintf("user%d", i+1)}})
	}
	return rs, nil
}

func (c *fakeConn) Stream(ctx context.Context, q string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	rs, err := c.Query(ctx, q)
	if err != nil {
		return err
	}
	onColumns(rs.Columns)
	for _, r := range rs.Rows {
		if err := onRow(r); err != nil {
			if errors.Is(err, db.ErrStop) {
				return nil
			}
			return err
		}
	}
	return nil
}

func (c *fakeConn) lastQuery() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queries) == 0 {
		return ""
	}
	return c.queries[len(c.queries)-1]
}

func (c *fakeConn) Exec(context.Context, string, ...any) (db.ExecResult, error) {
	return db.ExecResult{}, nil
}
func (c *fakeConn) Meta() db.MetaReader { return fakeMeta{} }
func (c *fakeConn) Dialect() db.Dialect { return fakeDialect{} }
func (c *fakeConn) Close() error        { return nil }

type fakeMeta struct{}

func (fakeMeta) Databases(context.Context) ([]string, error)       { return []string{"app"}, nil }
func (fakeMeta) Schemas(context.Context, string) ([]string, error) { return nil, nil }
func (fakeMeta) Tables(context.Context, db.Namespace) ([]db.Table, error) {
	return []db.Table{{Name: "users"}, {Name: "active", Kind: db.TableKindView}}, nil
}

func (fakeMeta) Columns(context.Context, db.TableRef) ([]db.Column, error) {
	return []db.Column{{Name: "id", Type: "bigint", PrimaryKey: true}, {Name: "name", Type: "text", Nullable: true}}, nil
}

func (fakeMeta) Indexes(context.Context, db.TableRef) ([]db.Index, error) {
	return []db.Index{{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true}}, nil
}

func (fakeMeta) ForeignKeys(context.Context, db.TableRef) ([]db.ForeignKey, error) { return nil, nil }

func (fakeMeta) DDL(_ context.Context, t db.TableRef) (string, error) {
	if t.Name == "missing" {
		return "", fmt.Errorf("%s: %w", t.Name, db.ErrNotFound)
	}
	return "CREATE TABLE `users` (\n  `id` bigint\n)", nil
}

type fakeDialect struct{}

func (fakeDialect) QuoteIdent(s string) string { return db.Quote(s, '`') }
func (d fakeDialect) QualifiedName(t db.TableRef) string {
	return d.QuoteIdent(t.Database) + "." + d.QuoteIdent(t.Name)
}

func (fakeDialect) Paginate(q string, limit, offset int) string {
	return db.LimitOffset(q, limit, offset)
}

type fakeBackend struct {
	mu           sync.Mutex
	conns        []config.Connection
	conn         *fakeConn
	needPassword bool
	connErr      error
	passwords    []string
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		conns: []config.Connection{
			{Name: "local", Driver: "mysql", Env: config.EnvDev},
			{Name: "prod", Driver: "postgres", Env: config.EnvProd},
		},
		conn: &fakeConn{rows: 3},
	}
}

func (f *fakeBackend) Connections() []config.Connection { return f.conns }

func (f *fakeBackend) Conn(context.Context, string, string) (db.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connErr != nil {
		return nil, f.connErr
	}
	if f.needPassword {
		return nil, session.ErrPasswordRequired
	}
	return f.conn, nil
}

func (f *fakeBackend) SetPassword(name, password string, save bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passwords = append(f.passwords, fmt.Sprintf("%s:%s:%v", name, password, save))
	f.needPassword = false
	return nil
}

func (f *fakeBackend) setConnErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connErr = err
}

// run executes cmd synchronously and returns its message.
func run(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

type fakeSession struct {
	conn   *fakeConn
	closed bool
}

func (c *fakeConn) Session(context.Context) (db.Session, error) { return &fakeSession{conn: c}, nil }

func (s *fakeSession) Query(ctx context.Context, q string, args ...any) (*db.ResultSet, error) {
	return s.conn.Query(ctx, q, args...)
}

func (s *fakeSession) Exec(ctx context.Context, q string, args ...any) (db.ExecResult, error) {
	return s.conn.Exec(ctx, q, args...)
}

func (s *fakeSession) Stream(ctx context.Context, q string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	return s.conn.Stream(ctx, q, onColumns, onRow)
}

func (s *fakeSession) Ping(context.Context) error { return nil }
func (s *fakeSession) Close() error               { s.closed = true; return nil }
