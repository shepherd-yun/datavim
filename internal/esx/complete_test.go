package esx

import (
	"slices"
	"strings"
	"testing"
)

func TestCompleteAt(t *testing.T) {
	cases := []struct {
		name   string
		text   string // | marks the cursor
		kind   CompleteKind
		prefix string
		index  string
		keys   []string
	}{
		{"method", "GE|", CompleteMethod, "GE", "", nil},
		{"method empty line", "GET a\n\n|", CompleteMethod, "", "", nil},
		{"index", "GET or|", CompleteIndex, "or", "", nil},
		{"index after slash", "GET /or|", CompleteIndex, "or", "", nil},
		{"path", "GET orders/_se|", CompletePath, "_se", "orders", nil},
		{"top key", "GET orders/_search\n{\"qu|", CompleteKey, "qu", "orders", []string{"query", "size", "aggs"}},
		{"query key", "GET orders/_search\n{\"query\":{\"b|", CompleteKey, "b", "orders", []string{"bool", "term", "match"}},
		{"bool key", "GET orders/_search\n{\"query\":{\"bool\":{\"|", CompleteKey, "", "orders", []string{"must", "filter", "should", "must_not"}},
		{"in must array", "GET orders/_search\n{\"query\":{\"bool\":{\"must\":[{\"te|", CompleteKey, "te", "orders", []string{"term"}},
		{"term field", "GET orders/_search\n{\"query\":{\"term\":{\"|", CompleteField, "", "orders", nil},
		{"multi-line term field", "GET orders/_search\n{\n  \"query\": {\n    \"range\": {\n      \"cre|", CompleteField, "cre", "orders", nil},
		{"sort", "GET orders/_search\n{\"sort\":[{\"|", CompleteField, "", "orders", nil},
		{"sort string", "GET orders/_search\n{\"sort\":[\"am|", CompleteField, "am", "orders", nil},
		{"source", "GET orders/_search\n{\"_source\":[\"us|", CompleteField, "us", "orders", nil},
		{"field value", "GET orders/_search\n{\"aggs\":{\"x\":{\"terms\":{\"field\":\"st|", CompleteField, "st", "orders", nil},
		{"agg type", "GET orders/_search\n{\"aggs\":{\"x\":{\"|", CompleteKey, "", "orders", []string{"terms", "avg"}},
		{"agg name", "GET orders/_search\n{\"aggs\":{\"|", CompleteNone, "", "", nil},
		{"string value", "GET orders/_search\n{\"query\":{\"query_string\":{\"query\":\"pa|", CompleteNone, "", "", nil},
		{"after a full string", "GET orders/_search\n{\"query\":{\"term\":{\"a\":\"x\", \"|", CompleteField, "", "orders", nil},
		{"no index", "GET _search\n{\"query\":{\"term\":{\"|", CompleteField, "", "", nil},
		{"second request", "GET a/_search\n{}\n\nPOST b/_search\n{\"query\":{\"match\":{\"|", CompleteField, "", "b", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, _, _ := strings.Cut(tc.text, "|")
			text := strings.Replace(tc.text, "|", "", 1)
			lines := strings.Split(before, "\n")
			line, col := len(lines)-1, len(lines[len(lines)-1])
			got := CompleteAt(text, line, col)
			if got.Kind != tc.kind {
				t.Fatalf("Kind = %v, want %v (%+v)", got.Kind, tc.kind, got)
			}
			if tc.kind == CompleteNone {
				return
			}
			if got.Prefix != tc.prefix || got.Index != tc.index {
				t.Errorf("got %+v", got)
			}
			for _, k := range tc.keys {
				if !slices.Contains(got.Keys, k) {
					t.Errorf("Keys %v lack %q", got.Keys, k)
				}
			}
		})
	}
}
