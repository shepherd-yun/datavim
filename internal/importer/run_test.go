package importer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
)

type call struct {
	sql  string
	args []any
}

// fakeQuerier records Exec and Query calls; fail decides which Execs fail.
// Query answers max, the table's largest id.
type fakeQuerier struct {
	calls []call
	fail  func(sql string, args []any) error
	max   string
}

func (f *fakeQuerier) Exec(ctx context.Context, q string, args ...any) (db.ExecResult, error) {
	f.calls = append(f.calls, call{q, args})
	if err := ctx.Err(); err != nil {
		return db.ExecResult{}, err
	}
	if f.fail != nil {
		if err := f.fail(q, args); err != nil {
			return db.ExecResult{}, err
		}
	}
	return db.ExecResult{RowsAffected: 1}, nil
}

func (f *fakeQuerier) Query(_ context.Context, q string, args ...any) (*db.ResultSet, error) {
	f.calls = append(f.calls, call{q, args})
	return &db.ResultSet{Rows: [][]db.Value{{{Text: f.max}}}}, nil
}

func (f *fakeQuerier) Stream(context.Context, string, func([]db.ColumnInfo), func([]db.Value) error) error {
	return nil
}

// statements lists the SQL run, inserts shortened to "INSERT n rows".
func (f *fakeQuerier) statements() []string {
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c.sql, "INSERT") {
			out = append(out, "INSERT "+strconv.Itoa(strings.Count(c.sql, "(")-1)+" rows")
			continue
		}
		out = append(out, c.sql)
	}
	return out
}

// idsPlan maps a one-column file "id" to an int column of app.t.
func idsPlan(driver string) Plan {
	return Plan{
		Driver: driver, Width: 1,
		Table:   db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "t"},
		Columns: []Column{{Source: 0, Header: "id", Target: TargetOf(driver, db.Column{Name: "id", Type: "int"})}},
	}
}

func idsFile(t *testing.T, n int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("id\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%d\n", i)
	}
	return writeFile(t, "ids.csv", b.String())
}

func TestRead(t *testing.T) {
	p := writeFile(t, "a.csv", "id,name\n1,a\n2,b\n3,c\n")
	s, err := Read(context.Background(), p, DefaultOptions(p), 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Rows) != 2 || !slices.Equal(s.Lines, []int{2, 3}) || s.Header[1] != "name" || s.BadUTF8 {
		t.Fatalf("sample %+v", s)
	}
	var scanned [][]string
	s, err = Read(context.Background(), p, DefaultOptions(p), 2, func(r []string) { scanned = append(scanned, r) })
	if err != nil || len(s.Rows) != 2 || len(scanned) != 3 {
		t.Fatalf("scan: %d rows, sample %d, err %v", len(scanned), len(s.Rows), err)
	}
	gbk := writeFile(t, "g.csv", "\xc3\xfb\xd7\xd6\n\xd5\xc5\xc8\xfd\n") // 名字 / 张三 in GBK
	if s, err := Read(context.Background(), gbk, DefaultOptions(gbk), 20, nil); err != nil || !s.BadUTF8 {
		t.Fatalf("GBK read as UTF-8: BadUTF8 = %v, err %v", s.BadUTF8, err)
	}
}

func TestCheck(t *testing.T) {
	var b strings.Builder
	b.WriteString("id\n1\n\n2\n")
	for range 60 {
		b.WriteString("x\n")
	}
	p := writeFile(t, "a.csv", b.String())
	var done atomic.Int64
	r, err := Check(context.Background(), p, DefaultOptions(p), idsPlan("mysql"), &done)
	if err != nil {
		t.Fatal(err)
	}
	// encoding/csv skips the empty line 3, so the first x is on line 5.
	if r.Rows != 2 || r.Failed != 60 || len(r.Errors) != MaxErrors || r.Errors[0].Line != 5 || done.Load() != 62 {
		t.Fatalf("result %+v, first %v, done %d", r, r.Errors[0], done.Load())
	}
}

func TestRunCommitsInBatches(t *testing.T) {
	q := &fakeQuerier{}
	var done atomic.Int64
	n, err := Run(context.Background(), q, Job{Path: idsFile(t, 1001), Options: DefaultOptions("ids.csv"), Plan: idsPlan("postgres")}, &done)
	if err != nil || n != 1001 || done.Load() != 1001 {
		t.Fatalf("n = %d, done = %d, err = %v", n, done.Load(), err)
	}
	batch := []string{"SAVEPOINT datavim_import", "", "RELEASE SAVEPOINT datavim_import"}
	want := []string{"BEGIN"}
	for _, rows := range []int{500, 500, 1} {
		batch[1] = "INSERT " + strconv.Itoa(rows) + " rows"
		want = append(want, batch...)
	}
	want = append(want, "COMMIT")
	if got := q.statements(); !slices.Equal(got, want) {
		t.Fatalf("statements:\n%v\nwant\n%v", got, want)
	}
	if q.calls[2].args[0] != "1" || !strings.Contains(q.calls[2].sql, "$500") {
		t.Fatalf("first insert %q %v", q.calls[2].sql, q.calls[2].args[:1])
	}
}

