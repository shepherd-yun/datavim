package esx

import (
	"slices"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	bulk := func(lines ...string) string { return strings.Join(lines, "\n") + "\n" }
	cases := []struct {
		method, path, body string
		level              Level
		op                 string
		targets            []string
		quiet              bool
	}{
		{"GET", "orders/_search", "", LevelRead, "", []string{"orders"}, false},
		{"get", "/orders/_search?q=a:1", "", LevelRead, "", []string{"orders"}, false},
		{"HEAD", "orders", "", LevelRead, "", []string{"orders"}, false},
		{"POST", "orders/_search", `{"query":{"match_all":{}}}`, LevelRead, "", []string{"orders"}, false},
		{"POST", "_sql?format=txt", `{"query":"select 1"}`, LevelRead, "", nil, false},
		{"POST", "orders/_count", "", LevelRead, "", []string{"orders"}, false},
		{"POST", "orders/_pit?keep_alive=5m", "", LevelRead, "", []string{"orders"}, false},
		{"DELETE", "_pit", `{"id":"x"}`, LevelRead, "", nil, false},
		{"POST", "_search/scroll", `{"scroll":"5m","scroll_id":"x"}`, LevelRead, "", nil, false},
		{"DELETE", "_search/scroll", `{"scroll_id":"x"}`, LevelRead, "", nil, false},
		{"DELETE", "_search/scroll/abc", "", LevelRead, "", nil, false},
		{"DELETE", "_search/scroll/_all", "", LevelDanger, "", nil, false},
		{"DELETE", "_search/point_in_time", `{"pit_id":["x"]}`, LevelRead, "", nil, false},
		{"DELETE", "_search/point_in_time/_all", "", LevelDanger, "", nil, false},
		{"POST", "_ingest/pipeline/p1/_simulate", `{}`, LevelRead, "", nil, false},
		{"POST", "orders/_doc", `{"a":1}`, LevelWrite, "index document", []string{"orders"}, false},
		{"PUT", "orders/_doc/1", `{"a":1}`, LevelWrite, "index document", []string{"orders"}, false},
		{"DELETE", "orders/_doc/1", "", LevelWrite, "delete document", []string{"orders"}, false},
		{"PUT", "orders/_create/1", `{}`, LevelWrite, "create document", []string{"orders"}, false},
		{"POST", "orders/_update/1", `{}`, LevelWrite, "update document", []string{"orders"}, false},
		{"PUT", "orders", `{}`, LevelWrite, "create index", []string{"orders"}, false},
		{"POST", "_refresh", "", LevelWrite, "refresh", nil, true},
		{"POST", "orders/_flush", "", LevelWrite, "flush", []string{"orders"}, true},
		{"POST", "orders/_cache/clear", "", LevelWrite, "clear cache", []string{"orders"}, true},
		{"POST", "orders/_open", "", LevelWrite, "open index", []string{"orders"}, false},
		{"PUT", "orders/_settings", `{"index":{"refresh_interval":"5s"}}`, LevelWrite, "update index settings", []string{"orders"}, false},
		{"PUT", "orders/_settings", `{"index":{"number_of_replicas":0}}`, LevelDanger, "update index settings", []string{"orders"}, false},
		{"PUT", "orders/_settings", `{"index.blocks.write":true}`, LevelDanger, "update index settings", []string{"orders"}, false},
		{"DELETE", "orders", "", LevelDanger, "delete index", []string{"orders"}, false},
		{"DELETE", "logs-*", "", LevelCritical, "delete index", []string{"logs-*"}, false},
		{"DELETE", "a,b", "", LevelCritical, "delete index", []string{"a", "b"}, false},
		{"DELETE", "_all", "", LevelCritical, "delete index", []string{"_all"}, false},
		{"POST", "orders/_close", "", LevelDanger, "close index", []string{"orders"}, false},
		{"POST", "logs-*/_close", "", LevelCritical, "close index", []string{"logs-*"}, false},
		{"POST", "orders/_delete_by_query", `{"query":{"term":{"a":1}}}`, LevelDanger, "delete by query", []string{"orders"}, false},
		{"POST", "orders/_delete_by_query", "", LevelCritical, "delete by query", []string{"orders"}, false},
		{"POST", "orders/_delete_by_query", `{"query":{"match_all":{}}}`, LevelCritical, "delete by query", []string{"orders"}, false},
		{"POST", "a,b/_delete_by_query", `{"query":{"term":{"a":1}}}`, LevelCritical, "delete by query", []string{"a", "b"}, false},
		{"POST", "orders/_update_by_query", `{}`, LevelDanger, "update by query", []string{"orders"}, false},
		{"POST", "_bulk", bulk(`{"index":{"_index":"t","_id":"1"}}`, `{"a":1}`), LevelWrite, "bulk", nil, false},
		{"POST", "_bulk", bulk(`{"index":{"_index":"t","_id":"1"}}`, `{"a":1}`, `{"delete":{"_index":"t","_id":"2"}}`), LevelDanger, "bulk", nil, false},
		{"POST", "_aliases", `{"actions":[{"add":{"index":"a","alias":"x"}}]}`, LevelWrite, "update aliases", nil, false},
		{"POST", "_aliases", `{"actions":[{"remove_index":{"index":"old"}}]}`, LevelDanger, "delete index", []string{"old"}, false},
		{"POST", "_aliases", `{"actions":[{"remove_index":{"index":"old-*"}}]}`, LevelCritical, "delete index", []string{"old-*"}, false},
		{"POST", "orders/_forcemerge?max_num_segments=1", "", LevelDanger, "force merge", []string{"orders"}, false},
		{"PUT", "_cluster/settings", `{}`, LevelDanger, "update cluster settings", nil, false},
		{"POST", "_reindex", `{}`, LevelWrite, "reindex", nil, false},
		{"POST", "_foo/bar", "", LevelDanger, "unknown request", nil, false},
		{"PATCH", "_plugins/_security/api/internalusers/bob", `[]`, LevelWrite, "put security object", nil, false},
		{"PUT", "_plugins/_security/api/roles/r", `{}`, LevelWrite, "put security object", nil, false},
		{"DELETE", "_plugins/_security/api/internalusers/bob", "", LevelDanger, "delete security object", nil, false},
		{"PATCH", "_anything", "", LevelDanger, "unknown request", nil, false},
		{"PUT", "_snapshot/r", `{}`, LevelDanger, "put snapshot repository", nil, false},
		{"PUT", "_snapshot/r/s", `{}`, LevelWrite, "create snapshot", nil, false},
		{"POST", "_snapshot/r/s/_restore", `{}`, LevelDanger, "restore snapshot", nil, false},
		{"POST", "orders", "", LevelDanger, "unknown request", []string{"orders"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			c := Classify(tc.method, tc.path, []byte(tc.body))
			if c.Level != tc.level {
				t.Errorf("Level = %v, want %v", c.Level, tc.level)
			}
			if tc.op != "" && c.Op != tc.op {
				t.Errorf("Op = %q, want %q", c.Op, tc.op)
			}
			if c.Op == "" {
				t.Error("Op is empty")
			}
			if !slices.Equal(c.Targets, tc.targets) {
				t.Errorf("Targets = %q, want %q", c.Targets, tc.targets)
			}
			if c.Quiet != tc.quiet {
				t.Errorf("Quiet = %v", c.Quiet)
			}
		})
	}
}

