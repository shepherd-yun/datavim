package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/rcmd"
	"github.com/shepherd-yun/datavim/internal/rdb"
)

func rs(s string) rdb.Reply { return rdb.Reply{Kind: rdb.KindString, Str: s} }

func rarr(elems ...rdb.Reply) rdb.Reply { return rdb.Reply{Kind: rdb.KindArray, Elems: elems} }

func TestViewReply(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cmd       string
		reply     rdb.Reply
		max       int
		cols      []string
		rows      [][]string
		lines     []string
		summary   string
		truncated bool
	}{
		{name: "json", cmd: "GET a", reply: rs(`{"a":1}`), lines: []string{"{", `  "a": 1`, "}"}, summary: `{"a":1}`},
		{name: "ok", cmd: "SET a 1", reply: rs("OK"), lines: []string{"OK"}, summary: "OK"},
		{name: "multiline", cmd: "GET a", reply: rs("x\ny"), lines: []string{"x", "y"}, summary: `x\ny`},
		{name: "int", cmd: "INCR a", reply: rdb.Reply{Kind: rdb.KindInt, Int: 3}, lines: []string{"(integer) 3"}, summary: "(integer) 3"},
		{name: "double", cmd: "ZSCORE z m", reply: rdb.Reply{Kind: rdb.KindDouble, Float: 3.5}, lines: []string{"(double) 3.5"}, summary: "(double) 3.5"},
		{name: "bool", cmd: "X", reply: rdb.Reply{Kind: rdb.KindBool, Bool: true}, lines: []string{"(boolean) true"}},
		{name: "big", cmd: "X", reply: rdb.Reply{Kind: rdb.KindBigInt, Str: "123"}, lines: []string{"(big number) 123"}},
		{name: "nil", cmd: "GET x", reply: rdb.Reply{Kind: rdb.KindNil}, lines: []string{"(nil)"}, summary: "(nil)"},
		{name: "error", cmd: "LPUSH h x", reply: rdb.Reply{Kind: rdb.KindError, Str: "WRONGTYPE Operation"}, lines: []string{"(error) WRONGTYPE Operation"}, summary: "(error) WRONGTYPE Operation"},
		{name: "empty", cmd: "LRANGE l 0 -1", reply: rarr(), lines: []string{"(empty array)"}, summary: "(empty array)"},
		{
			name: "list", cmd: "LRANGE l 0 -1", reply: rarr(rs("a"), rs("b"), rs("c")),
			cols: []string{"index", "value"}, rows: [][]string{{"1", "a"}, {"2", "b"}, {"3", "c"}}, summary: "3 items",
		},
		{
			name: "hgetall resp2", cmd: "HGETALL h", reply: rarr(rs("f1"), rs("v1"), rs("f2"), rs("v2")),
			cols: []string{"field", "value"}, rows: [][]string{{"f1", "v1"}, {"f2", "v2"}}, summary: "2 fields",
		},
		{
			name: "hgetall map", cmd: "hgetall h",
			reply: rdb.Reply{Kind: rdb.KindMap, Pairs: [][2]rdb.Reply{{rs("f1"), rs("v1")}, {rs("f2"), rs("v2")}}},
			cols:  []string{"field", "value"}, rows: [][]string{{"f1", "v1"}, {"f2", "v2"}}, summary: "2 fields",
		},
		{
			name: "zrange withscores", cmd: "ZRANGE z 0 -1 WITHSCORES", reply: rarr(rs("m"), rs("1.5"), rs("n"), rs("2")),
			cols: []string{"member", "score"}, rows: [][]string{{"m", "1.5"}, {"n", "2"}}, summary: "2 members",
		},
		{
			name: "zrange resp3", cmd: "ZRANGE z 0 -1 WITHSCORES",
			reply: rarr(rarr(rs("m"), rdb.Reply{Kind: rdb.KindDouble, Float: 1.5})),
			cols:  []string{"member", "score"}, rows: [][]string{{"m", "1.5"}}, summary: "1 members",
		},
		{
			name: "scan", cmd: "SCAN 0", reply: rarr(rs("17"), rarr(rs("\xff"), rs("b"))),
			cols: []string{"index", "key"}, rows: [][]string{{"1", "\xff"}, {"2", "b"}}, summary: "cursor 17 · 2 keys",
		},
		{
			name: "hscan", cmd: "HSCAN h 0", reply: rarr(rs("0"), rarr(rs("f"), rs("v"))),
			cols: []string{"field", "value"}, rows: [][]string{{"f", "v"}}, summary: "cursor 0 · 1 fields",
		},
		{
			name: "xrange", cmd: "XRANGE s - +", reply: rarr(rarr(rs("1-0"), rarr(rs("f"), rs("v")))),
			lines:   []string{`1) 1) "1-0"`, `   2) 1) "f"`, `      2) "v"`},
			summary: "1 items",
		},
		{
			name: "truncated", cmd: "LRANGE l 0 -1", reply: rarr(rs("a"), rs("b"), rs("c"), rs("d"), rs("e")), max: 3,
			cols: []string{"index", "value"}, rows: [][]string{{"1", "a"}, {"2", "b"}, {"3", "c"}}, summary: "5 items", truncated: true,
		},
		{
			name: "exec", cmd: "EXEC", reply: rarr(rs("OK"), rdb.Reply{Kind: rdb.KindInt, Int: 2}, rarr(rs("x"))),
			lines: []string{"1) OK", "2) (integer) 2", `3) 1) "x"`}, summary: "3 items",
		},
		{
			name: "nested map", cmd: "XINFO STREAM s",
			reply: rdb.Reply{Kind: rdb.KindMap, Pairs: [][2]rdb.Reply{{rs("length"), {Kind: rdb.KindInt, Int: 1}}, {rs("x"), rarr(rs("a"), rs("b"))}}},
			lines: []string{`1# "length" => (integer) 1`, `2# "x" => 1) "a"`, `   2) "b"`},
		},
		{
			name: "ten items", cmd: "EXEC", reply: rarr(rs("a"), rs("a"), rs("a"), rs("a"), rs("a"), rs("a"), rs("a"), rs("a"), rs("a"), rarr(rs("b"))),
			lines: []string{` 1) "a"`, ` 2) "a"`, ` 3) "a"`, ` 4) "a"`, ` 5) "a"`, ` 6) "a"`, ` 7) "a"`, ` 8) "a"`, ` 9) "a"`, `10) 1) "b"`},
		},
	} {
		args, _ := rcmd.Split(tc.cmd)
		limit := tc.max
		if limit == 0 {
			limit = 100
		}
		v := viewReply(args, tc.reply, limit)
		if !slices.Equal(v.Columns, tc.cols) || !equalRows(v.Rows, tc.rows) || !slices.Equal(v.Lines, tc.lines) ||
			v.Truncated != tc.truncated || (tc.summary != "" && v.Summary != tc.summary) {
			t.Errorf("%s: viewReply = %+v", tc.name, v)
		}
	}

	v := viewReply([]string{"SCAN", "0"}, rarr(rs("17"), rarr(rs("\xff"), rs("b"))), 100)
	if got := v.resultSet().Rows[0][1].Text; got != `\xFF` {
		t.Errorf("resultSet cell = %q", got)
	}
	if v := viewReply([]string{"GET", "a"}, rs(strings.Repeat("x", 100)), 100); len([]rune(v.Summary)) > 61 {
		t.Errorf("long summary = %q", v.Summary)
	}
	if v := viewReply([]string{"GET", "a"}, rs("\x00\x01"), 100); len(v.Lines) != 1 || !strings.Contains(v.Lines[0], "00 01") {
		t.Errorf("binary lines = %q", v.Lines)
	}
}

func equalRows(a, b [][]string) bool {
	return slices.EqualFunc(a, b, slices.Equal[[]string])
}
