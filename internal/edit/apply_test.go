package edit

import (
	"context"
	"errors"
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