func TestClassifyBulkCounts(t *testing.T) {
	body := `{"index":{"_index":"t","_id":"1"}}
{"a":1}

{"create":{"_index":"t","_id":"2"}}
{"a":2}
{"update":{"_index":"t","_id":"3"}}
{"doc":{"a":3}}
{"delete":{"_index":"t","_id":"4"}}
{"index":{"_index":"t","_id":"5"}}
{"a":5}
`
	c := Classify("POST", "_bulk", []byte(body))
	want := map[string]int{"index": 2, "create": 1, "update": 1, "delete": 1}
	for k, v := range want {
		if c.Counts[k] != v {
			t.Errorf("Counts = %v, want %v", c.Counts, want)
			break
		}
	}
	if c.Level != LevelDanger {
		t.Errorf("Level = %v", c.Level)
	}
}

func TestLevelString(t *testing.T) {
	got := []string{LevelRead.String(), LevelWrite.String(), LevelDanger.String(), LevelCritical.String()}
	if !slices.Equal(got, []string{"read", "write", "danger", "critical"}) {
		t.Errorf("got %v", got)
	}
}

func TestEveryEndpointClassified(t *testing.T) {
	if len(Endpoints) < 40 {
		t.Fatalf("only %d endpoints", len(Endpoints))
	}
	for _, e := range Endpoints {
		p := strings.NewReplacer("{index}", "orders", "{id}", "1").Replace(e.Path)
		if c := Classify(e.Method, p, nil); c.Op == "unknown request" {
			t.Errorf("%s %s is not classified", e.Method, e.Path)
		}
	}
}

func TestSensitiveRequest(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{"PUT", "_security/user/bob", true},
		{"POST", "_security/user/bob", true},
		{"POST", "/_security/user/bob/_password", true},
		{"PUT", "_security/user/bob/_password?refresh=true", true},
		{"PUT", "_plugins/_security/api/internalusers/bob", true},
		{"PATCH", "_plugins/_security/api/internalusers/bob", true},
		{"PUT", "_plugins/_security/api/account", true},
		{"GET", "_security/user", false},
		{"GET", "_security/user/bob", false},
		{"PUT", "_security/user/bob/_enable", false},
		{"PUT", "_security/role/r", false},
		{"POST", "orders/_doc", false},
		{"DELETE", "_plugins/_security/api/internalusers/bob", false},
	}
	for _, tc := range cases {
		if got := SensitiveRequest(tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}
