// Package dbtest holds the contract every db.Driver implementation must pass.
package dbtest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// RunContract checks conn against the standard fixture created in ns.
func RunContract(t *testing.T, conn db.Conn, ns db.Namespace) {
	t.Helper()
	ctx := context.Background()
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

	t.Run("Exec reports rows affected", func(t *testing.T) {
		res, err := conn.Exec(ctx, "UPDATE "+d.QualifiedName(ref("users"))+" SET note = 'x' WHERE id = 99")
		if err != nil {
			t.Fatal(err)
		}
		if res.RowsAffected != 0 {
			t.Fatalf("RowsAffected = %d, want 0", res.RowsAffected)
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
}
