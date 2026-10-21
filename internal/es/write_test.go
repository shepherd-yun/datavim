package es

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestWriteRequests(t *testing.T) {
	isWrite, notWrite := true, false
	replicas := "0"
	cases := []struct {
		name   string
		call   func(d Doer) error
		method string
		path   string
		body   string
	}{
		{"PutDoc", func(d Doer) error {
			_, err := PutDoc(ctx, d, "orders-2", "ord/1", "", json.RawMessage(`{"a":1}`), Version{1022, 3})
			return err
		}, "PUT", "orders-2/_doc/ord%2F1?if_seq_no=1022&if_primary_term=3&refresh=wait_for", `{"a":1}`},
		{"PutDoc routing", func(d Doer) error {
			_, err := PutDoc(ctx, d, "o", "1", "r 1", json.RawMessage(`{}`), Version{1, 1})
			return err
		}, "PUT", "o/_doc/1?if_seq_no=1&if_primary_term=1&refresh=wait_for&routing=r+1", `{}`},
		{"CreateDoc", func(d Doer) error { _, err := CreateDoc(ctx, d, "o", "", json.RawMessage(`{"a":1}`)); return err }, "POST", "o/_doc?refresh=wait_for", `{"a":1}`},
		{"CreateDoc id", func(d Doer) error { _, err := CreateDoc(ctx, d, "o", "x", json.RawMessage(`{"a":1}`)); return err }, "PUT", "o/_create/x?refresh=wait_for", `{"a":1}`},
		{"DeleteDoc", func(d Doer) error { return DeleteDoc(ctx, d, "o", "1", "", Version{5, 2}) }, "DELETE", "o/_doc/1?if_seq_no=5&if_primary_term=2&refresh=wait_for", ""},
		{"DeleteByQuery", func(d Doer) error {
			_, err := DeleteByQuery(ctx, d, []string{"a", "b"}, json.RawMessage(`{"term":{"x":1}}`))
			return err
		}, "POST", "a,b/_delete_by_query?wait_for_completion=false&conflicts=proceed", `{"query":{"term":{"x":1}}}`},
		{"CreateIndex", func(d Doer) error {
			return CreateIndex(ctx, d, NewIndex{Name: "n", Shards: 2, Replicas: 0, Mappings: json.RawMessage(`{"properties":{"a":{"type":"keyword"}}}`), Settings: json.RawMessage(`{"refresh_interval":"5s"}`)})
		}, "PUT", "n", `{"mappings":{"properties":{"a":{"type":"keyword"}}},"settings":{"number_of_replicas":0,"number_of_shards":2,"refresh_interval":"5s"}}`},
		{"DeleteIndices", func(d Doer) error { return DeleteIndices(ctx, d, []string{"a", "b"}) }, "DELETE", "a,b", ""},
		{"OpenIndex", func(d Doer) error { return OpenIndex(ctx, d, "a") }, "POST", "a/_open", ""},
		{"CloseIndex", func(d Doer) error { return CloseIndex(ctx, d, "a") }, "POST", "a/_close", ""},
		{"Refresh", func(d Doer) error { return Refresh(ctx, d, "a") }, "POST", "a/_refresh", ""},
		{"Flush", func(d Doer) error { return Flush(ctx, d, "a") }, "POST", "a/_flush", ""},
		{"ClearCache", func(d Doer) error { return ClearCache(ctx, d, "a") }, "POST", "a/_cache/clear", ""},
		{"ForceMerge", func(d Doer) error { return ForceMerge(ctx, d, "a", 1) }, "POST", "a/_forcemerge?max_num_segments=1", ""},
		{"UpdateAliases", func(d Doer) error {
			return UpdateAliases(ctx, d, []AliasAction{
				{Add: true, Index: "a", Alias: "x", IsWrite: &isWrite},
				{Add: true, Index: "b", Alias: "x", IsWrite: &notWrite, Filter: json.RawMessage(`{"term":{"s":1}}`), Routing: "1"},
				{Index: "c", Alias: "x"},
			})
		}, "POST", "_aliases", `{"actions":[{"add":{"alias":"x","index":"a","is_write_index":true}},{"add":{"alias":"x","filter":{"term":{"s":1}},"index":"b","is_write_index":false,"routing":"1"}},{"remove":{"alias":"x","index":"c"}}]}`},
		{"PutSettings", func(d Doer) error {
			return PutSettings(ctx, d, "a", map[string]*string{"index.number_of_replicas": &replicas, "index.refresh_interval": nil})
		}, "PUT", "a/_settings", `{"index.number_of_replicas":"0","index.refresh_interval":null}`},
		{"CancelTask", func(d Doer) error { return CancelTask(ctx, d, "n1:7") }, "POST", "_tasks/n1:7/_cancel", ""},
		{"Reindex", func(d Doer) error {
			_, err := Reindex(ctx, d, ReindexSpec{Source: "a", Dest: "b", Query: json.RawMessage(`{"term":{"x":1}}`), OpType: "create", Conflicts: "proceed"})
			return err
		}, "POST", "_reindex?wait_for_completion=false", `{"source":{"index":"a","query":{"term":{"x":1}}},"dest":{"index":"b","op_type":"create"},"conflicts":"proceed"}`},
		{"Reindex all", func(d Doer) error {
			_, err := Reindex(ctx, d, ReindexSpec{Source: "a", Dest: "b", OpType: "index", Conflicts: "abort"})
			return err
		}, "POST", "_reindex?wait_for_completion=false", `{"source":{"index":"a"},"dest":{"index":"b","op_type":"index"},"conflicts":"abort"}`},
		{"UpdateByQuery", func(d Doer) error {
			_, err := UpdateByQuery(ctx, d, []string{"a", "b"}, json.RawMessage(`{"term":{"x":1}}`), "ctx._source.y = 2")
			return err
		}, "POST", "a,b/_update_by_query?wait_for_completion=false", `{"query":{"term":{"x":1}},"script":{"source":"ctx._source.y = 2","lang":"painless"}}`},
		{"PutMapping", func(d Doer) error {
			return PutMapping(ctx, d, "a", json.RawMessage(`{"n":{"type":"text"}}`))
		}, "PUT", "a/_mapping", `{"properties":{"n":{"type":"text"}}}`},
		{"PutClusterSettings", func(d Doer) error {
			return PutClusterSettings(ctx, d, nil, map[string]*string{"b": &replicas})
		}, "PUT", "_cluster/settings", `{"transient":{"b":"0"}}`},
	}
	for _, tc := range cases {
		d := &fakeDoer{fn: func(Request) (Response, error) {
			return ok(`{"_id":"gen","_seq_no":9,"_primary_term":3,"task":"node:1"}`), nil
		}}
		if err := tc.call(d); err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		r := d.requests()[0]
		if r.Method != tc.method || r.Path != tc.path || string(r.Body) != tc.body {
			t.Errorf("%s: %s %s %s\nwant %s %s %s", tc.name, r.Method, r.Path, r.Body, tc.method, tc.path, tc.body)
		}
	}
}

