package esx

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

func TestFlattenMapping(t *testing.T) {
	raw := `{
	  "_source": {"enabled": true},
	  "properties": {
	    "title": {"type": "text", "analyzer": "ik_smart", "fields": {"keyword": {"type": "keyword"}}, "copy_to": "all"},
	    "user": {"properties": {"name": {"type": "keyword"}, "age": {"type": "integer", "index": false}}},
	    "items": {"type": "nested", "properties": {"sku": {"type": "keyword", "doc_values": false}}},
	    "tags": {"type": "keyword", "copy_to": ["all", "x"]}
	  }
	}`
	got, err := FlattenMapping(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := []Field{
		{Path: "items", Type: "nested", Object: true, Indexed: true},
		{Path: "items.sku", Type: "keyword", Indexed: true},
		{Path: "tags", Type: "keyword", Indexed: true, DocValues: true, CopyTo: []string{"all", "x"}},
		{Path: "title", Type: "text", Keyword: "title.keyword", Indexed: true, Analyzer: "ik_smart", Sub: []string{"title.keyword"}, CopyTo: []string{"all"}},
		{Path: "user", Type: "object", Object: true, Indexed: true},
		{Path: "user.age", Type: "integer", DocValues: true},
		{Path: "user.name", Type: "keyword", Indexed: true, DocValues: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got\n%+v\nwant\n%+v", got, want)
	}
	for _, empty := range []string{`{}`, `{"properties":{}}`, ``} {
		got, err := FlattenMapping(json.RawMessage(empty))
		if err != nil || len(got) != 0 {
			t.Errorf("%q: %v %v", empty, got, err)
		}
	}
}

func TestMergeMappings(t *testing.T) {
	got := MergeMappings(map[string][]Field{
		"a": {{Path: "code", Type: "keyword"}, {Path: "x", Type: "long"}},
		"b": {{Path: "code", Type: "long"}, {Path: "y", Type: "date"}},
	})
	var paths, types []string
	for _, f := range got {
		paths, types = append(paths, f.Path), append(types, f.Type)
	}
	if !slices.Equal(paths, []string{"code", "x", "y"}) || !slices.Equal(types, []string{"keyword|long", "long", "date"}) {
		t.Errorf("got %v %v", paths, types)
	}
}

func TestDefaultColumns(t *testing.T) {
	fields := []Field{
		{Path: "body", Type: "text"},
		{Path: "title", Type: "text", Keyword: "title.keyword"},
		{Path: "status", Type: "keyword"},
		{Path: "amount", Type: "double"},
		{Path: "user", Type: "object", Object: true},
		{Path: "user.name", Type: "keyword"},
		{Path: "loc", Type: "geo_point"},
		{Path: "vec", Type: "dense_vector"},
		{Path: "blob", Type: "binary"},
		{Path: "created_at", Type: "date"},
		{Path: "ok", Type: "boolean"},
	}
	got := DefaultColumns(fields, 12)
	want := []string{"status", "amount", "user.name", "created_at", "ok", "title"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	var many []Field
	for i := range 20 {
		many = append(many, Field{Path: string(rune('a' + i)), Type: "keyword"})
	}
	if got := DefaultColumns(many, 12); len(got) != 12 {
		t.Errorf("got %d columns", len(got))
	}
}
