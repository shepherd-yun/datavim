package edit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

type fakeQuerier struct {
	execs    []string
	affected map[string]int64
	fail     map[string]error
}

func (f *fakeQuerier) Exec(_ context.Context, q string, _ ...any) (db.ExecResult, error) {
	f.execs = append(f.execs, q)
	if err := f.fail[q]; err != nil {
		return db.ExecResult{}, err
	}
	n, ok := f.affected[q]
	if !ok {
		n = 1
	}
	return db.ExecResult{RowsAffected: n}, nil
}

func (f *fakeQuerier) Query(context.Context, string, ...any) (*db.ResultSet, error) {
	panic("Apply only executes")
}

func (f *fakeQuerier) Stream(context.Context, string, func([]db.ColumnInfo), func([]db.Value) error) error {
	panic("Apply only executes")
}

func TestApplyCommits(t *testing.T) {
	f := &fakeQuerier{}
	if err := Apply(context.Background(), f, sqlx.MySQL, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"START TRANSACTION", "a", "b", "COMMIT"}, f.execs); diff != "" {
		t.Fatalf("execs (-want +got):\n%s", diff)
	}
}

func TestApplyRollsBackWhenARowIsNotFound(t *testing.T) {
	f := &fakeQuerier{affected: map[string]int64{"b": 0}}
	err := Apply(context.Background(), f, sqlx.MySQL, []string{"a", "b", "c"})
	if diff := cmp.Diff([]string{"START TRANSACTION", "a", "b", "ROLLBACK"}, f.execs); diff != "" {
		t.Fatalf("execs (-want +got):\n%s", diff)
	}
	var se *StatementError
	var ce *CountError
	if !errors.As(err, &se) || se.Index != 1 || se.SQL != "b" || !errors.As(err, &ce) || ce.Affected != 0 {
		t.Fatalf("err = %v", err)
	}
	if want := "statement 2: changed 0 rows, expected 1 · rolled back, nothing was changed"; err.Error() != want {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestApplyRollsBackOnError(t *testing.T) {
	boom := errors.New("duplicate key")
	f := &fakeQuerier{fail: map[string]error{"a": boom}}
	err := Apply(context.Background(), f, sqlx.Postgres, []string{"a", "b"})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if diff := cmp.Diff([]string{"BEGIN", "a", "ROLLBACK"}, f.execs); diff != "" {
		t.Fatalf("execs (-want +got):\n%s", diff)
	}
}

func TestApplyBeginAndCommitFailures(t *testing.T) {
	f := &fakeQuerier{fail: map[string]error{"BEGIN": errors.New("down")}}
	if err := Apply(context.Background(), f, sqlx.Postgres, []string{"a"}); err == nil || len(f.execs) != 1 {
		t.Fatalf("a failed BEGIN runs nothing: %v %v", err, f.execs)
	}
	f = &fakeQuerier{fail: map[string]error{"COMMIT": errors.New("serialization failure")}}
	err := Apply(context.Background(), f, sqlx.Postgres, []string{"a"})
	if err == nil || f.execs[len(f.execs)-1] != "ROLLBACK" {
		t.Fatalf("a failed COMMIT is rolled back: %v %v", err, f.execs)
	}
}

func TestApplyDDLOnPostgresIsOneTransaction(t *testing.T) {
	q := &fakeQuerier{affected: map[string]int64{"ALTER a": 0, "ALTER b": 0}}
	if err := ApplyDDL(context.Background(), q, sqlx.Postgres, []string{"ALTER a", "ALTER b"}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"BEGIN", "ALTER a", "ALTER b", "COMMIT"}, q.execs); diff != "" {
		t.Fatal(diff) // DDL reports no rows: ApplyDDL must not count them
	}
}

func TestApplyDDLOnPostgresRollsBack(t *testing.T) {
	boom := errors.New("boom")
	q := &fakeQuerier{fail: map[string]error{"ALTER b": boom}}
	err := ApplyDDL(context.Background(), q, sqlx.Postgres, []string{"ALTER a", "ALTER b", "ALTER c"})
	var de *DDLError
	if !errors.As(err, &de) || de.Index != 1 || !de.RolledBack || !errors.Is(err, boom) {
		t.Fatalf("err = %#v", err)
	}
	if diff := cmp.Diff([]string{"BEGIN", "ALTER a", "ALTER b", "ROLLBACK"}, q.execs); diff != "" {
		t.Fatal(diff)
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("message: %v", err)
	}
}

func TestApplyDDLOnMySQLStopsAtTheFirstFailure(t *testing.T) {
	boom := errors.New("boom")
	q := &fakeQuerier{fail: map[string]error{"ALTER b": boom}}
	err := ApplyDDL(context.Background(), q, sqlx.MySQL, []string{"ALTER a", "ALTER b", "ALTER c"})
	var de *DDLError
	if !errors.As(err, &de) || de.Index != 1 || de.RolledBack {
		t.Fatalf("err = %#v", err)
	}
	// MySQL commits each DDL statement: no transaction to begin or roll back.
	if diff := cmp.Diff([]string{"ALTER a", "ALTER b"}, q.execs); diff != "" {
		t.Fatal(diff)
	}
	if !strings.Contains(err.Error(), "the 1 before it were applied") {
		t.Fatalf("message: %v", err)
	}
	q = &fakeQuerier{fail: map[string]error{"ALTER a": boom}}
	if err := ApplyDDL(context.Background(), q, sqlx.MySQL, []string{"ALTER a"}); !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("message: %v", err)
	}
}