func TestWriteResults(t *testing.T) {
	d := &fakeDoer{fn: func(Request) (Response, error) {
		return ok(`{"_id":"gen","_seq_no":9,"_primary_term":3,"task":"node:1"}`), nil
	}}
	v, err := PutDoc(ctx, d, "o", "1", "", json.RawMessage(`{}`), Version{1, 1})
	if err != nil || v != (Version{9, 3}) {
		t.Errorf("PutDoc = %v %v", v, err)
	}
	if id, err := CreateDoc(ctx, d, "o", "", json.RawMessage(`{}`)); err != nil || id != "gen" {
		t.Errorf("CreateDoc = %v %v", id, err)
	}
	if id, err := DeleteByQuery(ctx, d, []string{"o"}, nil); err != nil || id != "node:1" {
		t.Errorf("DeleteByQuery = %v %v", id, err)
	}
	if id, err := Reindex(ctx, d, ReindexSpec{Source: "a", Dest: "b"}); err != nil || id != "node:1" {
		t.Errorf("Reindex = %v %v", id, err)
	}
	if id, err := UpdateByQuery(ctx, d, []string{"o"}, nil, "x"); err != nil || id != "node:1" {
		t.Errorf("UpdateByQuery = %v %v", id, err)
	}
}

func TestPutDocConflict(t *testing.T) {
	d := &fakeDoer{fn: func(Request) (Response, error) {
		return Response{Status: 409, Body: []byte(`{"error":{"type":"version_conflict_engine_exception","reason":"[1]: version conflict, required seqNo [1]"},"status":409}`)}, nil
	}}
	if _, err := PutDoc(ctx, d, "o", "1", "", json.RawMessage(`{}`), Version{1, 1}); !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v", err)
	}
}

func TestDeleteIndicesRefusesPatterns(t *testing.T) {
	for _, names := range [][]string{{"logs-*"}, {"_all"}, {"a,b"}, {}, {""}, {"a?"}} {
		d := &fakeDoer{}
		if err := DeleteIndices(ctx, d, names); err == nil || len(d.requests()) != 0 {
			t.Errorf("%q: err %v, sent %d", names, err, len(d.requests()))
		}
	}
	d := &fakeDoer{}
	if _, err := DeleteByQuery(ctx, d, []string{"logs-*"}, nil); err == nil || len(d.requests()) != 0 {
		t.Errorf("DeleteByQuery pattern: %v", err)
	}
}

func TestGetTask(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{
		"GET _tasks/n:1": ok(`{"completed":true,"task":{"status":{"total":48000,"deleted":47990,"updated":0,"created":0}},"response":{"failures":[{"cause":"x"}]}}`),
		"GET _tasks/n:2": ok(`{"completed":false,"task":{"status":{"total":48000,"deleted":12000}}}`),
		"GET _tasks/n:3": ok(`{"completed":true,"task":{"status":{}},"error":{"type":"x","reason":"boom"}}`),
	}}
	cases := map[string]TaskStatus{
		"n:1": {Completed: true, Total: 48000, Deleted: 47990, Failures: 1},
		"n:2": {Total: 48000, Deleted: 12000},
		"n:3": {Completed: true, Err: "x: boom"},
	}
	for id, want := range cases {
		got, err := GetTask(ctx, d, id)
		if err != nil || got != want {
			t.Errorf("%s: %+v %v", id, got, err)
		}
	}
}

func TestWriteFuncsListed(t *testing.T) {
	if len(WriteFuncs) != 19 {
		t.Errorf("WriteFuncs = %v", WriteFuncs)
	}
	_ = fmt.Sprint
}
