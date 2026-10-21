package esx

// Keyword tables of the console's body completion (spec §6.3).
var (
	topKeys = []string{
		"query", "size", "from", "sort", "_source", "aggs", "aggregations", "highlight", "track_total_hits",
		"search_after", "post_filter", "fields", "docvalue_fields", "stored_fields", "script_fields",
		"collapse", "min_score", "timeout", "terminate_after", "explain", "version", "seq_no_primary_term",
		"pit", "runtime_mappings", "suggest", "rescore", "indices_boost", "profile",
	}
	queryKeys = []string{
		"bool", "term", "terms", "match", "match_all", "match_none", "match_phrase", "match_phrase_prefix",
		"multi_match", "range", "exists", "ids", "wildcard", "prefix", "regexp", "fuzzy", "nested",
		"query_string", "simple_query_string", "function_score", "constant_score", "dis_max", "has_child",
		"has_parent", "geo_distance", "geo_bounding_box", "script", "terms_set", "combined_fields",
	}
	boolKeys = []string{"must", "filter", "should", "must_not", "minimum_should_match", "boost"}
	aggKeys  = []string{
		"terms", "avg", "sum", "min", "max", "value_count", "cardinality", "stats", "extended_stats",
		"percentiles", "date_histogram", "histogram", "range", "date_range", "filter", "filters",
		"top_hits", "nested", "reverse_nested", "composite", "significant_terms", "missing", "aggs",
	}
	aggParamKeys   = []string{"field", "size", "order", "script", "missing", "interval", "calendar_interval", "fixed_interval", "format", "min_doc_count", "include", "exclude", "shard_size"}
	highlightKeys  = []string{"fields", "pre_tags", "post_tags", "fragment_size", "number_of_fragments", "require_field_match", "type", "order"}
	fieldQueries   = map[string]bool{"term": true, "terms": true, "match": true, "match_phrase": true, "match_phrase_prefix": true, "range": true, "prefix": true, "wildcard": true, "regexp": true, "fuzzy": true, "terms_set": true}
	fieldValueKeys = map[string]bool{"field": true, "path": true, "default_field": true}
	fieldArrayKeys = map[string]bool{"_source": true, "sort": true, "fields": true, "includes": true, "excludes": true, "docvalue_fields": true, "stored_fields": true}
	queryParents   = map[string]bool{"query": true, "must": true, "filter": true, "should": true, "must_not": true, "post_filter": true, "positive": true, "negative": true, "queries": true}
)