// failOn fails multi-row inserts and the single-row insert of id.
func failOn(id string) func(string, []any) error {
	return func(q string, args []any) error {
		if !strings.HasPrefix(q, "INSERT") {
			return nil
		}
		if len(args) > 1 || slices.Contains(args, any(id)) {
			return errors.New("duplicate key")
		}
		return nil
	}
}

func TestRunFindsTheRejectedRow(t *testing.T) {
	q := &fakeQuerier{fail: failOn("2")}
	var done atomic.Int64
	_, err := Run(context.Background(), q, Job{Path: idsFile(t, 3), Options: DefaultOptions("ids.csv"), Plan: idsPlan("mysql")}, &done)
	var le *LineError
	if !errors.As(err, &le) || le.Line != 3 || err.Error() != "line 3: duplicate key" {
		t.Fatalf("err = %v", err)
	}
	want := []string{"START TRANSACTION", "SAVEPOINT datavim_import", "INSERT 3 rows", "ROLLBACK TO SAVEPOINT datavim_import", "INSERT 1 rows", "INSERT 1 rows", "ROLLBACK"}
	if got := q.statements(); !slices.Equal(got, want) {
		t.Fatalf("statements:\n%v\nwant\n%v", got, want)
	}
	if done.Load() != 0 {
		t.Fatalf("done = %d", done.Load())
	}
}

func TestRunReportsTheBatchWhenNoRowFails(t *testing.T) {
	q := &fakeQuerier{fail: failOn("none")}
	_, err := Run(context.Background(), q, Job{Path: idsFile(t, 3), Options: DefaultOptions("ids.csv"), Plan: idsPlan("mysql")}, new(atomic.Int64))
	var le *LineError
	if !errors.As(err, &le) || le.Line != 0 || err.Error() != "lines 2–4: duplicate key" {
		t.Fatalf("err = %v", err)
	}
}

func TestRunStopsOnABadCell(t *testing.T) {
	q := &fakeQuerier{}
	p := writeFile(t, "a.csv", "id\n1\nx\n")
	_, err := Run(context.Background(), q, Job{Path: p, Options: DefaultOptions(p), Plan: idsPlan("mysql")}, new(atomic.Int64))
	var re *RowError
	if !errors.As(err, &re) || re.Line != 3 {
		t.Fatalf("err = %v", err)
	}
	if got := q.statements(); got[len(got)-1] != "ROLLBACK" {
		t.Fatalf("statements %v", got)
	}
}

func TestRunCreatesAndDropsOnMySQL(t *testing.T) {
	job := func(t *testing.T, driver string) Job {
		return Job{Path: idsFile(t, 1), Options: DefaultOptions("ids.csv"), Plan: idsPlan(driver), Create: []string{"CREATE TABLE t"}, Drop: "DROP TABLE t"}
	}
	q := &fakeQuerier{fail: failOn("1")}
	_, err := Run(context.Background(), q, job(t, "mysql"), new(atomic.Int64))
	want := []string{"CREATE TABLE t", "START TRANSACTION", "SAVEPOINT datavim_import", "INSERT 1 rows", "ROLLBACK TO SAVEPOINT datavim_import", "INSERT 1 rows", "ROLLBACK", "DROP TABLE t"}
	if got := q.statements(); err == nil || !slices.Equal(got, want) {
		t.Fatalf("mysql: err %v, statements\n%v\nwant\n%v", err, got, want)
	}

	q = &fakeQuerier{fail: func(s string, args []any) error {
		if s == "DROP TABLE t" {
			return errors.New("locked")
		}
		return failOn("1")(s, args)
	}}
	_, err = Run(context.Background(), q, job(t, "mysql"), new(atomic.Int64))
	var left *LeftoverError
	if !errors.As(err, &left) || left.Table != "app.t" || !strings.Contains(err.Error(), "was created but is empty") {
		t.Fatalf("drop fails: %v", err)
	}

	q = &fakeQuerier{fail: failOn("1")}
	_, err = Run(context.Background(), q, job(t, "postgres"), new(atomic.Int64))
	want = []string{"BEGIN", "CREATE TABLE t", "SAVEPOINT datavim_import", "INSERT 1 rows", "ROLLBACK TO SAVEPOINT datavim_import", "INSERT 1 rows", "ROLLBACK"}
	if got := q.statements(); err == nil || !slices.Equal(got, want) {
		t.Fatalf("postgres: err %v, statements\n%v\nwant\n%v", err, got, want)
	}
}

