package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

// render lists nodes as "Group: name detail; …" for compact comparison.
func render(groups []*tree.Node) string {
	var b strings.Builder
	for _, g := range groups {
		b.WriteString(g.Name + ":")
		for _, c := range g.Children {
			b.WriteString(" " + c.Name)
			if c.Detail != "" {
				b.WriteString(" " + c.Detail)
			}
			b.WriteString(";")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func TestTableChildrenGroups(t *testing.T) {
	app := db.Namespace{Database: "app"}
	s := db.TableSchema{
		Ref: db.TableRef{Namespace: app, Name: "users"},
		Columns: []db.Column{
			{Name: "org_id", Type: "bigint", Position: 3},
			{Name: "id", Type: "bigint", PrimaryKey: true, Position: 1},
			{Name: "email", Type: "varchar(255)", Position: 2},
		},
		Indexes: []db.Index{
			{Name: "idx_org", Columns: []string{"org_id"}},
			{Name: "uk_email", Columns: []string{"email"}, Unique: true},
			{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true},
			{Name: "idx_tags", Columns: []string{"tags"}, Method: "gin", Where: "tags IS NOT NULL"},
		},
		ForeignKeys: []db.ForeignKey{
			{Name: "fk_org", Columns: []string{"org_id"}, RefTable: db.TableRef{Namespace: app, Name: "orgs"}, RefColumns: []string{"id"}},
			{Name: "fk_audit", Columns: []string{"id"}, RefTable: db.TableRef{Namespace: db.Namespace{Database: "audit"}, Name: "log"}, RefColumns: []string{"user_id"}},
		},
	}
	groups := tableChildren("local", s)
	want := "Columns: id bigint PK; email varchar(255); org_id bigint;\n" +
		"Keys: PRIMARY (id); uk_email (email);\n" +
		"Foreign Keys: fk_org (org_id) → orgs(id); fk_audit (id) → audit.log(user_id);\n" +
		"Indexes: idx_org (org_id); uk_email (email) unique; PRIMARY (id) unique; idx_tags (tags) gin where tags IS NOT NULL;\n"
	if got := render(groups); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if groups[0].Detail != "3" || groups[3].Detail != "4" {
		t.Fatalf("a group shows how many it holds: %q %q", groups[0].Detail, groups[3].Detail)
	}
	if !groups[0].Expanded || groups[1].Expanded {
		t.Fatal("Columns starts expanded, the other groups closed")
	}
	fk := groups[2].Children[0]
	if fk.Kind != tree.KindForeignKey || fk.Ref.Name != "orgs" || fk.Parent() != groups[2] {
		t.Fatalf("fk = %+v", fk)
	}
	for _, g := range groups {
		if g.Kind != tree.KindGroup || !g.Loaded || g.Conn != "local" || g.NS != app {
			t.Fatalf("group %+v", g)
		}
	}
}

func TestTableChildrenHidesEmptyGroups(t *testing.T) {
	groups := tableChildren("local", db.TableSchema{Columns: []db.Column{{Name: "a", Type: "int"}}})
	if len(groups) != 1 || groups[0].Group != tree.GroupColumns {
		t.Fatalf("groups = %s", render(groups))
	}
}

func TestFetchChildrenOfTableAndView(t *testing.T) {
	c := &fakeConn{}
	c.setSchemas(usersSchema())
	app := db.Namespace{Database: "app"}
	nodes, err := fetchChildren(context.Background(), c, tree.KindTable, "local", app, "users")
	// usersSchema has columns and only the PRIMARY index: Keys and Indexes both list it.
	if err != nil || len(nodes) != 3 || nodes[0].Name != "Columns" || nodes[1].Name != "Keys" || nodes[2].Name != "Indexes" {
		t.Fatalf("table: %v %s", err, render(nodes))
	}
	if _, err := fetchChildren(context.Background(), c, tree.KindTable, "local", app, "missing"); err == nil || !strings.Contains(err.Error(), "table missing not found") {
		t.Fatalf("missing table: %v", err)
	}
	nodes, err = fetchChildren(context.Background(), c, tree.KindView, "local", app, "active")
	if err != nil || len(nodes) != 1 || nodes[0].Group != tree.GroupColumns {
		t.Fatalf("view: %v %s", err, render(nodes))
	}
}
