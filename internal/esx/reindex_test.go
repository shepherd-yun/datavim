package esx

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestCopyableSettings(t *testing.T) {
	var r map[string]struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(testdata(t, "settings_7.json"), &r); err != nil {
		t.Fatal(err)
	}
	got, shards, replicas := CopyableSettings(r["orders"].Settings)
	if shards != 5 || replicas != 1 {
		t.Errorf("shards %d replicas %d", shards, replicas)
	}
	var keys []string
	for k := range got {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"index.analysis.analyzer.my.tokenizer", "index.analysis.analyzer.my.type", "index.refresh_interval"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v", keys)
	}
}

func TestMappingConflicts(t *testing.T) {
	existing := []Field{{Path: "status", Type: "keyword"}, {Path: "user", Type: "object", Object: true}, {Path: "user.name", Type: "keyword"}}
	added, conflicts, err := MappingConflicts(existing, json.RawMessage(`{"status":{"type":"long"},"note":{"type":"text"},"user":{"properties":{"name":{"type":"keyword"},"age":{"type":"integer"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(added, []string{"note", "user.age"}) || !slices.Equal(conflicts, []string{"status: keyword → long"}) {
		t.Errorf("added %v conflicts %v", added, conflicts)
	}
	if _, _, err := MappingConflicts(existing, json.RawMessage(`{oops`)); err == nil {
		t.Error("bad json accepted")
	}
}
