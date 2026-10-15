package schemadiff

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// fakeWriter writes one short line per statement and, like the real ones,
// cannot write generated-column changes or foreign keys.
type fakeWriter struct{}

func (fakeWriter) DropTable(t db.TableRef, _ bool) string       { return "DROP " + t.Name }
func (fakeWriter) RenameTable(db.TableRef, string, bool) string { return "" }
func (fakeWriter) TruncateTable(db.TableRef) string             { return "" }

func (fakeWriter) CreateTable(s db.TableSchema) ([]string, []db.SchemaChange) {
	return []string{"CREATE " + s.Ref.Database + "." + s.Ref.Name}, nil
}

func (fakeWriter) AlterTable(t db.TableRef, changes []db.SchemaChange) ([]string, []db.SchemaChange) {
	var stmts []string
	var manual []db.SchemaChange
	for _, c := range changes {
		if c.Kind == db.ForeignKeyDiff || c.Kind == db.AlterColumn && c.Attrs&db.AttrGenerated != 0 {
			manual = append(manual, c)
			continue
		}
		stmts = append(stmts, "ALTER "+t.Name+" "+Describe(c))
	}
	return stmts, manual
}

func scriptFixture() []ObjectDiff {
	id := db.Column{Name: "id", Type: "int"}
	target := []db.TableSchema{table("users", []db.Column{id, {Name: "g", Type: "int"}}), table("tmp", []db.Column{id})}
	src := table("users", []db.Column{id, {Name: "g", Type: "int", Generated: "id + 1"}, {Name: "name", Type: "text", Nullable: true}})
	src.ForeignKeys = []db.ForeignKey{{Name: "fk", Columns: []string{"id"}, RefTable: src.Ref, RefColumns: []string{"id"}}}
	audit := table("audit", []db.Column{id})
	audit.Ref.Database = "app2" // created in the target's namespace, not the source's
	objs, _ := DiffNamespace(target, []db.TableSchema{src, audit})
	return objs
}

func TestWritable(t *testing.T) {
	objs := scriptFixture()
	users := objs[2]
	var got []bool
	for _, c := range users.Changes {
		got = append(got, Writable(fakeWriter{}, users.Target.Ref, c))
	}
	// AddColumn name, AlterColumn g (generated), ForeignKeyDiff fk
	if diff := cmp.Diff([]bool{true, false, false}, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestScript(t *testing.T) {
	objs := scriptFixture()
	stmts, manual := Script(fakeWriter{}, app, objs, func(int, int) bool { return true })
	want := []string{"CREATE app.audit", "ALTER users + column name  text NULL", "DROP tmp"}
	if diff := cmp.Diff(want, stmts); diff != "" {
		t.Errorf("stmts (-want +got):\n%s", diff)
	}
	wantManual := []string{"users: ~ column g  not generated → AS (id + 1)"}
	if diff := cmp.Diff(wantManual, manual); diff != "" {
		t.Errorf("manual (-want +got):\n%s", diff)
	}

	stmts, manual = Script(fakeWriter{}, app, objs, func(int, int) bool { return false })
	if len(stmts) != 0 {
		t.Errorf("nothing picked, stmts = %v", stmts)
	}
	if diff := cmp.Diff(wantManual, manual); diff != "" {
		t.Errorf("manual changes are listed even when nothing is picked (-want +got):\n%s", diff)
	}

	// only the users table's AddColumn
	stmts, _ = Script(fakeWriter{}, app, objs, func(o, c int) bool { return o == 2 && c == 0 })
	if diff := cmp.Diff([]string{"ALTER users + column name  text NULL"}, stmts); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}
