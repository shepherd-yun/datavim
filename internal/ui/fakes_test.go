package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/db/tunnel"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/session"
)

type fakeConn struct {
	mu         sync.Mutex
	queries    []string
	execs      []string
	rows       int              // rows returned by every Query
	fail       map[string]error // statement text -> error
	pingErr    error            // returned by every session Ping
	sessions   []*fakeSession
	catalogs   int              // Catalog calls
	catalogErr error            // returned by Catalog
	noKey      bool             // Columns reports no primary key and Indexes none
	affected   map[string]int64 // statement text -> RowsAffected (default 1)
}

func (c *fakeConn) setNoKey(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.noKey = v
}

func (c *fakeConn) keyless() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.noKey
}

// slowQuery blocks until its context is cancelled.
const slowQuery = "select sleep"

func (c *fakeConn) Query(ctx context.Context, q string, _ ...any) (*db.ResultSet, error) {
	c.mu.Lock()
	c.queries = append(c.queries, q)
	err, rows := c.fail[q], c.rows
	c.mu.Unlock()
	if q == slowQuery {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	rs := &db.ResultSet{Columns: []db.ColumnInfo{{Name: "id"}, {Name: "name"}}}
	for i := range rows {
		rs.Rows = append(rs.Rows, []db.Value{{Text: strconv.Itoa(i + 1)}, {Text: fmt.Sprintf("user%d", i+1)}})
	}
	if strings.HasPrefix(q, "SELECT ctid::text, *") {
		rs.Columns = append([]db.ColumnInfo{{Name: "ctid"}}, rs.Columns...)
		for i := range rs.Rows {
			rs.Rows[i] = append([]db.Value{{Text: fmt.Sprintf("(0,%d)", i+1)}}, rs.Rows[i]...)
		}
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

func (c *fakeConn) Exec(_ context.Context, q string, _ ...any) (db.ExecResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.execs = append(c.execs, q)
	if err := c.fail[q]; err != nil {
		return db.ExecResult{}, err
	}
	if n, ok := c.affected[q]; ok {
		return db.ExecResult{RowsAffected: n}, nil
	}
	return db.ExecResult{RowsAffected: 1}, nil
}

func (c *fakeConn) setAffected(q string, n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.affected == nil {
		c.affected = map[string]int64{}
	}
	c.affected[q] = n
}

func (c *fakeConn) execList() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.execs)
}

func (c *fakeConn) setFail(q string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail == nil {
		c.fail = map[string]error{}
	}
	c.fail[q] = err
}

func (c *fakeConn) Session(context.Context) (db.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := &fakeSession{conn: c}
	c.sessions = append(c.sessions, s)
	return s, nil
}

func (c *fakeConn) sessionCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sessions)
}
func (c *fakeConn) Meta() db.MetaReader { return fakeMeta{c: c} }

func (c *fakeConn) catalogCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.catalogs
}
func (c *fakeConn) Dialect() db.Dialect { return fakeDialect{} }
func (c *fakeConn) Close() error        { return nil }

type fakeMeta struct{ c *fakeConn }

func (fakeMeta) Databases(context.Context) ([]string, error)       { return []string{"app"}, nil }
func (fakeMeta) Schemas(context.Context, string) ([]string, error) { return nil, nil }
func (fakeMeta) Tables(context.Context, db.Namespace) ([]db.Table, error) {
	return []db.Table{{Name: "users"}, {Name: "active", Kind: db.TableKindView}}, nil
}

func (m fakeMeta) Columns(context.Context, db.TableRef) ([]db.Column, error) {
	if m.c.keyless() {
		return []db.Column{{Name: "id", Type: "bigint"}, {Name: "name", Type: "text", Nullable: true}}, nil
	}
	return []db.Column{{Name: "id", Type: "bigint", PrimaryKey: true}, {Name: "name", Type: "text", Nullable: true}}, nil
}

func (m fakeMeta) Indexes(context.Context, db.TableRef) ([]db.Index, error) {
	if m.c.keyless() {
		return nil, nil
	}
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

func (d fakeDialect) DDL() db.DDLWriter { return fakeDDL{d} }

type fakeDDL struct{ d fakeDialect }

func (w fakeDDL) DropTable(t db.TableRef, view bool) string {
	if view {
		return "DROP VIEW " + w.d.QualifiedName(t)
	}
	return "DROP TABLE " + w.d.QualifiedName(t)
}

func (w fakeDDL) RenameTable(t db.TableRef, newName string, _ bool) string {
	to := t
	to.Name = newName
	return "RENAME TABLE " + w.d.QualifiedName(t) + " TO " + w.d.QualifiedName(to)
}

func (w fakeDDL) TruncateTable(t db.TableRef) string { return "TRUNCATE TABLE " + w.d.QualifiedName(t) }

type fakeBackend struct {
	mu           sync.Mutex
	conns        []config.Connection
	conn         *fakeConn
	needPassword bool
	connErr      error
	passwords    []string
	resets       []string
	added        []config.Connection
	addErr       error
	lostConns    int
	passphrases  []string
	trusted      []string
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

func (f *fakeBackend) Connections() []config.Connection {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.conns)
}

func (f *fakeBackend) Connection(name string) (config.Connection, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		if c.Name == name {
			return c, true
		}
	}
	return config.Connection{}, false
}

func (f *fakeBackend) Reset(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resets = append(f.resets, name)
}

func (f *fakeBackend) AddConnection(c config.Connection, _ string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return f.addErr
	}
	c, err := config.Normalize(c)
	if err != nil {
		return err
	}
	f.added = append(f.added, c)
	f.conns = append(f.conns, c)
	return nil
}

