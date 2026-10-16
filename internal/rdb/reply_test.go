package rdb

import (
	"io"
	"math/big"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// replyErr is a server error reply, as go-redis's proto.RedisError is.
type replyErr string

func (e replyErr) Error() string { return string(e) }
func (replyErr) RedisError()     {}

var _ redis.Error = replyErr("")

func TestFromAny(t *testing.T) {
	big1, _ := new(big.Int).SetString("12345678901234567890", 10)
	for _, tc := range []struct {
		name string
		v    any
		err  error
		want Reply
	}{
		{"status", "OK", nil, Reply{Kind: KindString, Str: "OK"}},
		{"int", int64(3), nil, Reply{Kind: KindInt, Int: 3}},
		{"double", 3.5, nil, Reply{Kind: KindDouble, Float: 3.5}},
		{"bool", true, nil, Reply{Kind: KindBool, Bool: true}},
		{"big", big1, nil, Reply{Kind: KindBigInt, Str: "12345678901234567890"}},
		{"nil", nil, redis.Nil, Reply{Kind: KindNil}},
		{"nil value", nil, nil, Reply{Kind: KindNil}},
		{"error", nil, replyErr("WRONGTYPE Operation against a key"), Reply{Kind: KindError, Str: "WRONGTYPE Operation against a key"}},
	} {
		got, err := FromAny(tc.v, tc.err)
		if err != nil || !equal(got, tc.want) {
			t.Errorf("%s: FromAny = %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}

	arr, err := FromAny([]any{"a", int64(1), nil, replyErr("ERR x")}, nil)
	if err != nil || arr.Kind != KindArray || len(arr.Elems) != 4 ||
		arr.Elems[0].Str != "a" || arr.Elems[1].Int != 1 || arr.Elems[2].Kind != KindNil ||
		arr.Elems[3].Kind != KindError || arr.Elems[3].Str != "ERR x" {
		t.Errorf("array = %+v, %v", arr, err)
	}

	m, err := FromAny(map[any]any{"b": "2", "a": "1", int64(10): []any{"x"}}, nil)
	if err != nil || m.Kind != KindMap || len(m.Pairs) != 3 {
		t.Fatalf("map = %+v, %v", m, err)
	}
	if m.Pairs[0][0].Int != 10 || m.Pairs[1][0].Str != "a" || m.Pairs[1][1].Str != "1" || m.Pairs[2][0].Str != "b" ||
		m.Pairs[0][1].Kind != KindArray {
		t.Errorf("map order = %+v", m.Pairs)
	}

	if _, err := FromAny(nil, io.ErrUnexpectedEOF); !db.IsConnLost(err) {
		t.Errorf("network error = %v, want conn lost", err)
	}
}

func equal(a, b Reply) bool {
	return a.Kind == b.Kind && a.Str == b.Str && a.Int == b.Int && a.Float == b.Float && a.Bool == b.Bool &&
		len(a.Elems) == 0 && len(b.Elems) == 0 && len(a.Pairs) == 0 && len(b.Pairs) == 0
}
