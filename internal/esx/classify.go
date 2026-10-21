// Package esx holds the terminal- and network-free parts of the
// Elasticsearch support: request classification, the REST console file
// format, mappings, query strings, cell input and JSON paths.
package esx

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
)

// Level is how risky a request is (spec §9.1).
type Level int

const (
	LevelRead Level = iota
	LevelWrite
	LevelDanger
	LevelCritical
)

func (l Level) String() string {
	switch l {
	case LevelRead:
		return "read"
	case LevelWrite:
		return "write"
	case LevelDanger:
		return "danger"
	}
	return "critical"
}

// Class is a request's classification.
type Class struct {
	Level Level
	// Op names the request for confirmations and the audit log:
	// "delete index", "bulk", "index document", "refresh"…
	Op string
	// Targets is the index expression of the path's first segment, split
	// at commas; empty when the path has no index segment.
	Targets []string
	// Quiet writes need no confirmation off prod: refresh, flush, cache clear.
	Quiet bool
	// Counts are a _bulk body's actions by kind.
	Counts map[string]int
}

const opUnknown = "unknown request"

// readPosts are the APIs a POST only reads from, as the path after the
// index segment; "*" matches one segment.
var readPosts = []string{
	"_search", "_search/template", "_msearch", "_msearch/template", "_count", "_mget",
	"_explain/*", "_validate/query", "_field_caps", "_analyze", "_render/template", "_render/template/*",
	"_sql", "_sql/translate", "_query", "_eql/search", "_pit", "_search/point_in_time",
	"_search/scroll", "_ingest/pipeline/*/_simulate", "_ingest/pipeline/_simulate",
	"_termvectors", "_termvectors/*", "_mtermvectors", "_terms_enum", "_knn_search", "_rank_eval",
	"_cluster/allocation/explain", "_resolve/index/*",
}

// Classify grades a request by method and path, and for _bulk, _aliases,
// _delete_by_query and _settings by body. Anything it does not know is
// LevelDanger.
func Classify(method, path string, body []byte) Class {
	method = strings.ToUpper(method)
	segs := splitPath(path)
	var c Class
	rest := segs
	if len(segs) > 0 && (!strings.HasPrefix(segs[0], "_") || segs[0] == "_all") {
		c.Targets = strings.Split(segs[0], ",")
		rest = segs[1:]
	}
	api := strings.Join(rest, "/")
	set := func(l Level, op string) Class { c.Level, c.Op = l, op; return c }
	if method == "GET" || method == "HEAD" {
		return set(LevelRead, "read")
	}
	if method == "POST" && matchAny(readPosts, api) {
		return set(LevelRead, rest[0])
	}
	critical := func() Level {
		if wide(c.Targets) {
			return LevelCritical
		}
		return LevelDanger
	}
	switch method {
	case "DELETE":
		switch {
		case api == "" && c.Targets != nil:
			return set(critical(), "delete index")
		case api == "_pit":
			return set(LevelRead, "release pit")
		case api == "_search/scroll/_all", api == "_search/point_in_time/_all":
			return set(LevelDanger, "release all search contexts")
		case match("_search/scroll/*", api), api == "_search/scroll" && jsonHas(body, "scroll_id"):
			return set(LevelRead, "release scroll")
		case api == "_search/point_in_time" && jsonHas(body, "pit_id"):
			return set(LevelRead, "release pit")
		case api == "_search/scroll", api == "_search/point_in_time":
			return set(LevelDanger, "release search contexts") // no id: not ours alone
		case match("_doc/*", api):
			return set(LevelWrite, "delete document")
		case match("_alias/*", api), match("_aliases/*", api):
			return set(LevelWrite, "delete alias")
		case match("_data_stream/*", api):
			c.Targets = strings.Split(rest[1], ",")
			return set(critical(), "delete data stream")
		case firstOf(rest, "_index_template", "_template", "_component_template"):
			return set(LevelDanger, "delete template")
		case firstOf(rest, "_ingest"):
			return set(LevelDanger, "delete pipeline")
		case firstOf(rest, "_ilm"):
			return set(LevelDanger, "delete ilm policy")
		case firstOf(rest, "_snapshot"):
			return set(LevelDanger, "delete snapshot")
		case firstOf(rest, "_security"), match("_plugins/_security/api/*/*", api):
			return set(LevelDanger, "delete security object")
		}
	case "PATCH":
		if match("_plugins/_security/api/*/*", api) {
			return set(LevelWrite, "put security object")
		}
	case "PUT", "POST":
		switch {
		case api == "" && c.Targets != nil && method == "PUT":
			return set(LevelWrite, "create index")
		case api == "_doc", match("_doc/*", api):
			return set(LevelWrite, "index document")
		case match("_create/*", api):
			return set(LevelWrite, "create document")
		case match("_update/*", api) && method == "POST":
			return set(LevelWrite, "update document")
		case api == "_bulk":
			return classifyBulk(c, body)
		case api == "_aliases" && c.Targets == nil:
			return classifyAliases(c, body)
		case match("_alias/*", api), match("_aliases/*", api):
			return set(LevelWrite, "update aliases")
		case api == "_mapping":
			return set(LevelWrite, "update mapping")
		case api == "_settings":
			if c.Targets == nil || settingsRisky(body) {
				return set(LevelDanger, "update index settings")
			}
			return set(LevelWrite, "update index settings")
		case api == "_open":
			return set(LevelWrite, "open index")
		case api == "_close":
			return set(critical(), "close index")
		case api == "_refresh", api == "_flush", api == "_flush/synced":
			c.Quiet = true
			return set(LevelWrite, rest[0][1:])
		case api == "_cache/clear":
			c.Quiet = true
			return set(LevelWrite, "clear cache")
		case api == "_delete_by_query":
			if critical() == LevelCritical || !hasQuery(body) {
				return set(LevelCritical, "delete by query")
			}
			return set(LevelDanger, "delete by query")
		case api == "_update_by_query":
			return set(LevelDanger, "update by query")
		case api == "_forcemerge":
			return set(LevelDanger, "force merge")
		case api == "_reindex":
			return set(LevelWrite, "reindex")
		case firstOf(rest, "_rollover", "_shrink", "_split", "_clone"):
			return set(LevelDanger, rest[0][1:])
		case api == "_cluster/settings", api == "_cluster/reroute":
			return set(LevelDanger, "update cluster settings")
		case match("_tasks/*/_cancel", api), api == "_tasks/_cancel":
			return set(LevelWrite, "cancel task")
		case match("_data_stream/*", api):
			return set(LevelWrite, "create data stream")
		case firstOf(rest, "_index_template", "_template", "_component_template") && len(rest) == 2:
			return set(LevelWrite, "put template")
		case match("_ingest/pipeline/*", api):
			return set(LevelWrite, "put pipeline")
		case match("_ilm/policy/*", api):
			return set(LevelWrite, "put ilm policy")
		case match("_snapshot/*/*/_restore", api):
			return set(LevelDanger, "restore snapshot")
		case match("_snapshot/*/*", api):
			return set(LevelWrite, "create snapshot")
		case match("_snapshot/*", api):
			return set(LevelDanger, "put snapshot repository")
		case firstOf(rest, "_security") && len(rest) >= 3, match("_plugins/_security/api/*/*", api) && method == "PUT":
			return set(LevelWrite, "put security object")
		}
	}
	return set(LevelDanger, opUnknown)
}

