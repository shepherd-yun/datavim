package rcmd

import (
	"strings"
	"testing"
)

func TestCompleteAt(t *testing.T) {
	for _, tc := range []struct {
		line   string
		offset int // -1: end of line
		kind   CompKind
		prefix string
		start  int
		hint   string
	}{
		{line: "hs", offset: 2, kind: CompCommand, prefix: "hs", start: 0},
		{line: "", offset: 0, kind: CompCommand, prefix: "", start: 0},
		{line: "  ge", offset: -1, kind: CompCommand, prefix: "ge", start: 2},
		{line: "HSET us", offset: 7, kind: CompKey, prefix: "us", start: 5, hint: "HSET key field value"},
		{line: "HSET ", offset: 5, kind: CompKey, prefix: "", start: 5, hint: "HSET key field value"},
		{line: "HSET user:1 f", offset: 13, kind: CompNone, prefix: "f", start: 12, hint: "HSET key field value"},
		{line: "CONFIG s", offset: -1, kind: CompSubcommand, prefix: "s", start: 7, hint: "CONFIG"},
		{line: `GET "a b`, offset: -1, kind: CompKey, prefix: "a b", start: 4, hint: "GET key"},
		{line: "MSET a 1 b", offset: -1, kind: CompKey, prefix: "b", start: 9},
		{line: "MSET a 1 b ", offset: -1, kind: CompNone, prefix: "", start: 11},
		{line: "GET a", offset: 1, kind: CompCommand, prefix: "G", start: 0},
		{line: "NOPE x", offset: -1, kind: CompNone, prefix: "x", start: 5},
		{line: "CONFIG SET maxmemory", offset: -1, kind: CompNone, hint: "CONFIG SET parameter value", prefix: "maxmemory", start: 11},
	} {
		off := tc.offset
		if off < 0 {
			off = len(tc.line)
		}
		c := CompleteAt(tc.line, off)
		if c.Kind != tc.kind || c.Prefix != tc.prefix || c.Start != tc.start || !strings.HasPrefix(c.Hint, tc.hint) {
			t.Errorf("CompleteAt(%q, %d) = %+v", tc.line, off, c)
		}
		if tc.hint == "" && tc.kind == CompCommand && c.Hint != "" {
			t.Errorf("CompleteAt(%q) hint = %q", tc.line, c.Hint)
		}
	}
	if c := CompleteAt("HSET us", 7); c.Command.Name != "HSET" {
		t.Errorf("Command = %+v", c.Command)
	}
}
