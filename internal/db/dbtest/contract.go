// Package dbtest holds the contract every db.Driver implementation must pass.
package dbtest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/edit"
	"github.com/shepherd-yun/sqlvim/internal/schemadiff"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// Fixture describes the standard fixture and engine-specific bits of the contract.
type Fixture struct {
	NS db.Namespace
	// SleepSQL is a query that runs for at least 30 seconds.
	SleepSQL string
	// OpenReadOnly opens another connection to the same database with ReadOnly set.
	OpenReadOnly func(ctx context.Context) (db.Conn, error)
	// Driver is the driver's name, "mysql" or "postgres".
	Driver string
	// NormTypesSQL creates the table norm_types: columns id (auto-increment
	// primary key), n (DEFAULT 0), flag, name (DEFAULT 'x'), created_at
	// (DEFAULT CURRENT_TIMESTAMP), big, price, body, ci (its own collation),
	// and the table comment "normalized".
	NormTypesSQL []string
	// NormTypes is the type TableSchema must report for each column of norm_types.
	NormTypes map[string]string
	// AutoIncrement lists the auto-incrementing columns of norm_types.
	AutoIncrement []string
	// CICollation is the collation of norm_types.ci.
	CICollation string
	// CompareSQL creates cmp_src and cmp_dst, two tables whose differences
	// the round-trip test syncs: cmp_dst lacks columns email, "Odd Col" and
	// created, has a narrower name (nullable, no comment), a narrower qty with
	// another default, an extra column legacy with an index, an index
	// cmp_dst_name on (name, qty) where cmp_src has cmp_src_name on (name),
	// no unique index cmp_src_email and no table comment.
	CompareSQL []string
}

