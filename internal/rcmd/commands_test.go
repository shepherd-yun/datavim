package rcmd

import (
	"slices"
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	for _, tc := range []struct {
		args []string
		name string
		ok   bool
	}{
		{[]string{"hset", "k", "f", "v"}, "HSET", true},
		{[]string{"config", "set", "maxmemory", "1"}, "CONFIG SET", true},
		{[]string{"Client", "Kill", "ID", "3"}, "CLIENT KILL", true},
		{[]string{"CONFIG", "nope"}, "CONFIG", false},
		{[]string{"CONFIG"}, "CONFIG", false},
		{[]string{"nope"}, "", false},
		{[]string{"json.get", "k"}, "", false},
		{nil, "", false},
	} {
		c, ok := Lookup(tc.args)
		if ok != tc.ok || c.Name != tc.name {
			t.Errorf("Lookup(%q) = %q %v, want %q %v", tc.args, c.Name, ok, tc.name, tc.ok)
		}
	}
	if c, _ := Lookup([]string{"hset"}); !strings.HasPrefix(c.Args, "key field value") || c.Group != "hash" {
		t.Errorf("HSET = %+v", c)
	}
}

func TestKeyArg(t *testing.T) {
	for _, tc := range []struct {
		line string
		keys []int
	}{
		{"HSET k f v", []int{1}},
		{"MSET a 1 b 2", []int{1, 3}},
		{"DEL a b c", []int{1, 2, 3}},
		{`EVAL "return 1" 2 k1 k2 arg`, []int{3, 4}},
		{`fcall_ro f 1 k arg`, []int{3}},
		{"GET", []int{1}},
		{"PING x", nil},
		{"OBJECT ENCODING k", []int{2}},
		{"NOPE a", nil},
	} {
		args, _ := Split(tc.line)
		c, _ := Lookup(args)
		n := len(args) + 1
		var got []int
		for i := 1; i < n; i++ {
			if KeyArg(c, args, i) {
				got = append(got, i)
			}
		}
		if !slices.Equal(got, tc.keys) {
			t.Errorf("KeyArg(%q) = %v, want %v", tc.line, got, tc.keys)
		}
	}
}

func TestPairReply(t *testing.T) {
	for line, want := range map[string]bool{
		"HGETALL k":                      true,
		"ZRANGE k 0 -1":                  false,
		"ZRANGE k 0 -1 withscores":       true,
		"zrangebyscore k 0 1 WITHSCORES": true,
		"ZUNION 2 a b WITHSCORES":        true,
		"CONFIG GET *":                   true,
		"ZPOPMIN k":                      false,
		"ZPOPMIN k 2":                    true,
		"ZPOPMAX k 2":                    true,
		"HRANDFIELD k 3 WITHVALUES":      true,
		"HRANDFIELD k 3":                 false,
		"ZRANDMEMBER k 3 WITHSCORES":     true,
		"GET k":                          false,
	} {
		args, _ := Split(line)
		if got := PairReply(args); got != want {
			t.Errorf("PairReply(%q) = %v, want %v", line, got, want)
		}
	}
}

func TestNames(t *testing.T) {
	names := Names()
	if !slices.Contains(names, "GET") || !slices.Contains(names, "SCAN") || slices.Contains(names, "CONFIG SET") {
		t.Errorf("Names = %v", names)
	}
	if !slices.IsSorted(names) {
		t.Errorf("Names not sorted")
	}
	subs := SubNames("client")
	if !slices.Contains(subs, "KILL") || !slices.Contains(subs, "LIST") || !slices.IsSorted(subs) {
		t.Errorf("SubNames(CLIENT) = %v", subs)
	}
	if got := SubNames("GET"); len(got) != 0 {
		t.Errorf("SubNames(GET) = %v", got)
	}
}
