package redit

import (
	"errors"
	"reflect"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/edit"
)

func table(names []string, rows ...[]string) *edit.Table {
	cols := make([]db.ColumnInfo, len(names))
	for i, n := range names {
		cols[i] = db.ColumnInfo{Name: n}
	}
	vals := make([][]db.Value, len(rows))
	for i, r := range rows {
		for _, s := range r {
			vals[i] = append(vals[i], db.Value{Text: s})
		}
	}
	return edit.New(cols, vals, nil)
}

func v(s string) db.Value { return db.Value{Text: s} }

func set(t *testing.T, tb *edit.Table, row, col int, s string) {
	t.Helper()
	if err := tb.SetCell(row, col, v(s)); err != nil {
		t.Fatal(err)
	}
}

// insert adds a row at the end with the given cells.
func insert(t *testing.T, tb *edit.Table, cells ...string) {
	t.Helper()
	at := tb.InsertAfter(tb.Len() - 1)
	for i, s := range cells {
		set(t, tb, at, i, s)
	}
}

type checkWant struct {
	args   []string
	expect Expect
	value  string
	int    int64
	score  float64
}

func checks(p Plan) []checkWant {
	var out []checkWant
	for _, c := range p.Checks {
		out = append(out, checkWant{c.Args, c.Expect, c.Value, c.Int, c.Score})
	}
	return out
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n got %#v\nwant %#v", what, got, want)
	}
}

const bk = "b\xff k" // a key with a non-UTF-8 byte and a space

