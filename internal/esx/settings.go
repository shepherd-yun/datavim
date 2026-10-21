package esx

import (
	"encoding/json"
	"path"
	"slices"
	"strconv"
	"strings"
)

// dynamicSettings are the index settings that can change on an open
// index, without the "index." prefix; "*" matches within a segment.
var dynamicSettings = []string{
	"number_of_replicas", "auto_expand_replicas", "refresh_interval", "max_result_window",
	"max_inner_result_window", "max_rescore_window", "max_docvalue_fields_search", "max_script_fields",
	"max_ngram_diff", "max_shingle_diff", "max_refresh_listeners", "analyze.max_token_count",
	"highlight.max_analyzed_offset", "max_terms_count", "max_regex_length", "max_slices_per_scroll",
	"routing.allocation.*", "routing.allocation.*.*", "routing.allocation.*.*.*", "routing.rebalance.enable",
	"gc_deletes", "default_pipeline", "final_pipeline", "hidden", "blocks.*", "search.idle.after",
	"translog.durability", "translog.sync_interval", "translog.flush_threshold_size", "translog.retention.*",
	"merge.scheduler.max_thread_count", "merge.policy.*", "unassigned.node_left.delayed_timeout", "priority",
	"query.default_field", "mapping.*.limit", "mapping.*.*.limit", "mapping.ignore_malformed", "mapping.coerce",
	"lifecycle.*", "plugins.index_state_management.*", "search.slowlog.*", "search.slowlog.*.*",
	"search.slowlog.*.*.*", "indexing.slowlog.*", "indexing.slowlog.*.*", "indexing.slowlog.*.*.*",
	"write.wait_for_active_shards", "requests.cache.enable", "soft_deletes.retention_lease.period",
	"queries.cache.enabled", "shard.check_on_startup",
}

// DynamicSetting reports an index setting ("index.refresh_interval")
// that can change without closing the index.
func DynamicSetting(key string) bool {
	key = strings.TrimPrefix(key, "index.")
	for _, p := range dynamicSettings {
		if ok, _ := path.Match(strings.ReplaceAll(p, ".", "/"), strings.ReplaceAll(key, ".", "/")); ok {
			return true
		}
	}
	return false
}

// uncopyable are the index settings a new index cannot take from the one
// it copies; "*" matches the rest of the key.
var uncopyable = []string{
	"index.uuid", "index.creation_date", "index.provided_name", "index.version.*", "index.history.uuid",
	"index.resize.*", "index.routing.allocation.initial_recovery.*", "index.blocks.*",
	"index.lifecycle.indexing_complete",
}

// CopyableSettings are an index's flat settings that a new index can be
// created with, and its shard and replica counts.
func CopyableSettings(flat map[string]json.RawMessage) (settings map[string]json.RawMessage, shards, replicas int) {
	settings = map[string]json.RawMessage{}
	count := func(raw json.RawMessage) int {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			s = string(raw)
		}
		n, _ := strconv.Atoi(s)
		return n
	}
	for k, v := range flat {
		switch k {
		case "index.number_of_shards":
			shards = count(v)
			continue
		case "index.number_of_replicas":
			replicas = count(v)
			continue
		}
		if slices.ContainsFunc(uncopyable, func(p string) bool {
			if prefix, ok := strings.CutSuffix(p, "*"); ok {
				return strings.HasPrefix(k, prefix)
			}
			return k == p
		}) {
			continue
		}
		settings[k] = v
	}
	return settings, shards, replicas
}
