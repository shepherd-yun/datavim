package rcmd

import (
	"slices"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := map[Level][]string{
		Read: {
			"GET a", "HGETALL h", "SCAN 0", "TTL a", "TYPE a", "INFO", "DBSIZE", "EVAL_RO s 0", "FCALL_RO f 0",
			"SELECT 1", "PING", "CONFIG GET *", "CLIENT LIST", "CLIENT INFO", "SLOWLOG GET", "MEMORY USAGE k",
			"OBJECT ENCODING k", "MULTI", "EXEC", "DISCARD", "WATCH k", "UNWATCH", "AUTH p", "ECHO x",
			"TIME", "ROLE", "LASTSAVE", "COMMAND DOCS get", "COMMAND",
		},
		Write: {"SET a 1", "DEL a", "UNLINK a", "HSET h f v", "EXPIRE a 1", "RENAME a b", "EVAL s 0", "EVALSHA x 0", "FCALL f 0", "SCRIPT LOAD x", "FUNCTION LOAD x"},
		Dangerous: {
			"FLUSHDB", "FLUSHALL", "KEYS *", "CONFIG SET a b", "CONFIG REWRITE", "CONFIG RESETSTAT", "SHUTDOWN",
			"DEBUG SLEEP 0", "SCRIPT FLUSH", "SCRIPT KILL", "FUNCTION FLUSH", "FUNCTION DELETE x", "FUNCTION RESTORE x",
			"FUNCTION KILL", "CLIENT KILL ID 1", "CLIENT PAUSE 1", "CLIENT NO-EVICT on", "REPLICAOF no one",
			"SLAVEOF no one", "MIGRATE h 1 k 0 1", "ACL SETUSER u", "ACL DELUSER u", "ACL LOAD", "ACL SAVE", "SAVE",
			"BGSAVE", "BGREWRITEAOF", "SWAPDB 0 1", "MODULE LOAD x", "MODULE LOADEX x", "MODULE UNLOAD x",
			"FAILOVER", "CLUSTER INFO", "SLOWLOG RESET", "LATENCY RESET", "MEMORY PURGE",
		},
		Unsupported: {"SUBSCRIBE ch", "PSUBSCRIBE p", "SSUBSCRIBE c", "MONITOR", "SYNC", "PSYNC a 1", "HELLO 3", "RESET", "CLIENT TRACKING on", "CLIENT REPLY OFF", "QUIT"},
		Unknown:     {"JSON.SET k $ 1", "FOO", "CONFIG NOPE"},
	}
	for want, lines := range cases {
		for _, line := range lines {
			args, _ := Split(line)
			if got, name := Classify(args); got != want {
				t.Errorf("Classify(%q) = %v (%s), want %v", line, got, name, want)
			}
		}
	}
	for line, want := range map[string]string{"config Set a b": "CONFIG SET", "foo x": "FOO", "config nope": "CONFIG NOPE", "client tracking on": "CLIENT TRACKING", "get a": "GET"} {
		args, _ := Split(line)
		lvl, name := Classify(args)
		if name != want {
			t.Errorf("Classify(%q) name = %q, want %q", line, name, want)
		}
		if line == "config Set a b" && lvl != Dangerous {
			t.Errorf("mixed case level = %v", lvl)
		}
	}
	if Read.String() != "read" || Unknown.String() != "unknown" || Dangerous.String() != "dangerous" {
		t.Errorf("Level.String")
	}
}

