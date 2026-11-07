package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/db/dbtest"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// fixture is the contract's standard tables; users.id is a rowid alias.
var fixture = []string{
	`CREATE TABLE users (id INTEGER PRIMARY KEY, name VARCHAR(100) NOT NULL, email VARCHAR(100), note TEXT DEFAULT 'hi')`,
	`CREATE UNIQUE INDEX users_email_key ON users (email)`,
	`CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL,
	   CONSTRAINT orders_user_fk FOREIGN KEY (user_id) REFERENCES users (id))`,
	`CREATE TABLE "order" (id INT)`,
	`CREATE TABLE "MixedCase" (id INT)`,
	`CREATE VIEW active_users AS SELECT id, name FROM users`,
	`INSERT INTO users VALUES (1, 'alice', 'a@x.com', 'hi')`,
	`INSERT INTO users VALUES (2, '李雷', '', NULL)`,
	`INSERT INTO users VALUES (3, 'bob', NULL, 'multi' || char(10) || 'line')`,
}

// contractFixture is the Fixture of both engines.
func contractFixture(path string, open func(ctx context.Context, cfg db.ConnConfig) (db.Conn, error)) dbtest.Fixture {
	return dbtest.Fixture{
		Dialect:     sqlx.SQLite,
		SleepSQL:    "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<100000000000) SELECT count(*) FROM c",
		ManyRowsSQL: "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<100000000000) SELECT x FROM c",
		OpenReadOnly: func(ctx context.Context) (db.Conn, error) {
			return open(ctx, db.ConnConfig{Database: path, ReadOnly: true})
		},
		NoComments:   true,
		NoRoutines:   true,
		PartialAlter: true,
		NoViewRename: true,
		DateSQL:      func(s string) string { return "'" + s + "'" },
		NVarcharSQL:  []string{`CREATE TABLE uni (id INTEGER PRIMARY KEY, name TEXT)`},
		NormTypesSQL: []string{`CREATE TABLE norm_types (
			id INTEGER PRIMARY KEY AUTOINCREMENT, n INT DEFAULT 0, flag BOOLEAN, name VARCHAR(20) DEFAULT 'x',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, big BIGINT, price DECIMAL(10,2), body TEXT, ci TEXT COLLATE NOCASE)`},
		NormTypes: map[string]string{
			"id": "integer", "n": "int", "flag": "boolean", "name": "varchar(20)", "created_at": "timestamp",
			"big": "bigint", "price": "decimal(10,2)", "body": "text", "ci": "text",
		},
		AutoIncrement: []string{"id"},
		CICollation:   "NOCASE",
		CompareSQL: []string{
			`CREATE TABLE cmp_src (id INTEGER PRIMARY KEY, name VARCHAR(100) NOT NULL, qty INT DEFAULT 1,
				email VARCHAR(80), "Odd Col" INT, created TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`,
			`CREATE INDEX cmp_src_name ON cmp_src (name)`,
			`CREATE UNIQUE INDEX cmp_src_email ON cmp_src (email)`,
			`CREATE TABLE cmp_dst (id INTEGER PRIMARY KEY, name VARCHAR(50), qty SMALLINT DEFAULT 2, legacy INT)`,
			`CREATE INDEX cmp_dst_legacy ON cmp_dst (legacy)`,
			`CREATE INDEX cmp_dst_name ON cmp_dst (name, qty)`,
		},
		DataCompareSQL: []string{
			`CREATE TABLE dat_src (id INTEGER PRIMARY KEY, name VARCHAR(50) NOT NULL, note TEXT, price DECIMAL(10,2), seen_at TIMESTAMP)`,
			`CREATE TABLE dat_dst (id INTEGER PRIMARY KEY, name VARCHAR(50) NOT NULL, note TEXT, price DECIMAL(10,2), seen_at TIMESTAMP)`,
			`INSERT INTO dat_src VALUES (1, 'same', 'n', 1.50, '2026-01-01 10:00:00'), (2, 'b', '', 2.50, '2026-01-02 10:00:00'),
				(3, 'it''s', 'back\slash', 3.50, '2026-01-03 10:00:00'), (5, 'new', NULL, NULL, NULL)`,
			`INSERT INTO dat_dst VALUES (1, 'same', 'n', 1.50, '2026-01-01 10:00:00'), (2, 'b', NULL, 2.50, '2026-01-02 10:00:00'),
				(3, 'its', 'back\slash', 3.50, '2026-02-03 10:00:00'), (4, 'gone', NULL, NULL, NULL)`,
		},
	}
}