// splitPath drops the query string and the slashes around the path.
func splitPath(path string) []string {
	path, _, _ = strings.Cut(path, "?")
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func match(pattern, api string) bool {
	ps, as := strings.Split(pattern, "/"), strings.Split(api, "/")
	if len(ps) != len(as) {
		return false
	}
	for i, p := range ps {
		if p != "*" && p != as[i] || as[i] == "" {
			return false
		}
	}
	return true
}

func matchAny(patterns []string, api string) bool {
	for _, p := range patterns {
		if match(p, api) {
			return true
		}
	}
	return false
}

func firstOf(rest []string, names ...string) bool {
	if len(rest) == 0 {
		return false
	}
	for _, n := range names {
		if rest[0] == n {
			return true
		}
	}
	return false
}

// wide reports an index expression that may name more than one index.
func wide(targets []string) bool {
	if len(targets) > 1 {
		return true
	}
	for _, t := range targets {
		if t == "_all" || strings.ContainsAny(t, "*?") {
			return true
		}
	}
	return false
}

func jsonHas(body []byte, key string) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// hasQuery reports a delete-by-query body with a query that is not match_all.
func hasQuery(body []byte) bool {
	var m struct {
		Query map[string]json.RawMessage `json:"query"`
	}
	if json.Unmarshal(body, &m) != nil || len(m.Query) == 0 {
		return false
	}
	_, all := m.Query["match_all"]
	return !all
}

// settingsRisky reports index settings that drop replicas or block the index.
func settingsRisky(body []byte) bool {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	risky := false
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		if obj, ok := v.(map[string]any); ok {
			for k, sub := range obj {
				walk(prefix+k+".", sub)
			}
			return
		}
		key := strings.TrimPrefix(strings.TrimSuffix(prefix, "."), "index.")
		switch {
		case strings.HasPrefix(key, "blocks."):
			risky = true
		case key == "number_of_replicas":
			if n, ok := v.(float64); ok && n == 0 {
				risky = true
			}
			if s, ok := v.(string); ok && s == "0" {
				risky = true
			}
		}
	}
	walk("", m)
	return risky
}

func classifyBulk(c Class, body []byte) Class {
	c.Level, c.Op, c.Counts = LevelWrite, "bulk", map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(nil, len(body)+1)
	source := false
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if source {
			source = false
			continue
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(line, &m) != nil || len(m) != 1 {
			c.Level = LevelDanger // not a bulk body we understand
			continue
		}
		for action := range m {
			c.Counts[action]++
			switch action {
			case "delete":
				c.Level = LevelDanger
			case "index", "create", "update":
				source = true
			default:
				c.Level = LevelDanger
			}
		}
	}
	return c
}

func classifyAliases(c Class, body []byte) Class {
	var b struct {
		Actions []map[string]struct {
			Index   string   `json:"index"`
			Indices []string `json:"indices"`
		} `json:"actions"`
	}
	c.Level, c.Op = LevelWrite, "update aliases"
	if json.Unmarshal(body, &b) != nil {
		return c
	}
	var removed []string
	for _, a := range b.Actions {
		if r, ok := a["remove_index"]; ok {
			if r.Index != "" {
				removed = append(removed, r.Index)
			}
			removed = append(removed, r.Indices...)
		}
	}
	if removed == nil {
		return c
	}
	c.Op, c.Targets, c.Level = "delete index", removed, LevelDanger
	if wide(removed) {
		c.Level = LevelCritical
	}
	return c
}

// SensitiveRequest reports a request whose body carries a password: it is
// kept out of the history and the confirmations (spec §9.4).
func SensitiveRequest(method, path string) bool {
	method = strings.ToUpper(method)
	api := strings.Join(splitPath(path), "/")
	switch {
	case match("_security/user/*/_password", api):
		return method == "PUT" || method == "POST"
	case match("_security/user/*", api):
		return method == "PUT" || method == "POST"
	case match("_plugins/_security/api/internalusers/*", api):
		return method == "PUT" || method == "PATCH"
	case api == "_plugins/_security/api/account":
		return method == "PUT"
	}
	return false
}
