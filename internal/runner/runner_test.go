package runner

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

type fakeQ struct {
	rows     int
	affected int64
	fail     map[string]error
	ran      []string
}

func (f *fakeQ) Query(ctx context.Context, q string, _ ...any) (*db.ResultSet, error) {
	return db.Collect(func(oc func([]db.ColumnInfo), or func([]db.Value) error) error { return f.Stream(ctx, q, oc, or) })
}

func (f *fakeQ) Exec(_ context.Context, q string, _ ...any) (db.ExecResult, error) {
	f.ran = append(f.ran, q)
	return db.ExecResult{RowsAffected: f.affected}, f.fail[q]
}

func (f *fakeQ) Stream(_ context.Context, q string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	f.ran = append(f.ran, q)
	if err := f.fail[q]; err != nil {
		return err
	}
	onColumns([]db.ColumnInfo{{Name: "n"}})
	for i := range f.rows {
		if err := onRow([]db.Value{{Text: strconv.Itoa(i)}}); err != nil {
			if errors.Is(err, db.ErrStop) {
				return nil
			}
			return err
		}
	}
	return nil
}

func stmts(src string) []sqlx.Statement { return sqlx.Split(src, sqlx.MySQL) }

func TestGuard(t *testing.T) {
	cases := []struct {
		src      string
		readOnly bool
		verdict  Verdict
		expect   string
	}{
		{"select 1", true, Allow, ""},
		{"delete from t where id = 1", false, Allow, ""},
		{"delete from t where id = 1", true, Reject, ""},
		{"create table x (id int)", true, Reject, ""},
		{"set @a = 1", true, Allow, ""},
		{"update t set a = 1", false, Confirm, "t"},
		{"delete", false, Confirm, "yes"},
		{"select 1; update t set a = 1; delete from u", false, Confirm, "t"},
		{"update t set a = 1; insert into u values (1)", true, Reject, ""},
	}
	for _, c := range cases {
		got := Guard(stmts(c.src), sqlx.MySQL, Policy{ReadOnly: c.readOnly})
		if got.Verdict != c.verdict || got.Expect != c.expect {
			t.Errorf("Guard(%q, ro=%v) = %+v; want verdict %d expect %q", c.src, c.readOnly, got, c.verdict, c.expect)
		}
	}
	ro := Guard(stmts("delete from t where id = 1"), sqlx.MySQL, Policy{ReadOnly: true})
	if !strings.Contains(ro.Reason, "read-only") || ro.SQL != "delete from t where id = 1" {
		t.Fatalf("reject = %+v", ro)
	}
	cf := Guard(stmts("update t set a = 1"), sqlx.MySQL, Policy{})
	if !strings.Contains(cf.Reason, "WHERE") || cf.SQL != "update t set a = 1" {
		t.Fatalf("confirm = %+v", cf)
	}
}

func TestRunInOrderAndStopOnError(t *testing.T) {
	q := &fakeQ{rows: 2, affected: 3, fail: map[string]error{"bad": errors.New("syntax error")}}
	res := Run(context.Background(), q, stmts("select 1; update t set a = 1 where id = 2; bad; select 2"), sqlx.MySQL, 10)
	if len(res) != 3 {
		t.Fatalf("results = %d (stops after the error)", len(res))
	}
	if res[0].Rows == nil || len(res[0].Rows.Rows) != 2 || res[0].Truncated {
		t.Fatalf("select: %+v", res[0])
	}
	if res[1].Rows != nil || res[1].Affected != 3 {
		t.Fatalf("update: %+v", res[1])
	}
	if res[2].Err == nil || res[2].SQL != "bad" {
		t.Fatalf("bad: %+v", res[2])
	}
	if strings.Join(q.ran, "|") != "select 1|update t set a = 1 where id = 2|bad" {
		t.Fatalf("ran = %v", q.ran)
	}
}

func TestRunTruncates(t *testing.T) {
	q := &fakeQ{rows: 5}
	r := RunOne(context.Background(), q, "select * from big", sqlx.MySQL, 3)
	if r.Err != nil || len(r.Rows.Rows) != 3 || !r.Truncated {
		t.Fatalf("result = %+v", r)
	}
	exact := RunOne(context.Background(), &fakeQ{rows: 3}, "select * from big", sqlx.MySQL, 3)
	if exact.Truncated {
		t.Fatal("exactly maxRows rows is not truncated")
	}
}

func TestRunStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q := &fakeQ{}
	if res := Run(ctx, q, stmts("select 1; select 2"), sqlx.MySQL, 10); len(res) != 0 || len(q.ran) != 0 {
		t.Fatalf("res=%v ran=%v", res, q.ran)
	}
}

func TestGuardBlocksSwitchingBackToReadWrite(t *testing.T) {
	stmts := sqlx.Split("set default_transaction_read_only = off; prepare s as delete from t; execute s;", sqlx.Postgres)
	c := Guard(stmts, sqlx.Postgres, Policy{ReadOnly: true})
	if c.Verdict != Reject || c.SQL != "set default_transaction_read_only = off" {
		t.Fatalf("check = %+v", c)
	}
	if c := Guard(stmts, sqlx.Postgres, Policy{}); c.Verdict != Allow {
		t.Fatalf("read-write connection: check = %+v", c)
	}
}

func TestGuardProd(t *testing.T) {
	prod := Policy{Prod: true}
	if c := Guard(stmts("select * from t; begin; commit"), sqlx.MySQL, prod); c.Verdict != Allow {
		t.Fatalf("reads and transaction control run without asking: %+v", c)
	}
	c := Guard(stmts("insert into t values (1); create table x (id int)"), sqlx.MySQL, prod)
	if c.Verdict != Confirm || c.Expect != "" || !strings.Contains(c.Title, "PROD") {
		t.Fatalf("check = %+v", c)
	}
	body := strings.Join(c.Body, "\n")
	if !strings.Contains(body, "write t: insert into t values (1)") || !strings.Contains(body, "ddl: create table x (id int)") {
		t.Fatalf("body:\n%s", body)
	}
	c = Guard(stmts("update t set a = 1"), sqlx.MySQL, prod)
	if c.Verdict != Confirm || c.Expect != "t" || !strings.Contains(c.Title, "PROD") {
		t.Fatalf("missing WHERE on prod still needs the table name: %+v", c)
	}
	c = Guard(stmts("delete from t where id = 1"), sqlx.MySQL, Policy{Prod: true, ReadOnly: true})
	if c.Verdict != Reject || !strings.Contains(c.Reason, ":set noreadonly") {
		t.Fatalf("read-only wins: %+v", c)
	}
}

func TestGuardMissingWhereDialog(t *testing.T) {
	c := Guard(stmts("update t set a = 1"), sqlx.MySQL, Policy{})
	if c.Title != "Statement has no WHERE clause" || len(c.Body) != 3 || c.Body[2] != "update t set a = 1" {
		t.Fatalf("check = %+v", c)
	}
}

func TestRunRecordsWhereEachStatementStarts(t *testing.T) {
	src := "select 1;\n  select 2"
	res := Run(context.Background(), &fakeQ{}, stmts(src), sqlx.MySQL, 10)
	if len(res) != 2 || res[0].Start != 0 || res[1].Start != strings.Index(src, "select 2") {
		t.Fatalf("starts: %+v", res)
	}
}
