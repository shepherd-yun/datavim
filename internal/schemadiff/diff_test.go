package schemadiff

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

var app = db.Namespace{Database: "app"}

func table(name string, cols []db.Column, idx ...db.Index) db.TableSchema {
	return db.TableSchema{Ref: db.TableRef{Namespace: app, Name: name}, Columns: cols, Indexes: idx}
}

func str(s string) *string { return &s }

// summary is one "Kind name risk" line per change.
func summary(changes []db.SchemaChange) []string {
	var out []string
	for _, c := range changes {
		name := c.Option
		switch {
		case c.New != nil:
			name = c.New.Name
		case c.Old != nil:
			name = c.Old.Name
		case c.NewIndex != nil:
			name = c.NewIndex.Name
		case c.OldIndex != nil:
			name = c.OldIndex.Name
		case c.NewFK != nil:
			name = c.NewFK.Name
		case c.OldFK != nil:
			name = c.OldFK.Name
		}
		out = append(out, fmt.Sprintf("%s %s %d", c.Kind, name, c.Risk))
	}
	return out
}

var pk = db.Index{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true, Method: "btree"}

func TestDiffTableIdentical(t *testing.T) {
	a := table("users", []db.Column{{Name: "id", Type: "bigint"}}, pk)
	if got := DiffTable(a, a, nil); len(got) != 0 {
		t.Fatalf("changes = %v", summary(got))
	}
}

