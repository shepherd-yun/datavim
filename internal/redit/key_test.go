package redit

import (
	"testing"
)

func TestCapsFor(t *testing.T) {
	for v, want := range map[string]bool{"7.2.4": true, "6.0.0": true, "10.0": true, "5.0.14": false, "": false, "x": false} {
		if got := CapsFor(v).KeepTTL; got != want {
			t.Errorf("CapsFor(%q).KeepTTL = %v", v, got)
		}
	}
}

func TestString(t *testing.T) {
	p := String(bk, "old", "v", Caps{KeepTTL: true})
	eq(t, "cmds", p.Cmds, [][]string{{"SET", bk, "v", "KEEPTTL"}})
	eq(t, "checks", checks(p), []checkWant{{args: []string{"GET", bk}, expect: ExpectValue, value: "old"}})
	if p.RestoreTTL || p.N != 1 || p.Key != bk {
		t.Fatalf("plan = %+v", p)
	}
	p = String("k", "old", "v", Caps{})
	eq(t, "cmds", p.Cmds, [][]string{{"SET", "k", "v"}})
	eq(t, "checks", checks(p), []checkWant{{args: []string{"GET", "k"}, expect: ExpectValue, value: "old"}})
	if !p.RestoreTTL {
		t.Fatal("RestoreTTL without KEEPTTL")
	}
}

func TestNewKey(t *testing.T) {
	cases := []struct {
		typ  string
		args []string
		cmds [][]string // nil: an error
	}{
		{"string", []string{"v"}, [][]string{{"SET", "k", "v"}}},
		{"string", []string{"a", "b"}, nil},
		{"hash", []string{"f", "v", "g", "w"}, [][]string{{"HSET", "k", "f", "v", "g", "w"}}},
		{"hash", []string{"f"}, nil},
		{"list", []string{"a", "b"}, [][]string{{"RPUSH", "k", "a", "b"}}},
		{"list", nil, nil},
		{"set", []string{"m"}, [][]string{{"SADD", "k", "m"}}},
		{"set", nil, nil},
		{"zset", []string{"1.5", "m", "2", "n"}, [][]string{{"ZADD", "k", "1.5", "m", "2", "n"}}},
		{"zset", []string{"a", "m"}, nil},
		{"zset", []string{"1"}, nil},
		{"stream", []string{"f", "v"}, [][]string{{"XADD", "k", "*", "f", "v"}}},
		{"stream", []string{"f", "v", "g"}, nil},
	}
	for _, c := range cases {
		p, err := NewKey(c.typ, "k", c.args)
		if c.cmds == nil {
			if err == nil || err.Error() != "expected "+Usage(c.typ) {
				t.Errorf("%s %q: err = %v", c.typ, c.args, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s %q: %v", c.typ, c.args, err)
			continue
		}
		eq(t, c.typ+" cmds", p.Cmds, c.cmds)
		eq(t, c.typ+" checks", checks(p), []checkWant{{args: []string{"EXISTS", "k"}, expect: ExpectInt}})
		if p.Checks[0].What != "key already exists" || p.N != 1 || p.Key != "k" {
			t.Errorf("%s: plan = %+v", c.typ, p)
		}
	}
	if Usage("zset") != "score member [score member …]" {
		t.Fatalf("usage = %q", Usage("zset"))
	}
}