func TestApplyInTxReleasesItsSavepoint(t *testing.T) {
	f := &fakeQuerier{}
	if err := ApplyInTx(context.Background(), f, sqlx.MySQL, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"SAVEPOINT sqlvim_edit", "a", "b", "RELEASE SAVEPOINT sqlvim_edit"}
	if diff := cmp.Diff(want, f.execs); diff != "" {
		t.Fatalf("execs (-want +got):\n%s", diff)
	}
}

func TestApplyInTxRollsBackToItsSavepoint(t *testing.T) {
	f := &fakeQuerier{affected: map[string]int64{"b": 2}}
	err := ApplyInTx(context.Background(), f, sqlx.Postgres, []string{"a", "b", "c"})
	want := []string{"SAVEPOINT sqlvim_edit", "a", "b", "ROLLBACK TO SAVEPOINT sqlvim_edit"}
	if diff := cmp.Diff(want, f.execs); diff != "" {
		t.Fatalf("execs (-want +got):\n%s", diff)
	}
	var se *StatementError
	if !errors.As(err, &se) || se.Index != 1 || !se.InTx {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "the transaction is still open") {
		t.Fatalf("message: %v", err)
	}
}

func TestApplyInTxSavepointSyntax(t *testing.T) {
	for _, tc := range []struct {
		d          sqlx.Dialect
		ok, failed []string
	}{
		{sqlx.SQLServer, []string{"SAVE TRANSACTION sqlvim_edit", "a"}, []string{"SAVE TRANSACTION sqlvim_edit", "b", "ROLLBACK TRANSACTION sqlvim_edit"}},
		{sqlx.Oracle, []string{"SAVEPOINT sqlvim_edit", "a"}, []string{"SAVEPOINT sqlvim_edit", "b", "ROLLBACK TO SAVEPOINT sqlvim_edit"}},
	} {
		f := &fakeQuerier{}
		if err := ApplyInTx(context.Background(), f, tc.d, []string{"a"}); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(tc.ok, f.execs); diff != "" {
			t.Errorf("%s ok (-want +got):\n%s", tc.d.Driver(), diff)
		}
		f = &fakeQuerier{affected: map[string]int64{"b": 0}}
		_ = ApplyInTx(context.Background(), f, tc.d, []string{"b"})
		if diff := cmp.Diff(tc.failed, f.execs); diff != "" {
			t.Errorf("%s failed (-want +got):\n%s", tc.d.Driver(), diff)
		}
	}
}