func TestDiffTableColumns(t *testing.T) {
	target := table("users", []db.Column{
		{Name: "id", Type: "bigint"},
		{Name: "name", Type: "varchar(50)", Nullable: true},
		{Name: "legacy", Type: "int", Nullable: true},
	})
	source := table("users", []db.Column{
		{Name: "id", Type: "bigint"},
		{Name: "name", Type: "varchar(100)", Comment: "who"},
		{Name: "email", Type: "varchar(255)", Nullable: true},
	})
	got := DiffTable(target, source, nil)
	want := []string{
		"DropColumn legacy 1", // Destructive
		"AddColumn email 0",
		"AlterColumn name 2", // Lossy: NOT NULL now
	}
	if diff := cmp.Diff(want, summary(got)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if got[1].After != "name" {
		t.Errorf("email goes after %q, want name", got[1].After)
	}
	if a := got[2].Attrs; a != db.AttrType|db.AttrNullable|db.AttrComment {
		t.Errorf("attrs = %b", a)
	}
	if got[2].Old != &target.Columns[1] || got[2].New != &source.Columns[1] {
		t.Error("changes point into target and source")
	}
}

func TestDiffTableRenames(t *testing.T) {
	target := table("users", []db.Column{{Name: "id", Type: "int"}, {Name: "nm", Type: "varchar(10)"}})
	source := table("users", []db.Column{{Name: "id", Type: "int"}, {Name: "name", Type: "varchar(20)"}})
	got := DiffTable(target, source, map[string]string{"nm": "name"})
	if diff := cmp.Diff([]string{"RenameColumn name 0", "AlterColumn name 0"}, summary(got)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if got[0].Old.Name != "nm" || got[1].Old.Name != "nm" || got[1].Attrs != db.AttrType {
		t.Errorf("rename %+v, alter %+v", got[0], got[1])
	}
	plain := DiffTable(target, source, nil)
	// name is NOT NULL without a default: adding it fails on a table with rows
	if diff := cmp.Diff([]string{"DropColumn nm 1", "AddColumn name 2"}, summary(plain)); diff != "" {
		t.Fatalf("without renames (-want +got):\n%s", diff)
	}
}

func TestDiffTableColumnRisks(t *testing.T) {
	base := []db.Column{{Name: "id", Type: "int"}}
	for _, c := range []struct {
		name     string
		old, nw  *db.Column
		wantRisk db.Risk
	}{
		{"add NOT NULL without default", nil, &db.Column{Name: "c", Type: "int"}, db.Lossy},
		{"add NOT NULL with default", nil, &db.Column{Name: "c", Type: "int", Default: str("0")}, 0},
		{"add nullable", nil, &db.Column{Name: "c", Type: "int", Nullable: true}, 0},
		{"add auto increment", nil, &db.Column{Name: "c", Type: "int", AutoIncrement: true}, 0},
		{"add generated", nil, &db.Column{Name: "c", Type: "int", Generated: "id + 1"}, 0},
		{"narrow", &db.Column{Name: "c", Type: "varchar(100)"}, &db.Column{Name: "c", Type: "varchar(50)"}, db.Lossy},
		{"widen", &db.Column{Name: "c", Type: "varchar(50)"}, &db.Column{Name: "c", Type: "varchar(100)"}, 0},
		{"comment only", &db.Column{Name: "c", Type: "int", Comment: "a"}, &db.Column{Name: "c", Type: "int", Comment: "b"}, 0},
		{"drop NOT NULL", &db.Column{Name: "c", Type: "int"}, &db.Column{Name: "c", Type: "int", Nullable: true}, 0},
		{"drop", &db.Column{Name: "c", Type: "int"}, nil, db.Destructive},
	} {
		t.Run(c.name, func(t *testing.T) {
			target, source := table("t", slices.Clone(base)), table("t", slices.Clone(base))
			if c.old != nil {
				target.Columns = append(target.Columns, *c.old)
			}
			if c.nw != nil {
				source.Columns = append(source.Columns, *c.nw)
			}
			got := DiffTable(target, source, nil)
			if len(got) != 1 || got[0].Risk != c.wantRisk {
				t.Fatalf("changes = %v, want one with risk %d", summary(got), c.wantRisk)
			}
		})
	}
}

func TestDiffTablePairsIndexes(t *testing.T) {
	target := table("users", []db.Column{{Name: "id", Type: "int"}}, pk,
		db.Index{Name: "idx_a", Columns: []string{"a"}, Method: "btree"},
		db.Index{Name: "u_b", Columns: []string{"b"}, Unique: true, Method: "btree"},
		db.Index{Name: "old_x", Columns: []string{"x"}, Method: "btree"},
		db.Index{Name: "u_c", Columns: []string{"c"}, Unique: true, Method: "btree"},
		db.Index{Name: "z_old_name", Columns: []string{"z"}, Method: "btree"},
	)
	source := table("users", []db.Column{{Name: "id", Type: "int"}},
		db.Index{Name: "users_pkey", Columns: []string{"id"}, Unique: true, Primary: true, Method: "btree"},
		db.Index{Name: "idx_a", Columns: []string{"a", "b"}, Method: "btree"},
		db.Index{Name: "u_b", Columns: []string{"b"}, Unique: true, Method: "btree"},
		db.Index{Name: "new_y", Columns: []string{"y"}, Unique: true, Method: "btree"},
		db.Index{Name: "z_new_name", Columns: []string{"z"}, Method: "btree"},
	)
	want := []string{
		"DropIndex idx_a 0",
		"DropIndex old_x 0",
		"DropIndex u_c 1", // a unique index goes and nothing replaces it
		"AddIndex idx_a 0",
		"AddIndex new_y 2", // existing rows may hold duplicates
	}
	if diff := cmp.Diff(want, summary(DiffTable(target, source, nil))); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestDiffTableRetargetsIndexNames(t *testing.T) {
	target := table("users_bak", []db.Column{{Name: "id", Type: "int"}},
		db.Index{Name: "users_bak_pkey", Columns: []string{"id"}, Unique: true, Primary: true, Constraint: true})
	source := table("users", []db.Column{{Name: "id", Type: "int"}},
		db.Index{Name: "users_pkey", Columns: []string{"id", "tenant"}, Unique: true, Primary: true, Constraint: true},
		db.Index{Name: "users_email_key", Columns: []string{"email"}, Unique: true, Constraint: true})
	got := DiffTable(target, source, nil)
	want := []string{"DropIndex users_bak_pkey 0", "AddIndex users_bak_pkey 2", "AddIndex users_bak_email_key 2"}
	if diff := cmp.Diff(want, summary(got)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if source.Indexes[1].Name != "users_email_key" {
		t.Error("DiffTable renamed the source's index")
	}
}

func TestDiffTableCommentAndOptions(t *testing.T) {
	target := table("t", nil)
	target.Options = map[string]string{"engine": "MyISAM", "collation": "utf8mb4_bin"}
	source := table("t", nil)
	source.Comment = "people"
	source.Options = map[string]string{"engine": "InnoDB", "collation": "utf8mb4_bin"}
	got := DiffTable(target, source, nil)
	if diff := cmp.Diff([]string{"AlterTableComment  0", "AlterTableOption engine 0"}, summary(got)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if got[0].NewValue != "people" || got[1].OldValue != "MyISAM" || got[1].NewValue != "InnoDB" {
		t.Errorf("changes = %+v", got)
	}
	source.Options = nil // a side without options (PostgreSQL) changes none
	if got := DiffTable(target, source, nil); len(got) != 1 {
		t.Errorf("changes = %v", summary(got))
	}
}

func TestDiffTableForeignKeys(t *testing.T) {
	ref := func(database, name string) db.TableRef {
		return db.TableRef{Namespace: db.Namespace{Database: database}, Name: name}
	}
	target := table("orders", nil)
	target.ForeignKeys = []db.ForeignKey{
		{Name: "fk_user", Columns: []string{"user_id"}, RefTable: ref("app", "users"), RefColumns: []string{"id"}},
		{Name: "fk_gone", Columns: []string{"x"}, RefTable: ref("app", "x"), RefColumns: []string{"id"}},
	}
	source := table("orders", nil)
	source.Ref.Database = "app2" // another namespace: its local references point into app2
	source.ForeignKeys = []db.ForeignKey{
		{Name: "fk_user", Columns: []string{"user_id"}, RefTable: ref("app2", "users"), RefColumns: []string{"id"}},
		{Name: "fk_new", Columns: []string{"y"}, RefTable: ref("app2", "y"), RefColumns: []string{"id"}},
	}
	got := DiffTable(target, source, nil)
	if diff := cmp.Diff([]string{"ForeignKeyDiff fk_new 0", "ForeignKeyDiff fk_gone 0"}, summary(got)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if got[0].OldFK != nil || got[1].NewFK != nil {
		t.Errorf("changes = %+v", got)
	}
}