func (f *fakeBackend) Conn(context.Context, string, string) (db.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lostConns > 0 {
		f.lostConns--
		return nil, fmt.Errorf("%w: connection reset by peer", db.ErrConnLost)
	}
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

func (s *fakeSession) Query(ctx context.Context, q string, args ...any) (*db.ResultSet, error) {
	return s.conn.Query(ctx, q, args...)
}

func (s *fakeSession) Exec(ctx context.Context, q string, args ...any) (db.ExecResult, error) {
	return s.conn.Exec(ctx, q, args...)
}

func (s *fakeSession) Stream(ctx context.Context, q string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	return s.conn.Stream(ctx, q, onColumns, onRow)
}

func (s *fakeSession) Ping(context.Context) error {
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()
	return s.conn.pingErr
}

func (s *fakeSession) Close() error {
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()
	s.closed = true
	return nil
}

type fakeStore struct {
	mu      sync.Mutex
	entries []history.Entry
	drafts  map[string]string
	recents []history.Recent // oldest first
}

func newFakeStore() *fakeStore { return &fakeStore{drafts: map[string]string{}} }

func (s *fakeStore) AddHistory(e history.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
	return nil
}

func (s *fakeStore) History(_ string, limit int) ([]history.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []history.Entry
	for i := len(s.entries) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.entries[i])
	}
	return out, nil
}

func (s *fakeStore) SaveDraft(key, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drafts[key] = text
	return nil
}

func (s *fakeStore) Draft(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drafts[key], nil
}

func (s *fakeStore) draft(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drafts[key]
}

func (f *fakeBackend) setLost(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lostConns = n
}

func (f *fakeBackend) resetList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.resets)
}

func (f *fakeBackend) SetReadOnly(name string, ro bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.conns {
		if f.conns[i].Name == name {
			f.conns[i].ReadOnly = &ro
			return nil
		}
	}
	return fmt.Errorf("unknown connection %q", name)
}

func (f *fakeBackend) SetPassphrase(path, passphrase string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.passphrases = append(f.passphrases, path+":"+passphrase)
	f.connErr = nil
}

func (f *fakeBackend) TrustHost(e *tunnel.UnknownHostError) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trusted = append(f.trusted, e.Host)
	f.connErr = nil
	return nil
}

func (f *fakeBackend) UpdateConnection(old string, c config.Connection, password string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return f.addErr
	}
	c, err := config.Normalize(c)
	if err != nil {
		return err
	}
	for i := range f.conns {
		if f.conns[i].Name == old {
			f.conns[i] = c
			if password != "" {
				f.passwords = append(f.passwords, c.Name+":"+password)
			}
			return nil
		}
	}
	return fmt.Errorf("unknown connection %q", old)
}

func (f *fakeBackend) RemoveConnection(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return f.addErr
	}
	n := len(f.conns)
	f.conns = slices.DeleteFunc(f.conns, func(c config.Connection) bool { return c.Name == name })
	if len(f.conns) == n {
		return fmt.Errorf("unknown connection %q", name)
	}
	return nil
}

func (m fakeMeta) Catalog(context.Context) (db.Catalog, error) {
	m.c.mu.Lock()
	defer m.c.mu.Unlock()
	m.c.catalogs++
	if m.c.catalogErr != nil {
		return db.Catalog{}, m.c.catalogErr
	}
	return fakeCatalog(), nil
}

func (fakeMeta) RoutineDDL(_ context.Context, r db.Routine) (string, error) {
	return "CREATE FUNCTION " + r.Name + "() RETURNS int RETURN 1", nil
}

// fakeCatalog is what fakeMeta.Catalog returns: the MySQL-style database app.
func fakeCatalog() db.Catalog {
	app := db.Namespace{Database: "app"}
	users := db.TableRef{Namespace: app, Name: "users"}
	return db.Catalog{
		Databases:  []string{"app"},
		Namespaces: []db.Namespace{app},
		Tables: []db.CatalogTable{
			{Namespace: app, Name: "active", Kind: db.TableKindView},
			{Namespace: app, Name: "order"},
			{Namespace: app, Name: "users"},
		},
		Columns: []db.CatalogColumn{
			{Table: users, Name: "id", Type: "bigint"},
			{Table: users, Name: "name", Type: "text"},
			{Table: users, Name: "email", Type: "text"},
		},
		Routines: []db.Routine{{Namespace: app, Name: "add_one", Kind: db.RoutineFunction}},
	}
}

func (s *fakeStore) AddRecent(r history.Recent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recents = slices.DeleteFunc(s.recents, func(o history.Recent) bool {
		return o.Kind == r.Kind && o.Conn == r.Conn && o.Database == r.Database && o.Schema == r.Schema && o.Name == r.Name
	})
	s.recents = append(s.recents, r)
	return nil
}

func (s *fakeStore) Recent(limit int) ([]history.Recent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []history.Recent
	for i := len(s.recents) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.recents[i])
	}
	return out, nil
}

// The system clipboard is never touched by tests: copies are dropped
// unless a test captures them, and reads answer nothing unless a test
// fakes the clipboard.
func init() {
	copyToClipboard = func(string) tea.Cmd { return nil }
	readClipboard = func(func(string) tea.Msg) tea.Cmd { return nil }
}

// fakeClipboard makes clipboard reads return text.
func fakeClipboard(t *testing.T, text string) {
	t.Helper()
	old := readClipboard
	readClipboard = func(done func(string) tea.Msg) tea.Cmd {
		return func() tea.Msg { return done(text) }
	}
	t.Cleanup(func() { readClipboard = old })
}