func TestGuard(t *testing.T) {
	ro := Policy{ReadOnly: true, Conn: "cache", DBSize: -1}
	prod := Policy{Prod: true, Conn: "cache", DBSize: -1}
	dev := Policy{Conn: "cache", DBSize: -1}
	for _, tc := range []struct {
		name    string
		text    string
		p       Policy
		verdict Verdict
		line    int
		reason  string
		title   string
		expect  string
		body    []string
	}{
		{name: "ro read", text: "GET a", p: ro, verdict: Allow},
		{name: "ro write", text: "SET a 1", p: ro, verdict: Reject, reason: ":set noreadonly"},
		{name: "ro unknown", text: "FOO", p: ro, verdict: Reject, reason: ":set noreadonly"},
		{name: "ro danger", text: "FLUSHDB", p: ro, verdict: Reject, reason: ":set noreadonly"},
		{name: "ro multi", text: "MULTI\nSET a 1\nEXEC", p: ro, verdict: Reject, line: 1},
		{name: "ro batch", text: "GET a\nSET b 1", p: ro, verdict: Reject, line: 1},
		{name: "prod write", text: "SET a 1", p: prod, verdict: Confirm, title: "Write to PROD", body: []string{"1 command(s) will run on PROD cache:", "write: SET a 1"}},
		{name: "prod unknown", text: "FOO", p: prod, verdict: Confirm, title: "Write to PROD", body: []string{"unknown: FOO"}},
		{name: "prod read", text: "GET a", p: prod, verdict: Allow},
		{name: "prod two writes", text: "SET a 1\nDEL b", p: prod, verdict: Confirm, body: []string{"2 command(s) will run on PROD cache:", "write: SET a 1", "write: DEL b"}},
		{name: "dev unknown", text: "FOO", p: dev, verdict: Allow},
		{name: "dev write", text: "SET a 1", p: dev, verdict: Allow},
		{name: "dev danger", text: "FLUSHDB", p: dev, verdict: Confirm, title: "Dangerous command", body: []string{"dangerous: FLUSHDB"}},
		{name: "prod danger", text: "FLUSHDB", p: prod, verdict: Confirm, title: "PROD · Dangerous command", expect: "cache"},
		{name: "prod mixed", text: "SET a 1\nFLUSHDB", p: prod, verdict: Confirm, title: "PROD · Dangerous command", expect: "cache", body: []string{"write: SET a 1", "dangerous: FLUSHDB"}},
		{name: "keys big", text: "KEYS *", p: Policy{Conn: "cache", DBSize: 20000}, verdict: Reject, reason: "SCAN"},
		{name: "keys small", text: "KEYS *", p: Policy{Conn: "cache", DBSize: 50}, verdict: Confirm, title: "Dangerous command"},
		{name: "unsupported", text: "SUBSCRIBE ch", p: dev, verdict: Reject, reason: "SUBSCRIBE is not supported in sqlvim"},
		{name: "unsupported prod", text: "GET a\nMONITOR", p: prod, verdict: Reject, line: 1, reason: "MONITOR is not supported in sqlvim"},
		{name: "select in multi", text: "MULTI\nSELECT 2", p: dev, verdict: Reject, line: 1, reason: "SELECT inside MULTI"},
		{name: "select already multi", text: "SELECT 2", p: Policy{InMulti: true, DBSize: -1}, verdict: Reject, reason: "SELECT inside MULTI"},
		{name: "select before multi", text: "SELECT 2\nMULTI", p: dev, verdict: Allow},
		{name: "select after exec", text: "MULTI\nEXEC\nSELECT 2", p: dev, verdict: Allow},
		{name: "split error", text: `SET "a`, p: dev, verdict: Reject, reason: "unterminated quote"},
		{name: "empty", text: "", p: ro, verdict: Allow},
	} {
		c := Guard(Lines(tc.text), tc.p)
		if c.Verdict != tc.verdict || c.Line != tc.line || !strings.Contains(c.Reason, tc.reason) ||
			(tc.title != "" && c.Title != tc.title) || c.Expect != tc.expect {
			t.Errorf("%s: Guard = %+v", tc.name, c)
		}
		for _, b := range tc.body {
			if !slices.Contains(c.Body, b) {
				t.Errorf("%s: Body %q lacks %q", tc.name, c.Body, b)
			}
		}
	}
	long := "SET a " + strings.Repeat("x", 100)
	c := Guard(Lines(long), Policy{Prod: true, Conn: "cache", DBSize: -1})
	if got := c.Body[len(c.Body)-1]; len([]rune(got)) > len("write: ")+61 || !strings.HasSuffix(got, "…") {
		t.Errorf("long body line = %q", got)
	}
	c = Guard(Lines("AUTH secret\nSET a 1"), Policy{Prod: true, Conn: "cache", DBSize: -1})
	if strings.Contains(strings.Join(c.Body, "\n"), "secret") {
		t.Errorf("Body shows the password: %q", c.Body)
	}
}

func TestNeedsDBSize(t *testing.T) {
	if !NeedsDBSize(Lines("GET a\nkeys *")) {
		t.Errorf("keys * not detected")
	}
	if NeedsDBSize(Lines("GET keys")) {
		t.Errorf("GET keys detected")
	}
}

func TestGuardArgs(t *testing.T) {
	cmds := [][]string{{"HSET", "k", "f", "v"}}
	ro := GuardArgs(cmds, Policy{ReadOnly: true, Conn: "cache", DBSize: -1})
	if ro.Verdict != Reject || !strings.Contains(ro.Reason, ":set noreadonly") {
		t.Fatalf("read-only: %+v", ro)
	}
	if c := GuardArgs(cmds, Policy{Prod: true, Conn: "cache", DBSize: -1}); c.Verdict != Confirm || c.Expect != "" {
		t.Fatalf("prod: %+v", c)
	}
	if c := GuardArgs(cmds, Policy{Conn: "cache", DBSize: -1}); c.Verdict != Allow {
		t.Fatalf("dev: %+v", c)
	}
	spaced := GuardArgs([][]string{{"HSET", "a b", "f\xff", "v\n"}}, Policy{Prod: true, Conn: "cache", DBSize: -1})
	if spaced.Verdict != Confirm || len(spaced.Body) != 2 || !strings.Contains(spaced.Body[1], `write: HSET "a b"`) {
		t.Fatalf("quoted args stay one write command: %+v", spaced)
	}
}

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, s string
		want       bool
	}{
		{"*", "a/b:c", true},
		{"user:*", "user:1", true},
		{"user:*", "users", false},
		{"h?llo", "hello", true},
		{"h[ae]llo", "hallo", true},
		{"h[^e]llo", "hello", false},
		{"h[a-c]llo", "hbllo", true},
		{`a\*b`, "a*b", true},
		{`a\*b`, "axb", false},
		{"*x", "\xffx", true},
		{"", "a", false},
	} {
		if got := Match(c.pattern, c.s); got != c.want {
			t.Errorf("Match(%q, %q) = %v", c.pattern, c.s, got)
		}
	}
}