// makeFile creates the database file with the fixture, using the local engine.
func makeFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c, err := Driver{}.Open(ctx, db.ConnConfig{Database: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	for _, q := range fixture {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return path
}

func open(t *testing.T, cfg db.ConnConfig) db.Conn {
	t.Helper()
	c, err := Driver{}.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestLocalContract(t *testing.T) {
	path := makeFile(t)
	c := open(t, db.ConnConfig{Database: path})
	dbtest.RunContract(t, c, contractFixture(path, Driver{}.Open))
}

func TestOpenMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	for _, ro := range []bool{false, true} {
		if c, err := (Driver{}).Open(context.Background(), db.ConnConfig{Database: path, ReadOnly: ro}); err == nil {
			_ = c.Close()
			t.Fatalf("readOnly=%v: opened a missing file", ro)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("readOnly=%v: the file was created: %v", ro, err)
		}
	}
}

func TestOpenNotADatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.db")
	if err := os.WriteFile(path, []byte(strings.Repeat("not a database ", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, err := (Driver{}).Open(context.Background(), db.ConnConfig{Database: path}); err == nil {
		_ = c.Close()
		t.Fatal("opened a file that is no database")
	}
}

func TestOpenTrickyPaths(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a b.db", "a'b.db", "数据.db", "a?b#c%d.db"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		c := open(t, db.ConnConfig{Database: path})
		if _, err := c.Exec(context.Background(), "CREATE TABLE t (a)"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
			t.Fatalf("%s: wrote elsewhere: %v %v", name, fi, err)
		}
	}
}

func TestOpenExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "h.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c := open(t, db.ConnConfig{Database: "~/h.db"})
	if _, err := c.Query(context.Background(), "SELECT 1"); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyRejectsWrites(t *testing.T) {
	ctx := context.Background()
	path := makeFile(t)
	c := open(t, db.ConnConfig{Database: path, ReadOnly: true})
	if _, err := c.Exec(ctx, "INSERT INTO users (name) VALUES ('x')"); err == nil {
		t.Fatal("INSERT succeeded")
	}
	rs, err := c.Query(ctx, "SELECT count(*) FROM users")
	if err != nil || rs.Rows[0][0].Text != "3" {
		t.Fatalf("%v %v", rs, err)
	}
}

func TestForeignKeysOn(t *testing.T) {
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	if _, err := c.Exec(context.Background(), "INSERT INTO orders VALUES (1, 999)"); err == nil {
		t.Fatal("the foreign key was not enforced")
	}
}

func TestBlobValues(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	rs, err := c.Query(ctx, `SELECT x'00ff', 'x''00ff''', x'', 1, 1.5, 2.0, NULL, 9007199254740993`)
	if err != nil {
		t.Fatal(err)
	}
	want := []db.Value{
		{Text: "x'00FF'", Blob: true},
		{Text: "x'00ff'"},
		{Text: "x''", Blob: true},
		{Text: "1"},
		{Text: "1.5"},
		{Text: "2.0"},
		{Null: true},
		{Text: "9007199254740993"},
	}
	if diff := cmp.Diff(want, rs.Rows[0]); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestDateColumnsKeepTheirText(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	for _, q := range []string{
		"CREATE TABLE d (a DATE, b DATETIME, c TIMESTAMP)",
		"INSERT INTO d VALUES ('2026-01-02', '2026-01-02 03:04:05.123456', '2026-01-02T03:04:05Z'), ('tomorrow', 5, x'01')",
	} {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	rs, err := c.Query(ctx, "-- first\nSELECT a, a, b, c FROM d ORDER BY rowid -- last")
	if err != nil {
		t.Fatal(err)
	}
	want := [][]db.Value{
		{{Text: "2026-01-02"}, {Text: "2026-01-02"}, {Text: "2026-01-02 03:04:05.123456"}, {Text: "2026-01-02T03:04:05Z"}},
		{{Text: "tomorrow"}, {Text: "tomorrow"}, {Text: "5"}, {Text: "x'01'", Blob: true}},
	}
	if diff := cmp.Diff(want, rs.Rows); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if got := []string{rs.Columns[0].Name, rs.Columns[1].Name, rs.Columns[2].DatabaseType}; !cmp.Equal(got, []string{"a", "a", "DATETIME"}) {
		t.Fatalf("columns: %+v", rs.Columns)
	}
}

func TestColumnTypes(t *testing.T) {
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	rs, err := c.Query(context.Background(), "SELECT id, name, note, 1+1 AS e FROM users WHERE id < 0")
	if err != nil {
		t.Fatal(err)
	}
	want := []db.ColumnInfo{
		{Name: "id", DatabaseType: "INTEGER"},
		{Name: "name", DatabaseType: "VARCHAR(100)"},
		{Name: "note", DatabaseType: "TEXT"},
		{Name: "e", DatabaseType: "ANY"},
	}
	if diff := cmp.Diff(want, rs.Columns); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestTableNS(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	m := c.Meta()
	if dbs, err := m.Databases(ctx); dbs != nil || err != nil {
		t.Errorf("Databases = %v, %v", dbs, err)
	}
	if ss, err := m.Schemas(ctx, ""); ss != nil || err != nil {
		t.Errorf("Schemas = %v, %v", ss, err)
	}
	if _, err := c.Exec(ctx, "CREATE TABLE auto (id INTEGER PRIMARY KEY AUTOINCREMENT)"); err != nil {
		t.Fatal(err)
	}
	tables, err := m.Tables(ctx, db.Namespace{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range tables {
		if strings.HasPrefix(tb.Name, "sqlite_") {
			t.Errorf("listed %s", tb.Name)
		}
	}
	if _, err := m.RoutineDDL(ctx, db.Routine{Name: "x"}); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("RoutineDDL = %v", err)
	}
	srv := c.Server()
	_, err1 := srv.Info(ctx)
	_, err2 := srv.Sample(ctx)
	_, err3 := srv.Sessions(ctx, true)
	_, err4 := srv.Locks(ctx)
	_, err5 := srv.TopStatements(ctx, 1)
	_, err6 := srv.Settings(ctx)
	_, err7 := srv.Replication(ctx)
	_, err8 := srv.CancelQuery(ctx, db.SessionID{})
	_, err9 := srv.KillSession(ctx, db.SessionID{})
	for i, err := range []error{err1, err2, err3, err4, err5, err6, err7, err8, err9} {
		if !errors.Is(err, db.ErrUnavailable) {
			t.Errorf("server method %d: %v", i, err)
		}
	}
}

func TestPrimaryKeyIndexes(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	for _, q := range []string{
		"CREATE TABLE p (b INT, a INT, PRIMARY KEY (a, b)) WITHOUT ROWID",
		"CREATE TABLE u (a INT UNIQUE, b INT)",
		"CREATE INDEX u_expr ON u (lower(b), a) WHERE a > 0",
	} {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := c.Meta().Indexes(ctx, db.TableRef{Name: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 1 || !idx[0].Primary || !cmp.Equal(idx[0].Columns, []string{"a", "b"}) {
		t.Errorf("p: %+v", idx)
	}
	s, err := c.Meta().TableSchema(ctx, db.TableRef{Name: "u"})
	if err != nil {
		t.Fatal(err)
	}
	var expr *db.Index
	for i := range s.Indexes {
		if s.Indexes[i].Name == "u_expr" {
			expr = &s.Indexes[i]
		}
	}
	if expr == nil || !cmp.Equal(expr.Columns, []string{"lower(b)", "a"}) || expr.Where != "a > 0" {
		t.Errorf("u_expr: %+v", s.Indexes)
	}
}

func TestUntypedColumns(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	if _, err := c.Exec(ctx, "CREATE TABLE bare (a, b)"); err != nil {
		t.Fatal(err)
	}
	cols, err := c.Meta().Columns(ctx, db.TableRef{Name: "bare"})
	if err != nil || cols[0].Type != "ANY" {
		t.Fatalf("%+v %v", cols, err)
	}
	s, err := c.Meta().TableSchema(ctx, db.TableRef{Name: "bare"})
	if err != nil {
		t.Fatal(err)
	}
	s.Ref.Name = "bare2"
	stmts, _ := c.Dialect().DDL().CreateTable(s)
	if strings.Contains(strings.ToLower(stmts[0]), "any") {
		t.Fatalf("an untyped column stays untyped: %s", stmts[0])
	}
}

func TestAlterManual(t *testing.T) {
	ref := db.TableRef{Name: "t"}
	w := dialect{}.DDL()
	a, b := &db.Column{Name: "a", Type: "int", Nullable: true}, &db.Column{Name: "b", Type: "int", Nullable: true}
	nn := &db.Column{Name: "n", Type: "text", Nullable: false, Default: ptr("'x'")}
	changes := []db.SchemaChange{
		{Kind: db.AddColumn, New: nn},
		{Kind: db.RenameColumn, Old: a, New: b},
		{Kind: db.DropColumn, Old: a},
		{Kind: db.DropIndex, OldIndex: &db.Index{Name: "i"}},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "j", Columns: []string{"a", "lower(b)"}, Unique: true, Where: "a > 0"}},
	}
	stmts, manual := w.AlterTable(ref, changes)
	want := []string{
		`ALTER TABLE "t" ADD COLUMN "n" text NOT NULL DEFAULT 'x'`,
		`ALTER TABLE "t" RENAME COLUMN "a" TO "b"`,
		`ALTER TABLE "t" DROP COLUMN "a"`,
		`DROP INDEX "i"`,
		`CREATE UNIQUE INDEX "j" ON "t" ("a", lower(b)) WHERE a > 0`,
	}
	if diff := cmp.Diff(want, stmts); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if len(manual) != 0 {
		t.Errorf("manual: %+v", manual)
	}
	hand := []db.SchemaChange{
		{Kind: db.AlterColumn, Old: a, New: a, Attrs: db.AttrType},
		{Kind: db.AlterColumn, Old: a, New: a, Attrs: db.AttrDefault},
		{Kind: db.ForeignKeyDiff, NewFK: &db.ForeignKey{Name: "f"}},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "p", Columns: []string{"a"}, Unique: true, Primary: true}},
		{Kind: db.DropIndex, OldIndex: &db.Index{Name: "p", Primary: true}},
		{Kind: db.DropIndex, OldIndex: &db.Index{Name: "sqlite_autoindex_t_1", Unique: true, Constraint: true}},
		{Kind: db.AlterTableComment, NewValue: "c"},
	}
	stmts, manual = w.AlterTable(ref, hand)
	if len(stmts) != 0 || len(manual) != len(hand) {
		t.Errorf("stmts %v, manual %d of %d", stmts, len(manual), len(hand))
	}
}

func ptr(s string) *string { return &s }

func TestNormalizeType(t *testing.T) {
	w := dialect{}.DDL()
	for in, want := range map[string]string{"VARCHAR(50)": "varchar(50)", " Text ": "text", "": "any", "UNSIGNED BIG INT": "unsigned big int"} {
		if got := w.NormalizeType(in); got != want {
			t.Errorf("NormalizeType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenameTable(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	w := c.Dialect().DDL()
	if _, err := c.Exec(ctx, w.RenameTable(db.TableRef{Name: "order"}, "order2", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Query(ctx, `SELECT * FROM "order2"`); err != nil {
		t.Fatal(err)
	}
	if got := w.TruncateTable(db.TableRef{Name: "t"}); got != `DELETE FROM "t"` {
		t.Error(got)
	}
	// a view cannot be renamed: the server says so
	if _, err := c.Exec(ctx, w.RenameTable(db.TableRef{Name: "active_users"}, "x", true)); err == nil {
		t.Error("renamed a view")
	}
}

func TestErrPos(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	q := "SELECT 1;\nSELECT 'users' FORM users"
	_, err := c.Query(ctx, q)
	off, ok := db.ErrorOffset(err)
	if !ok || off != strings.LastIndex(q, "users") {
		t.Fatalf("offset %d, %v, err %v", off, ok, err)
	}
	_, err = c.Query(ctx, "SELECT * FROM no_such_table_xyz")
	if err == nil {
		t.Fatal("want an error")
	}
	if _, ok := db.ErrorOffset(err); ok {
		t.Fatalf("no position in %v", err)
	}
}

func TestMultiStatement(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	rs, err := c.Query(ctx, "CREATE TEMP TABLE m (a); INSERT INTO m VALUES (1); SELECT a FROM m;")
	if err != nil || len(rs.Rows) != 1 {
		t.Fatalf("%+v %v", rs, err)
	}
	if rs, err := c.Query(ctx, "-- only a comment"); err != nil || len(rs.Rows) != 0 {
		t.Fatalf("%+v %v", rs, err)
	}
	if _, err := c.Exec(ctx, "  "); err != nil {
		t.Fatal(err)
	}
	res, err := c.Exec(ctx, "UPDATE users SET note = 'a' WHERE id = 1; UPDATE users SET note = 'b' WHERE id < 3")
	if err != nil || res.RowsAffected != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := c.Exec(ctx, "UPDATE users SET note = 'c' WHERE id = 1; bad; UPDATE users SET note = 'd' WHERE id = 1"); err == nil {
		t.Fatal("want an error")
	}
	rs, err = c.Query(ctx, "SELECT note FROM users WHERE id = 1")
	if err != nil || rs.Rows[0][0].Text != "c" {
		t.Fatalf("stopped at the error: %+v %v", rs, err)
	}
}

func TestArgsAreBound(t *testing.T) {
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	rs, err := c.Query(context.Background(), "SELECT name FROM users WHERE id = ? AND name <> ?", 1, "it's")
	if err != nil || rs.Rows[0][0].Text != "alice" {
		t.Fatalf("%+v %v", rs, err)
	}
}

func TestLocalCancelReadInTxKeepsIt(t *testing.T) {
	ctx := context.Background()
	f := contractFixture("", nil)
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	s, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, q := range []string{"BEGIN", "UPDATE users SET note = 'x' WHERE id = 1"} {
		if _, err := s.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(100*time.Millisecond, cancel)
	if _, err := s.Query(cctx, f.SleepSQL); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	rs, err := s.Query(ctx, "SELECT note FROM users WHERE id = 1")
	if err != nil || rs.Rows[0][0].Text != "x" {
		t.Fatalf("the transaction is gone: %+v %v", rs, err)
	}
}

func TestLocalCancelWriteInTxReportsTheRollback(t *testing.T) {
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	s, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, q := range []string{"CREATE TABLE big (x)", "BEGIN", "UPDATE users SET note = 'x' WHERE id = 1"} {
		if _, err := s.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(100*time.Millisecond, cancel)
	_, err = s.Exec(cctx, "INSERT INTO big WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<100000000000) SELECT x FROM c")
	if !errors.Is(err, db.ErrConnLost) {
		t.Fatalf("err = %v, want ErrConnLost: SQLite rolled the transaction back", err)
	}
	if _, err := s.Exec(ctx, "COMMIT"); !errors.Is(err, db.ErrConnLost) {
		t.Fatalf("COMMIT after the loss: %v", err)
	}
	rs, err := c.Query(ctx, "SELECT note FROM users WHERE id = 1")
	if err != nil || rs.Rows[0][0].Text != "hi" {
		t.Fatalf("%+v %v", rs, err)
	}
}

func TestSessionLocked(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the busy timeout")
	}
	ctx := context.Background()
	c := open(t, db.ConnConfig{Database: makeFile(t)})
	holder, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	for _, q := range []string{"BEGIN IMMEDIATE", "UPDATE users SET note = 'x' WHERE id = 1"} {
		if _, err := holder.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	s, err := c.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	start := time.Now()
	_, err = s.Exec(ctx, "UPDATE users SET note = 'y' WHERE id = 2")
	if err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(start); took < 4*time.Second || took > 15*time.Second {
		t.Fatalf("took %v", took)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}