func TestBuildHash(t *testing.T) {
	af := "a\xff f"
	tb := table([]string{"field", "value"}, []string{af, "1"}, []string{"b", "2"}, []string{"c", "3"})
	set(t, tb, 0, 1, "9")
	set(t, tb, 1, 0, "bb")
	tb.Delete(2)
	insert(t, tb, "d", "4")
	p, err := Build(tb, Target{Key: bk, Type: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "checks", checks(p), []checkWant{
		{args: []string{"HGET", bk, af}, expect: ExpectValue, value: "1"},
		{args: []string{"HGET", bk, "b"}, expect: ExpectValue, value: "2"},
		{args: []string{"HEXISTS", bk, "bb"}, expect: ExpectInt, int: 0},
		{args: []string{"HGET", bk, "c"}, expect: ExpectValue, value: "3"},
		{args: []string{"HEXISTS", bk, "d"}, expect: ExpectInt, int: 0},
	})
	eq(t, "cmds", p.Cmds, [][]string{
		{"HDEL", bk, "b"},
		{"HDEL", bk, "c"},
		{"HSET", bk, af, "9"},
		{"HSET", bk, "bb", "2"},
		{"HSET", bk, "d", "4"},
	})
	if p.Key != bk || p.N != 4 {
		t.Fatalf("key %q, N %d", p.Key, p.N)
	}
	if w := p.Checks[4].What; w != `field "d" already exists` {
		t.Fatalf("what = %q", w)
	}
}

func TestBuildHashErrors(t *testing.T) {
	tb := table([]string{"field", "value"}, []string{"a", "1"}, []string{"b", "2"})
	set(t, tb, 1, 0, "a")
	if _, err := Build(tb, Target{Key: "k", Type: "hash"}); err == nil || err.Error() != `field "a" appears twice` {
		t.Fatalf("err = %v", err)
	}
	tb = table([]string{"field", "value"}, []string{"a", "1"})
	at := tb.InsertAfter(0)
	set(t, tb, at, 0, "n")
	if _, err := Build(tb, Target{Key: "k", Type: "hash"}); !errors.Is(err, ErrNull) {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildHashMovedName(t *testing.T) {
	// a is renamed to x and b to a: a's HGET check is there, so no HEXISTS a.
	tb := table([]string{"field", "value"}, []string{"a", "1"}, []string{"b", "2"})
	set(t, tb, 0, 0, "x")
	set(t, tb, 1, 0, "a")
	p, err := Build(tb, Target{Key: "k", Type: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "checks", checks(p), []checkWant{
		{args: []string{"HGET", "k", "a"}, expect: ExpectValue, value: "1"},
		{args: []string{"HEXISTS", "k", "x"}, expect: ExpectInt},
		{args: []string{"HGET", "k", "b"}, expect: ExpectValue, value: "2"},
	})
	eq(t, "cmds", p.Cmds, [][]string{
		{"HDEL", "k", "a"}, {"HDEL", "k", "b"}, {"HSET", "k", "x", "1"}, {"HSET", "k", "a", "2"},
	})
}

func TestBuildSet(t *testing.T) {
	tb := table([]string{"member"}, []string{"x"}, []string{"z"}, []string{"keep"})
	set(t, tb, 0, 0, "y")
	tb.Delete(1)
	insert(t, tb, "w\xff ")
	p, err := Build(tb, Target{Key: bk, Type: "set"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "checks", checks(p), []checkWant{
		{args: []string{"SISMEMBER", bk, "x"}, expect: ExpectInt, int: 1},
		{args: []string{"SISMEMBER", bk, "y"}, expect: ExpectInt, int: 0},
		{args: []string{"SISMEMBER", bk, "z"}, expect: ExpectInt, int: 1},
		{args: []string{"SISMEMBER", bk, "w\xff "}, expect: ExpectInt, int: 0},
	})
	eq(t, "cmds", p.Cmds, [][]string{
		{"SREM", bk, "x"}, {"SREM", bk, "z"}, {"SADD", bk, "y"}, {"SADD", bk, "w\xff "},
	})
	if p.N != 3 {
		t.Fatalf("N = %d", p.N)
	}
	set(t, tb, 3, 0, "keep")
	if _, err := Build(tb, Target{Key: bk, Type: "set"}); err == nil || err.Error() != `member "keep" appears twice` {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildZSet(t *testing.T) {
	cols := []string{"rank", "member", "score"}
	tb := table(cols, []string{"0", "m1", "1"}, []string{"1", "m2", "2"})
	set(t, tb, 0, 2, "2.5")
	insert(t, tb, "", "m3", "7")
	p, err := Build(tb, Target{Key: "k", Type: "zset"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "checks", checks(p), []checkWant{
		{args: []string{"ZSCORE", "k", "m1"}, expect: ExpectScore, score: 1},
		{args: []string{"ZSCORE", "k", "m3"}, expect: ExpectNil},
	})
	eq(t, "cmds", p.Cmds, [][]string{{"ZADD", "k", "XX", "2.5", "m1"}, {"ZADD", "k", "NX", "7", "m3"}})

	tb = table(cols, []string{"0", "m1", "1"}, []string{"1", "m2", "2"})
	set(t, tb, 0, 1, "n1")
	tb.Delete(1)
	p, err = Build(tb, Target{Key: "k", Type: "zset"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "cmds", p.Cmds, [][]string{{"ZREM", "k", "m1"}, {"ZREM", "k", "m2"}, {"ZADD", "k", "NX", "1", "n1"}})
	eq(t, "checks", checks(p), []checkWant{
		{args: []string{"ZSCORE", "k", "m1"}, expect: ExpectScore, score: 1},
		{args: []string{"ZSCORE", "k", "n1"}, expect: ExpectNil},
		{args: []string{"ZSCORE", "k", "m2"}, expect: ExpectScore, score: 2},
	})

	tb = table(cols, []string{"0", "m1", "1"})
	set(t, tb, 0, 2, "abc")
	if _, err := Build(tb, Target{Key: "k", Type: "zset"}); err == nil || err.Error() != `score "abc" is not a number` {
		t.Fatalf("err = %v", err)
	}
	tb = table(cols, []string{"0", "m1", "1"})
	set(t, tb, 0, 0, "5")
	if _, err := Build(tb, Target{Key: "k", Type: "zset"}); err == nil || err.Error() != "the rank column cannot be edited" {
		t.Fatalf("err = %v", err)
	}
}
