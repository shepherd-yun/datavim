package schemadiff

import (
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
)

func TestDescribe(t *testing.T) {
	x := "'x'"
	users := db.TableRef{Namespace: app, Name: "users"}
	people := db.TableRef{Namespace: app, Name: "people"}
	for _, c := range []struct {
		change db.SchemaChange
		want   string
	}{
		{
			db.SchemaChange{Kind: db.AddColumn, New: &db.Column{Name: "email", Type: "varchar(255)", Nullable: true}},
			"+ column email  varchar(255) NULL",
		},
		{
			db.SchemaChange{Kind: db.DropColumn, Old: &db.Column{Name: "legacy", Type: "int", Default: &x}},
			"- column legacy  int NOT NULL DEFAULT 'x'",
		},
		{
			db.SchemaChange{Kind: db.AddColumn, New: &db.Column{Name: "id", Type: "bigint", AutoIncrement: true}},
			"+ column id  bigint NOT NULL auto increment",
		},
		{
			db.SchemaChange{
				Kind: db.AlterColumn, Old: &db.Column{Name: "name", Type: "varchar(50)", Nullable: true},
				New: &db.Column{Name: "name", Type: "varchar(100)"}, Attrs: db.AttrType | db.AttrNullable,
			},
			"~ column name  varchar(50) NULL → varchar(100) NOT NULL",
		},
		{
			db.SchemaChange{
				Kind: db.AlterColumn, Old: &db.Column{Name: "n", Type: "int"},
				New: &db.Column{Name: "n", Type: "int", Default: &x, Comment: "who"}, Attrs: db.AttrDefault | db.AttrComment,
			},
			"~ column n  no default comment '' → DEFAULT 'x' comment 'who'",
		},
		{
			db.SchemaChange{
				Kind: db.AlterColumn, Old: &db.Column{Name: "c", Type: "int"},
				New:   &db.Column{Name: "c", Type: "int", Collation: "C", AutoIncrement: true, Generated: "a + 1"},
				Attrs: db.AttrCollation | db.AttrAutoIncrement | db.AttrGenerated,
			},
			"~ column c  default collation no auto increment not generated → COLLATE C auto increment AS (a + 1)",
		},
		{
			db.SchemaChange{Kind: db.RenameColumn, Old: &db.Column{Name: "nm"}, New: &db.Column{Name: "nick"}},
			"~ column nm → nick  (rename)",
		},
		{
			db.SchemaChange{Kind: db.AddIndex, NewIndex: &db.Index{Name: "u_email", Columns: []string{"email"}, Unique: true, Method: "btree"}},
			"+ index u_email (email) unique",
		},
		{
			db.SchemaChange{Kind: db.AddIndex, NewIndex: &db.Index{Name: "tags", Columns: []string{"tags"}, Method: "gin", Where: "x > 0"}},
			"+ index tags (tags) gin WHERE x > 0",
		},
		{
			db.SchemaChange{Kind: db.DropIndex, OldIndex: &db.Index{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true}},
			"- primary key (id)",
		},
		{db.SchemaChange{Kind: db.AlterTableComment, NewValue: "people"}, "~ comment '' → 'people'"},
		{
			db.SchemaChange{Kind: db.AlterTableOption, Option: "engine", OldValue: "MyISAM", NewValue: "InnoDB"},
			"~ engine MyISAM → InnoDB",
		},
		{
			db.SchemaChange{Kind: db.ForeignKeyDiff, NewFK: &db.ForeignKey{Name: "fk", Columns: []string{"user_id"}, RefTable: users, RefColumns: []string{"id"}}},
			"+ foreign key fk (user_id) REFERENCES users (id)",
		},
		{
			db.SchemaChange{
				Kind:  db.ForeignKeyDiff,
				OldFK: &db.ForeignKey{Name: "fk", Columns: []string{"user_id"}, RefTable: users, RefColumns: []string{"id"}},
				NewFK: &db.ForeignKey{Name: "fk", Columns: []string{"user_id"}, RefTable: people, RefColumns: []string{"id"}},
			},
			"~ foreign key fk (user_id) REFERENCES users (id) → (user_id) REFERENCES people (id)",
		},
		{
			db.SchemaChange{Kind: db.ForeignKeyDiff, OldFK: &db.ForeignKey{Name: "fk", Columns: []string{"a"}, RefTable: users, RefColumns: []string{"id"}}},
			"- foreign key fk (a) REFERENCES users (id)",
		},
	} {
		if got := Describe(c.change); got != c.want {
			t.Errorf("got  %s\nwant %s", got, c.want)
		}
	}
}

func TestDescribeObject(t *testing.T) {
	two := table("audit", []db.Column{{Name: "id"}, {Name: "at"}})
	one := table("log", []db.Column{{Name: "id"}})
	for _, c := range []struct {
		o    ObjectDiff
		want string
	}{
		{ObjectDiff{Kind: CreateTable, Name: "audit", Source: &two}, "+ audit  (new table, 2 columns)"},
		{ObjectDiff{Kind: CreateTable, Name: "log", Source: &one}, "+ log  (new table, 1 column)"},
		{ObjectDiff{Kind: DropTable, Name: "tmp"}, "- tmp"},
		{ObjectDiff{Kind: AlterTable, Name: "users"}, "~ users"},
	} {
		if got := DescribeObject(c.o); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}
