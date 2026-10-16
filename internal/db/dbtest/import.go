package dbtest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/importer"
)

// runImportContract checks that importer.Run writes through the driver:
// string parameters for every column type, NULL, DEFAULT, rollback, the
// rejected row, and new tables.
func runImportContract(t *testing.T, conn db.Conn, f Fixture) {
	ctx := context.Background()
	d := conn.Dialect()
	ref := func(name string) db.TableRef { return db.TableRef{Namespace: f.NS, Name: name} }
	exec := func(t *testing.T, q string) {
		t.Helper()
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	file := func(t *testing.T, data string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "in.csv")
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	run := func(t *testing.T, job importer.Job) (int, error) {
		t.Helper()
		s, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		return importer.Run(ctx, s, job, new(atomic.Int64))
	}
	planFor := func(t *testing.T, table db.TableRef) importer.Plan {
		t.Helper()
		cols, err := conn.Meta().Columns(ctx, table)
		if err != nil {
			t.Fatal(err)
		}
		p := importer.Plan{Driver: f.Driver, Table: table, Width: len(cols)}
		for i, c := range cols {
			p.Columns = append(p.Columns, importer.Column{Source: i, Header: c.Name, Target: importer.TargetOf(f.Driver, c)})
		}
		return p
	}
	count := func(t *testing.T, q string) string {
		t.Helper()
		rs, err := conn.Query(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return rs.Rows[0][0].Text
	}

	t.Run("importer writes text parameters into typed columns in one transaction", func(t *testing.T) {
		table := ref("imp_into")
		name := d.QualifiedName(table)
		exec(t, "DROP TABLE IF EXISTS "+name)
		exec(t, "CREATE TABLE "+name+" (id int PRIMARY KEY, name varchar(10) NOT NULL, born date, flag boolean, note text, n int NOT NULL DEFAULT 7)")
		p := planFor(t, table)
		src := file(t, "id,name,born,flag,note,n\n"+
			"1,张三,2026/1/2,yes,\"a, \"\"q\"\"\nline\",\n"+
			"2,Bob,2026-01-03 00:00:00,0,,5\n")
		job := importer.Job{Path: src, Options: importer.DefaultOptions(src), Plan: p}
		if n, err := run(t, job); err != nil || n != 2 {
			t.Fatalf("n = %d, err = %v", n, err)
		}
		rs, err := conn.Query(ctx, "SELECT id, name, note, n, CASE WHEN flag THEN 'y' ELSE 'n' END FROM "+name+" ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		want := [][]db.Value{
			{{Text: "1"}, {Text: "张三"}, {Text: "a, \"q\"\nline"}, {Text: "7"}, {Text: "y"}},
			{{Text: "2"}, {Text: "Bob"}, {Null: true}, {Text: "5"}, {Text: "n"}},
		}
		if diff := cmp.Diff(want, rs.Rows); diff != "" {
			t.Fatalf("rows (-want +got):\n%s", diff)
		}
		if got := count(t, "SELECT COUNT(*) FROM "+name+" WHERE (id = 1 AND born = DATE '2026-01-02') OR (id = 2 AND born = DATE '2026-01-03')"); got != "2" {
			t.Fatalf("dates: %s rows match", got)
		}

		// id 1 again on line 3: the server rejects it, the replay names it, nothing stays.
		dup := file(t, "id,name,born,flag,note,n\n3,c,,,,\n1,d,,,,\n")
		_, err = run(t, importer.Job{Path: dup, Options: importer.DefaultOptions(dup), Plan: p})
		var le *importer.LineError
		if !errors.As(err, &le) || le.Line != 3 {
			t.Fatalf("duplicate: %v", err)
		}
		if got := count(t, "SELECT COUNT(*) FROM "+name); got != "2" {
			t.Fatalf("%s rows after the rollback, want 2", got)
		}
	})

	t.Run("importer creates a table from the file, and leaves none when it fails", func(t *testing.T) {
		for _, tc := range []struct {
			table, data string
			ok          bool
		}{
			{"imp_new", "id,name,amount\n1,Alice,1.50\n2,Bob,22.25\n", true},
			{"imp_bad", "id,name\n1,a\nx,b\n", false},
		} {
			table := ref(tc.table)
			exec(t, "DROP TABLE IF EXISTS "+d.QualifiedName(table))
			src := file(t, tc.data)
			o := importer.DefaultOptions(src)
			in := importer.NewInferrer(f.Driver, "")
			sample, err := importer.Read(ctx, src, o, importer.PreviewRows, in.Add)
			if err != nil {
				t.Fatal(err)
			}
			schema := in.Schema(table, importer.UniqueIdents(sample.Header))
			p := importer.Plan{Driver: f.Driver, Table: table, Width: len(sample.Header)}
			for i, c := range schema.Columns {
				p.Columns = append(p.Columns, importer.Column{Source: i, Header: c.Name, Target: importer.TargetOf(f.Driver, c)})
			}
			create, _ := d.DDL().CreateTable(schema)
			job := importer.Job{Path: src, Options: o, Plan: p, Create: create, Drop: d.DDL().DropTable(table, false)}
			if tc.ok {
				if n, err := run(t, job); err != nil || n != 2 {
					t.Fatalf("%s: n = %d, err = %v", tc.table, n, err)
				}
				if got := count(t, "SELECT COUNT(*) FROM "+d.QualifiedName(table)+" WHERE amount = 22.25"); got != "1" {
					t.Fatalf("amount: %s", got)
				}
				continue
			}
			// imp_bad infers id as varchar (x is not a number); force bigint so row 3 fails.
			job.Plan.Columns[0].Target = importer.TargetOf(f.Driver, db.Column{Name: "id", Type: "bigint"})
			schema.Columns[0].Type = "bigint"
			job.Create, _ = d.DDL().CreateTable(schema)
			if _, err := run(t, job); err == nil {
				t.Fatalf("%s imported", tc.table)
			}
			tables, err := conn.Meta().Tables(ctx, f.NS)
			if err != nil {
				t.Fatal(err)
			}
			if slices.ContainsFunc(tables, func(x db.Table) bool { return x.Name == tc.table }) {
				t.Fatalf("%s is left behind on %s", tc.table, f.Driver)
			}
		}
	})
}