func TestRunCancelAndConnLost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	q := &fakeQuerier{fail: func(s string, _ []any) error {
		if strings.HasPrefix(s, "INSERT") {
			cancel()
			return context.Canceled
		}
		return nil
	}}
	_, err := Run(ctx, q, Job{Path: idsFile(t, 2), Options: DefaultOptions("ids.csv"), Plan: idsPlan("mysql")}, new(atomic.Int64))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if got := q.statements(); got[len(got)-1] != "ROLLBACK" || slices.Contains(got, "ROLLBACK TO SAVEPOINT datavim_import") {
		t.Fatalf("cancel statements %v", got)
	}

	q = &fakeQuerier{fail: func(s string, _ []any) error {
		if strings.HasPrefix(s, "INSERT") {
			return fmt.Errorf("%w: reset by peer", db.ErrConnLost)
		}
		return nil
	}}
	_, err = Run(context.Background(), q, Job{Path: idsFile(t, 2), Options: DefaultOptions("ids.csv"), Plan: idsPlan("mysql")}, new(atomic.Int64))
	if !db.IsConnLost(err) || slices.Contains(q.statements(), "ROLLBACK TO SAVEPOINT datavim_import") {
		t.Fatalf("conn lost: %v, %v", err, q.statements())
	}
}

// namesPlan maps a one-column file "name" to app.t and generates its id.
func namesPlan(driver, idType string) Plan {
	return Plan{
		Driver: driver, Width: 1,
		Table: db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "t"},
		Columns: []Column{
			{Source: 0, Header: "name", Target: TargetOf(driver, db.Column{Name: "name", Type: "text"})},
			{Source: -1, Header: "(generate)", Generate: true, Target: TargetOf(driver, db.Column{Name: "id", Type: idType, PrimaryKey: true})},
		},
	}
}

func TestRunGeneratesIDs(t *testing.T) {
	p := writeFile(t, "a.csv", "name\na\n\nb\n")
	q := &fakeQuerier{max: "41"}
	n, err := Run(context.Background(), q, Job{Path: p, Options: DefaultOptions(p), Plan: namesPlan("mysql", "int"), Rows: 2}, new(atomic.Int64))
	if err != nil || n != 2 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	want := []string{"START TRANSACTION", "SELECT COALESCE(MAX(`id`), 0) FROM `app`.`t` FOR UPDATE", "SAVEPOINT datavim_import", "INSERT 2 rows", "RELEASE SAVEPOINT datavim_import", "COMMIT"}
	if got := q.statements(); !slices.Equal(got, want) {
		t.Fatalf("statements:\n%v\nwant\n%v", got, want)
	}
	ins := q.calls[3]
	if !strings.Contains(ins.sql, "(`name`, `id`)") || !slices.Equal(ins.args, []any{"a", "42", "b", "43"}) {
		t.Fatalf("insert %s %v", ins.sql, ins.args)
	}

	q = &fakeQuerier{max: "0"} // an empty table: MAX is NULL, COALESCE gives 0
	if _, err := Run(context.Background(), q, Job{Path: p, Options: DefaultOptions(p), Plan: namesPlan("postgres", "integer"), Rows: 2}, new(atomic.Int64)); err != nil {
		t.Fatal(err)
	}
	want = []string{"BEGIN", `LOCK TABLE "public"."t" IN SHARE ROW EXCLUSIVE MODE`, `SELECT COALESCE(MAX("id"), 0) FROM "public"."t"`, "SAVEPOINT datavim_import"}
	if got := q.statements(); !slices.Equal(got[:4], want) || !slices.Equal(q.calls[4].args, []any{"a", "1", "b", "2"}) {
		t.Fatalf("postgres: %v %v", got, q.calls[4].args)
	}
}

func TestRunRefusesIDsOutOfRange(t *testing.T) {
	p := writeFile(t, "a.csv", "name\na\nb\n")
	q := &fakeQuerier{max: "126"}
	_, err := Run(context.Background(), q, Job{Path: p, Options: DefaultOptions(p), Plan: namesPlan("mysql", "tinyint"), Rows: 2}, new(atomic.Int64))
	if err == nil || !strings.Contains(err.Error(), "out of range for tinyint") {
		t.Fatalf("err = %v", err)
	}
	if got := q.statements(); slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "INSERT") }) || got[len(got)-1] != "ROLLBACK" {
		t.Fatalf("statements %v", got)
	}
}