// RunContract checks conn against the standard fixture created in f.NS.
func RunContract(t *testing.T, conn db.Conn, f Fixture) {
	t.Helper()
	ctx := context.Background()
	ns := f.NS
	d := conn.Dialect()
	ref := func(name string) db.TableRef { return db.TableRef{Namespace: ns, Name: name} }

	t.Run("Query returns columns and text values, NULL distinct from empty", func(t *testing.T) {
		rs, err := conn.Query(ctx, "SELECT id, name, email, note FROM "+d.QualifiedName(ref("users"))+" ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, c := range rs.Columns {
			names = append(names, c.Name)
			if c.DatabaseType == "" {
				t.Errorf("column %s has empty DatabaseType", c.Name)
			}
		}
		if diff := cmp.Diff([]string{"id", "name", "email", "note"}, names); diff != "" {
			t.Fatalf("columns (-want +got):\n%s", diff)
		}
		want := [][]db.Value{
			{{Text: "1"}, {Text: "alice"}, {Text: "a@x.com"}, {Text: "hi"}},
			{{Text: "2"}, {Text: "李雷"}, {Text: ""}, {Null: true}},
			{{Text: "3"}, {Text: "bob"}, {Null: true}, {Text: "multi\nline"}},
		}
		if diff := cmp.Diff(want, rs.Rows); diff != "" {
			t.Fatalf("rows (-want +got):\n%s", diff)
		}
	})

	t.Run("Query error is returned", func(t *testing.T) {
		if _, err := conn.Query(ctx, "SELECT * FROM no_such_table_xyz"); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("syntax errors point into the statement", func(t *testing.T) {
		q := "SELECT *\nFORM " + d.QualifiedName(ref("users"))
		_, err := conn.Query(ctx, q)
		off, ok := db.ErrorOffset(err)
		if !ok || !strings.HasPrefix(q[off:], "FORM") {
			t.Fatalf("Query: offset %d, %v (err %v)", off, ok, err)
		}
		s, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		_, err = s.Exec(ctx, q)
		if off, ok := db.ErrorOffset(err); !ok || !strings.HasPrefix(q[off:], "FORM") {
			t.Fatalf("Session.Exec: offset %d, %v (err %v)", off, ok, err)
		}
	})

	t.Run("Exec reports rows affected", func(t *testing.T) {
		res, err := conn.Exec(ctx, "UPDATE "+d.QualifiedName(ref("users"))+" SET note = 'x' WHERE id = 99")
		if err != nil {
			t.Fatal(err)
		}
		if res.RowsAffected != 0 {
			t.Fatalf("RowsAffected = %d, want 0", res.RowsAffected)
		}
	})

	t.Run("Exec counts matched rows, not changed rows", func(t *testing.T) {
		res, err := conn.Exec(ctx, "UPDATE "+d.QualifiedName(ref("users"))+" SET name = name WHERE id = 1")
		if err != nil {
			t.Fatal(err)
		}
		if res.RowsAffected != 1 {
			t.Fatalf("RowsAffected = %d, want 1: data edits check that each statement found its row", res.RowsAffected)
		}
	})

	t.Run("quoted identifiers and pagination", func(t *testing.T) {
		for _, name := range []string{"order", "MixedCase"} {
			if _, err := conn.Query(ctx, "SELECT * FROM "+d.QualifiedName(ref(name))); err != nil {
				t.Errorf("select from %s: %v", name, err)
			}
		}
		rs, err := conn.Query(ctx, d.Paginate("SELECT id FROM "+d.QualifiedName(ref("users"))+" ORDER BY id", 1, 1))
		if err != nil {
			t.Fatal(err)
		}
		if len(rs.Rows) != 1 || rs.Rows[0][0].Text != "2" {
			t.Fatalf("page rows = %v, want [[2]]", rs.Rows)
		}
	})

	t.Run("Databases and Schemas", func(t *testing.T) {
		dbs, err := conn.Meta().Databases(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(dbs, ns.Database) {
			t.Fatalf("Databases = %v, want it to contain %q", dbs, ns.Database)
		}
		schemas, err := conn.Meta().Schemas(ctx, ns.Database)
		if err != nil {
			t.Fatal(err)
		}
		if ns.Schema == "" {
			if len(schemas) != 0 {
				t.Fatalf("Schemas = %v, want none", schemas)
			}
			return
		}
		if !slices.Contains(schemas, ns.Schema) {
			t.Fatalf("Schemas = %v, want it to contain %q", schemas, ns.Schema)
		}
		for _, s := range schemas {
			if strings.HasPrefix(s, "pg_") || s == "information_schema" {
				t.Errorf("system schema %q should be hidden", s)
			}
		}
	})

	t.Run("Tables lists tables and views", func(t *testing.T) {
		tables, err := conn.Meta().Tables(ctx, ns)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]db.TableKind{}
		for _, tb := range tables {
			got[tb.Name] = tb.Kind
		}
		want := map[string]db.TableKind{
			"users": db.TableKindTable, "orders": db.TableKindTable, "order": db.TableKindTable,
			"MixedCase": db.TableKindTable, "active_users": db.TableKindView,
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatalf("tables (-want +got):\n%s", diff)
		}
	})

	t.Run("Columns", func(t *testing.T) {
		cols, err := conn.Meta().Columns(ctx, ref("users"))
		if err != nil {
			t.Fatal(err)
		}
		if len(cols) != 4 {
			t.Fatalf("got %d columns: %+v", len(cols), cols)
		}
		id, name, email, note := cols[0], cols[1], cols[2], cols[3]
		if id.Name != "id" || !id.PrimaryKey || id.Nullable {
			t.Errorf("id = %+v", id)
		}
		if name.Name != "name" || name.Nullable || name.Comment != "display name" || name.PrimaryKey {
			t.Errorf("name = %+v", name)
		}
		if email.Name != "email" || !email.Nullable {
			t.Errorf("email = %+v", email)
		}
		if note.Default == nil || !strings.Contains(*note.Default, "hi") {
			t.Errorf("note default = %v", note.Default)
		}
		missing, err := conn.Meta().Columns(ctx, ref("no_such_table_xyz"))
		if err != nil || len(missing) != 0 {
			t.Errorf("missing table: cols=%v err=%v, want empty and nil", missing, err)
		}
	})

	t.Run("Indexes", func(t *testing.T) {
		idx, err := conn.Meta().Indexes(ctx, ref("users"))
		if err != nil {
			t.Fatal(err)
		}
		var sawPK, sawEmail bool
		for _, i := range idx {
			if i.Primary {
				sawPK = i.Unique && cmp.Equal(i.Columns, []string{"id"})
			}
			if i.Name == "users_email_key" {
				sawEmail = i.Unique && !i.Primary && cmp.Equal(i.Columns, []string{"email"})
			}
		}
		if !sawPK || !sawEmail {
			t.Fatalf("indexes = %+v", idx)
		}
	})

	t.Run("ForeignKeys", func(t *testing.T) {
		fks, err := conn.Meta().ForeignKeys(ctx, ref("orders"))
		if err != nil {
			t.Fatal(err)
		}
		want := []db.ForeignKey{{
			Name: "orders_user_fk", Columns: []string{"user_id"},
			RefTable: ref("users"), RefColumns: []string{"id"},
		}}
		if diff := cmp.Diff(want, fks); diff != "" {
			t.Fatalf("fks (-want +got):\n%s", diff)
		}
	})

	t.Run("DDL", func(t *testing.T) {
		ddl, err := conn.Meta().DDL(ctx, ref("users"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"CREATE TABLE", "users", "email"} {
			if !strings.Contains(ddl, want) {
				t.Errorf("DDL missing %q:\n%s", want, ddl)
			}
		}
		view, err := conn.Meta().DDL(ctx, ref("active_users"))
		if err != nil || !strings.Contains(view, "VIEW") {
			t.Errorf("view DDL = %q, %v", view, err)
		}
		if _, err := conn.Meta().DDL(ctx, ref("no_such_table_xyz")); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("missing DDL err = %v, want ErrNotFound", err)
		}
	})

	t.Run("TableSchema reads the table with normalized types and defaults", func(t *testing.T) {
		s, err := conn.Meta().TableSchema(ctx, ref("users"))
		if err != nil {
			t.Fatal(err)
		}
		if s.Ref != ref("users") {
			t.Errorf("Ref = %+v", s.Ref)
		}
		var names []string
		for i, c := range s.Columns {
			names = append(names, c.Name)
			if c.Position != i+1 {
				t.Errorf("%s: Position = %d, want %d", c.Name, c.Position, i+1)
			}
		}
		if diff := cmp.Diff([]string{"id", "name", "email", "note"}, names); diff != "" {
			t.Fatalf("columns (-want +got):\n%s", diff)
		}
		if c := s.Columns[1]; c.Comment != "display name" || c.Nullable {
			t.Errorf("name = %+v", c)
		}
		if d := s.Columns[3].Default; d == nil || *d != "'hi'" {
			t.Errorf("note default = %v, want 'hi' quoted", d)
		}
		var email *db.Index
		for i := range s.Indexes {
			if s.Indexes[i].Name == "users_email_key" {
				email = &s.Indexes[i]
			}
		}
		if email == nil || !email.Unique || email.Primary || email.Method != "btree" {
			t.Errorf("users_email_key = %+v", email)
		}
		orders, err := conn.Meta().TableSchema(ctx, ref("orders"))
		if err != nil || len(orders.ForeignKeys) != 1 {
			t.Errorf("orders foreign keys = %+v, %v", orders.ForeignKeys, err)
		}
		if _, err := conn.Meta().TableSchema(ctx, ref("no_such_table_xyz")); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("missing table err = %v, want ErrNotFound", err)
		}

		for _, q := range f.NormTypesSQL {
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		t.Cleanup(func() { _, _ = conn.Exec(ctx, "DROP TABLE "+d.QualifiedName(ref("norm_types"))) })
		n, err := conn.Meta().TableSchema(ctx, ref("norm_types"))
		if err != nil {
			t.Fatal(err)
		}
		types := map[string]string{}
		cols := map[string]db.Column{}
		for _, c := range n.Columns {
			types[c.Name], cols[c.Name] = c.Type, c
		}
		if diff := cmp.Diff(f.NormTypes, types); diff != "" {
			t.Errorf("types (-want +got):\n%s", diff)
		}
		for name, want := range map[string]string{"n": "0", "name": "'x'", "created_at": "CURRENT_TIMESTAMP"} {
			if d := cols[name].Default; d == nil || *d != want {
				t.Errorf("%s default = %v, want %s", name, d, want)
			}
		}
		for _, name := range f.AutoIncrement {
			if c := cols[name]; !c.AutoIncrement || c.Default != nil {
				t.Errorf("%s: AutoIncrement = %v, Default = %v; want true, nil", name, c.AutoIncrement, c.Default)
			}
		}
		for name, c := range cols {
			want := ""
			if name == "ci" {
				want = f.CICollation
			}
			if c.Collation != want {
				t.Errorf("%s collation = %q, want %q", name, c.Collation, want)
			}
		}
		if n.Comment != "normalized" {
			t.Errorf("table comment = %q", n.Comment)
		}
	})

	t.Run("Catalog lists namespaces, tables, columns and routines", func(t *testing.T) {
		c, err := conn.Meta().Catalog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(c.Databases, ns.Database) {
			t.Errorf("Databases = %v, want %q", c.Databases, ns.Database)
		}
		if !slices.Contains(c.Namespaces, ns) {
			t.Errorf("Namespaces = %v, want %v", c.Namespaces, ns)
		}
		for _, n := range c.Namespaces {
			if strings.HasPrefix(n.Schema, "pg_") || n.Schema == "information_schema" ||
				n.Schema == "" && slices.Contains([]string{"mysql", "information_schema", "performance_schema", "sys"}, n.Database) {
				t.Errorf("system namespace %v listed", n)
			}
		}
		kinds := map[string]db.TableKind{}
		for _, tb := range c.Tables {
			if tb.Namespace == ns {
				kinds[tb.Name] = tb.Kind
			}
		}
		want := map[string]db.TableKind{
			"users": db.TableKindTable, "orders": db.TableKindTable, "order": db.TableKindTable,
			"MixedCase": db.TableKindTable, "active_users": db.TableKindView,
		}
		if diff := cmp.Diff(want, kinds); diff != "" {
			t.Errorf("tables (-want +got):\n%s", diff)
		}
		var userCols []string
		for _, col := range c.Columns {
			if col.Table == ref("users") {
				userCols = append(userCols, col.Name)
				if col.Type == "" {
					t.Errorf("column %s has no type", col.Name)
				}
			}
		}
		if diff := cmp.Diff([]string{"id", "name", "email", "note"}, userCols); diff != "" {
			t.Errorf("users columns (-want +got):\n%s", diff)
		}
		var fn *db.Routine
		for i, r := range c.Routines {
			if r.Namespace == ns && r.Name == "add_one" {
				fn = &c.Routines[i]
			}
		}
		if fn == nil || fn.Kind != db.RoutineFunction {
			t.Fatalf("routines = %+v, want function add_one", c.Routines)
		}
		ddl, err := conn.Meta().RoutineDDL(ctx, *fn)
		if err != nil || !strings.Contains(ddl, "add_one") {
			t.Errorf("RoutineDDL = %q, %v", ddl, err)
		}
		missing := *fn
		missing.Name = "no_such_routine_xyz"
		if _, err := conn.Meta().RoutineDDL(ctx, missing); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("missing routine err = %v, want ErrNotFound", err)
		}
	})

	t.Run("Stream delivers columns and can stop early", func(t *testing.T) {
		q := "SELECT id FROM " + d.QualifiedName(ref("users")) + " ORDER BY id"
		var cols []db.ColumnInfo
		var rows [][]db.Value
		err := conn.Stream(ctx, q,
			func(c []db.ColumnInfo) { cols = c },
			func(r []db.Value) error {
				rows = append(rows, r)
				return db.ErrStop
			})
		if err != nil {
			t.Fatal(err)
		}
		if len(cols) != 1 || cols[0].Name != "id" || len(rows) != 1 || rows[0][0].Text != "1" {
			t.Fatalf("cols=%v rows=%v", cols, rows)
		}
		if _, err := conn.Query(ctx, "SELECT 1"); err != nil {
			t.Fatalf("conn unusable after an abandoned stream: %v", err)
		}

		cols = nil
		err = conn.Stream(ctx, "SELECT id, name FROM "+d.QualifiedName(ref("users"))+" WHERE id < 0",
			func(c []db.ColumnInfo) { cols = c },
			func([]db.Value) error { return errors.New("no rows expected") })
		if err != nil || len(cols) != 2 {
			t.Fatalf("empty result: cols=%v err=%v", cols, err)
		}
	})

	t.Run("Session keeps state, survives cancel and early stop", func(t *testing.T) {
		s, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		for _, q := range []string{"CREATE TEMPORARY TABLE sqlvim_tmp (id int)", "INSERT INTO sqlvim_tmp VALUES (1), (2)"} {
			if _, err := s.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		countTmp := func() int {
			t.Helper()
			rs, err := s.Query(ctx, "SELECT id FROM sqlvim_tmp")
			if err != nil {
				t.Fatalf("session lost its state: %v", err)
			}
			return len(rs.Rows)
		}
		if n := countTmp(); n != 2 {
			t.Fatalf("rows = %d", n)
		}

		cctx, cancel := context.WithCancel(ctx)
		time.AfterFunc(300*time.Millisecond, cancel)
		start := time.Now()
		err = s.Stream(cctx, f.SleepSQL, func([]db.ColumnInfo) {}, func([]db.Value) error { return nil })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled stream err = %v", err)
		}
		if el := time.Since(start); el > 10*time.Second {
			t.Fatalf("cancel took %v", el)
		}
		if n := countTmp(); n != 2 {
			t.Fatalf("after cancel rows = %d", n)
		}

		var got int
		err = s.Stream(ctx, "SELECT id FROM sqlvim_tmp ORDER BY id", func([]db.ColumnInfo) {}, func([]db.Value) error {
			got++
			return db.ErrStop
		})
		if err != nil || got != 1 {
			t.Fatalf("early stop: got=%d err=%v", got, err)
		}
		if n := countTmp(); n != 2 {
			t.Fatalf("after early stop rows = %d", n)
		}
		if err := s.Ping(ctx); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Session keeps a transaction open across calls", func(t *testing.T) {
		s, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		users := d.QualifiedName(ref("users"))
		note := func(q db.Querier) string {
			t.Helper()
			rs, err := q.Query(ctx, "SELECT note FROM "+users+" WHERE id = 1")
			if err != nil {
				t.Fatal(err)
			}
			return rs.Rows[0][0].Text
		}
		if _, err := s.Exec(ctx, "BEGIN"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Exec(ctx, "UPDATE "+users+" SET note = 'in tx' WHERE id = 1"); err != nil {
			t.Fatal(err)
		}
		if got := note(s); got != "in tx" {
			t.Fatalf("the session sees its own change: %q", got)
		}
		if got := note(conn); got != "hi" {
			t.Fatalf("other connections do not see uncommitted changes: %q", got)
		}
		if _, err := s.Exec(ctx, "ROLLBACK"); err != nil {
			t.Fatal(err)
		}
		if got := note(s); got != "hi" {
			t.Fatalf("rolled back: %q", got)
		}
	})

	t.Run("pooled queries do not see session state", func(t *testing.T) {
		s, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Exec(ctx, "CREATE TEMPORARY TABLE sqlvim_private (id int)"); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		for range 4 { // touch every pooled connection
			if _, err := conn.Query(ctx, "SELECT * FROM sqlvim_private"); err == nil {
				t.Fatal("a closed session's temporary table leaked into the pool")
			}
		}
	})

	t.Run("ReadOnly connections reject writes on the server", func(t *testing.T) {
		ro, err := f.OpenReadOnly(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ro.Close() }()
		update := "UPDATE " + d.QualifiedName(ref("users")) + " SET note = 'x' WHERE id = 1"
		if _, err := ro.Exec(ctx, update); err == nil {
			t.Fatal("pooled write succeeded on a read-only connection")
		}
		s, err := ro.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		if _, err := s.Exec(ctx, update); err == nil {
			t.Fatal("session write succeeded on a read-only connection")
		}
		if _, err := s.Query(ctx, "SELECT id FROM "+d.QualifiedName(ref("users"))); err != nil {
			t.Fatalf("reads must still work: %v", err)
		}
	})

	sd := sqlx.DialectOf(f.Driver)
	read := func(t *testing.T, q string) *db.ResultSet {
		t.Helper()
		rs, err := conn.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return rs
	}
	apply := func(t *testing.T, stmts []string) error {
		t.Helper()
		s, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		return edit.Apply(ctx, s, sd, stmts)
	}

	t.Run("staged edits apply in one transaction", func(t *testing.T) {
		table := ref("edit_users")
		name := d.QualifiedName(table)
		for _, q := range []string{
			"CREATE TABLE " + name + " (id int PRIMARY KEY, name varchar(50), note varchar(50))",
			"INSERT INTO " + name + " VALUES (1, 'a', NULL), (2, 'b', 'x')",
		} {
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		cols, err := conn.Meta().Columns(ctx, table)
		if err != nil {
			t.Fatal(err)
		}
		idx, err := conn.Meta().Indexes(ctx, table)
		if err != nil {
			t.Fatal(err)
		}
		key := edit.ChooseKey(sd, cols, idx, false)
		if key.Kind != edit.PrimaryKey {
			t.Fatalf("key = %+v", key)
		}
		target := edit.Target{Dialect: sd, Table: table, Key: key}
		all := "SELECT * FROM " + name + " ORDER BY id"

		rs := read(t, all)
		tb := edit.New(rs.Columns, rs.Rows, nil)
		tricky := "it's \\ \"q\" 李\nline"
		_ = tb.SetCell(0, 1, db.Value{Text: tricky})
		_ = tb.SetCell(0, 2, db.Value{Text: ""})
		tb.Delete(1)
		at := tb.InsertAfter(1)
		_ = tb.SetCell(at, 0, db.Value{Text: "3"})
		stmts, _, err := edit.Plan(tb, target)
		if err != nil {
			t.Fatal(err)
		}
		if err := apply(t, stmts); err != nil {
			t.Fatalf("%v\n%s", err, strings.Join(stmts, "\n"))
		}
		want := [][]db.Value{
			{{Text: "1"}, {Text: tricky}, {Text: ""}},
			{{Text: "3"}, {Null: true}, {Null: true}},
		}
		if diff := cmp.Diff(want, read(t, all).Rows); diff != "" {
			t.Fatalf("rows (-want +got):\n%s", diff)
		}

		// A row deleted behind our back: its UPDATE finds nothing, and the
		// UPDATE before it is rolled back too.
		rs = read(t, all)
		tb = edit.New(rs.Columns, rs.Rows, nil)
		_ = tb.SetCell(0, 1, db.Value{Text: "first"})
		_ = tb.SetCell(1, 1, db.Value{Text: "gone"})
		if _, err := conn.Exec(ctx, "DELETE FROM "+name+" WHERE id = 3"); err != nil {
			t.Fatal(err)
		}
		stmts, _, _ = edit.Plan(tb, target)
		var ce *edit.CountError
		if err := apply(t, stmts); !errors.As(err, &ce) || ce.Affected != 0 {
			t.Fatalf("err = %v", err)
		}
		if got := read(t, all).Rows[0][1].Text; got != tricky {
			t.Fatalf("the first UPDATE was rolled back: %q", got)
		}
	})

	t.Run("rows without a key are located by their values or ctid", func(t *testing.T) {
		table := ref("edit_bare")
		name := d.QualifiedName(table)
		for _, q := range []string{
			"CREATE TABLE " + name + " (name varchar(50), n int)",
			"INSERT INTO " + name + " VALUES ('dup', 1), ('dup', 1), ('other', NULL)",
		} {
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		cols, _ := conn.Meta().Columns(ctx, table)
		idx, _ := conn.Meta().Indexes(ctx, table)
		key := edit.ChooseKey(sd, cols, idx, false)
		sel := "SELECT * FROM " + name + " ORDER BY name, n"
		if key.Kind == edit.RowID {
			sel = "SELECT ctid::text, * FROM " + name + " ORDER BY name, n"
		}
		rs := read(t, sel)
		var ids []string
		if key.Kind == edit.RowID {
			for i, row := range rs.Rows {
				ids = append(ids, row[0].Text)
				rs.Rows[i] = row[1:]
			}
			rs.Columns = rs.Columns[1:]
		}
		tb := edit.New(rs.Columns, rs.Rows, ids)
		_ = tb.SetCell(0, 1, db.Value{Text: "2"}) // one of two identical rows
		tb.Delete(2)                              // n IS NULL (MySQL) or its ctid
		stmts, warnings, err := edit.Plan(tb, edit.Target{Dialect: sd, Table: table, Key: key})
		if err != nil || len(warnings) != 1 {
			t.Fatalf("plan: %v %v", err, warnings)
		}
		if err := apply(t, stmts); err != nil {
			t.Fatalf("%v\n%s", err, strings.Join(stmts, "\n"))
		}
		want := [][]db.Value{{{Text: "dup"}, {Text: "1"}}, {{Text: "dup"}, {Text: "2"}}}
		if diff := cmp.Diff(want, read(t, "SELECT name, n FROM "+name+" ORDER BY name, n").Rows); diff != "" {
			t.Fatalf("rows (-want +got):\n%s", diff)
		}
	})

	t.Run("DDLWriter renames, truncates and drops tables and views", func(t *testing.T) {
		w := d.DDL()
		exec := func(q string) {
			t.Helper()
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		kinds := func() map[string]db.TableKind {
			t.Helper()
			tables, err := conn.Meta().Tables(ctx, ns)
			if err != nil {
				t.Fatal(err)
			}
			out := map[string]db.TableKind{}
			for _, tb := range tables {
				out[tb.Name] = tb.Kind
			}
			return out
		}
		table, view := ref("Ops Tmp"), ref("ops_view")
		exec("CREATE TABLE " + d.QualifiedName(table) + " (id int)")
		exec("INSERT INTO " + d.QualifiedName(table) + " VALUES (1), (2)")
		exec("CREATE VIEW " + d.QualifiedName(view) + " AS SELECT 1 AS one")

		exec(w.RenameTable(table, "Ops Tmp2", false))
		exec(w.RenameTable(view, "ops_view2", true))
		table, view = ref("Ops Tmp2"), ref("ops_view2")
		got := kinds()
		if _, old := got["Ops Tmp"]; old || got["Ops Tmp2"] != db.TableKindTable || got["ops_view2"] != db.TableKindView {
			t.Fatalf("after rename: %v", got)
		}

		exec(w.TruncateTable(table))
		rs, err := conn.Query(ctx, "SELECT COUNT(*) FROM "+d.QualifiedName(table))
		if err != nil {
			t.Fatal(err)
		}
		if n := rs.Rows[0][0].Text; n != "0" {
			t.Fatalf("rows after truncate = %s", n)
		}

		exec(w.DropTable(view, true))
		exec(w.DropTable(table, false))
		got = kinds()
		if _, ok := got["Ops Tmp2"]; ok {
			t.Fatalf("table still there: %v", got)
		}
		if _, ok := got["ops_view2"]; ok {
			t.Fatalf("view still there: %v", got)
		}
	})

	t.Run("schema sync round-trips through DiffTable, AlterTable and CreateTable", func(t *testing.T) {
		exec := func(stmts []string) {
			t.Helper()
			for _, q := range stmts {
				if _, err := conn.Exec(ctx, q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
		}
		schema := func(name string) db.TableSchema {
			t.Helper()
			s, err := conn.Meta().TableSchema(ctx, ref(name))
			if err != nil {
				t.Fatal(err)
			}
			return s
		}
		w := d.DDL()
		exec(f.CompareSQL)
		src := schema("cmp_src")

		changes := schemadiff.DiffTable(schema("cmp_dst"), src, nil)
		if len(changes) < 10 {
			t.Fatalf("the fixture yields only %d changes: %+v", len(changes), changes)
		}
		stmts, manual := w.AlterTable(ref("cmp_dst"), changes)
		if len(manual) != 0 {
			t.Fatalf("manual: %+v", manual)
		}
		exec(stmts)
		if left := schemadiff.DiffTable(schema("cmp_dst"), src, nil); len(left) != 0 {
			t.Fatalf("after\n%s\nstill differ: %+v", strings.Join(stmts, ";\n"), left)
		}

		created := src
		created.Ref = ref("cmp_new")
		created.Indexes = slices.Clone(src.Indexes)
		for i := range created.Indexes {
			// PostgreSQL index names are unique per schema: cmp_src has these.
			created.Indexes[i].Name = strings.Replace(created.Indexes[i].Name, "cmp_src", "cmp_new", 1)
		}
		stmts, manual = w.CreateTable(created)
		if len(manual) != 0 {
			t.Fatalf("manual: %+v", manual)
		}
		exec(stmts)
		if left := schemadiff.DiffTable(schema("cmp_new"), src, nil); len(left) != 0 {
			t.Fatalf("after\n%s\nstill differ: %+v", strings.Join(stmts, ";\n"), left)
		}
	})
}
