package sqlite

import (
	"context"
	"testing"

	"github.com/shepherd-yun/datavim/internal/datadiff"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/export"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

func TestBlobRoundTrip(t *testing.T) {
	path := makeFile(t)
	engines := map[string]db.Conn{
		"local": open(t, db.ConnConfig{Database: path}),
		"cli":   openCLIConn(t, path, false),
	}
	for name, c := range engines {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			exec := func(q string) {
				t.Helper()
				if _, err := c.Exec(ctx, q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			src, dst, cpy := "b_src_"+name, "b_dst_"+name, "b_cpy_"+name
			for _, tb := range []string{src, dst, cpy} {
				exec("CREATE TABLE " + tb + " (id INTEGER PRIMARY KEY, v)")
			}
			exec("INSERT INTO " + src + " VALUES (1, x'00ff'), (2, 'x''00ff'''), (3, x'')")
			exec("INSERT INTO " + dst + " VALUES (1, 'x''00FF'''), (2, x'00ff')")
			check := func(table string) {
				t.Helper()
				rs, err := c.Query(ctx, "SELECT typeof(v), hex(v) FROM "+table+" ORDER BY id")
				if err != nil {
					t.Fatal(err)
				}
				want := [][2]string{{"blob", "00FF"}, {"text", "78273030666627"}, {"blob", ""}}
				if len(rs.Rows) != len(want) {
					t.Fatalf("%s: %d rows", table, len(rs.Rows))
				}
				for i, w := range want {
					if rs.Rows[i][0].Text != w[0] || rs.Rows[i][1].Text != w[1] {
						t.Errorf("%s row %d = %+v, want %v", table, i, rs.Rows[i], w)
					}
				}
			}

			// read, export as INSERT, run it
			rs, err := c.Query(ctx, "SELECT * FROM "+src+" ORDER BY id")
			if err != nil {
				t.Fatal(err)
			}
			script, err := export.Encode(export.SQL, rs, export.Options{Driver: "sqlite", Table: []string{cpy}})
			if err != nil {
				t.Fatal(err)
			}
			exec(script)
			check(cpy)

			// compare and sync the data
			schema := func(tb string) db.TableSchema {
				t.Helper()
				s, err := c.Meta().TableSchema(ctx, db.TableRef{Name: tb})
				if err != nil {
					t.Fatal(err)
				}
				return s
			}
			spec, err := datadiff.NewSpec(schema(src), schema(dst))
			if err != nil {
				t.Fatal(err)
			}
			so := spec.NewSource(100)
			if err := c.Stream(ctx, spec.Query(sqlx.SQLite, db.TableRef{Name: src}, ""), func([]db.ColumnInfo) {}, so.Add); err != nil {
				t.Fatal(err)
			}
			m := so.Match()
			var cols []db.ColumnInfo
			if err := c.Stream(ctx, spec.Query(sqlx.SQLite, db.TableRef{Name: dst}, ""), func(cs []db.ColumnInfo) { cols = cs }, m.Add); err != nil {
				t.Fatal(err)
			}
			stmts, err := spec.Script(sqlx.SQLite, db.TableRef{Name: dst}, cols, m.Rows(), func(int) bool { return true })
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range stmts {
				exec(q)
			}
			check(dst)
		})
	}
}

func TestBlobRowLocation(t *testing.T) {
	path := makeFile(t)
	for name, c := range map[string]db.Conn{"local": open(t, db.ConnConfig{Database: path}), "cli": openCLIConn(t, path, false)} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			tb := "kb_" + name
			if _, err := c.Exec(ctx, "CREATE TABLE "+tb+" (k BLOB PRIMARY KEY, a TEXT)"); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Exec(ctx, "INSERT INTO "+tb+" VALUES (x'00ff', 'blob'), ('x''00ff''', 'text')"); err != nil {
				t.Fatal(err)
			}
			rs, err := c.Query(ctx, "SELECT k, a FROM "+tb+" ORDER BY a")
			if err != nil {
				t.Fatal(err)
			}
			lit := sqlx.Literal(sqlx.SQLite, rs.Columns[0].DatabaseType, rs.Rows[0][0])
			res, err := c.Exec(ctx, "UPDATE "+tb+" SET a = 'found' WHERE k = "+lit)
			if err != nil || res.RowsAffected != 1 {
				t.Fatalf("%s: %+v %v", lit, res, err)
			}
			out, err := c.Query(ctx, "SELECT a FROM "+tb+" WHERE typeof(k) = 'text'")
			if err != nil || out.Rows[0][0].Text != "text" {
				t.Fatalf("the text row changed: %+v %v", out, err)
			}
		})
	}
}
