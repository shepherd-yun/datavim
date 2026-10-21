package es

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

const tasksPath = "GET _tasks?actions=*reindex,*byquery,*forcemerge,cluster:admin/snapshot/*,indices:admin/resize*&detailed=true&group_by=none"

func TestTasks(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{tasksPath: fixture(t, "tasks.json")}}
	got, err := Tasks(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, k := range got {
		ids = append(ids, k.ID)
	}
	if !slices.Equal(ids, []string{"n1:9", "n1:7", "n2:4"}) {
		t.Fatalf("ids = %v", ids)
	}
	r := got[1]
	if r.Node != "n1" || r.Action != "indices:data/write/reindex" || r.Description != "reindex from [orders] to [orders-v2][_doc]" ||
		r.Running != 5*time.Second || !r.Cancellable || r.Total != 88000 || r.Done != 12000 {
		t.Errorf("reindex = %+v", r)
	}
	if fm := got[2]; fm.Total != -1 || fm.Done != -1 || fm.Cancellable {
		t.Errorf("forcemerge = %+v", fm)
	}
}

func TestCancelTask(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"POST _tasks/n1:12/_cancel": ok(`{"nodes":{}}`)}}
	if err := CancelTask(ctx, d, "n1:12"); err != nil {
		t.Error(err)
	}
	d = &fakeDoer{table: map[string]Response{"POST _tasks/n1:12/_cancel": {Status: 404, Body: []byte(`{"error":{"type":"resource_not_found_exception","reason":"task [n1:12] is not found"},"status":404}`)}}}
	if err := CancelTask(ctx, d, "n1:12"); !errors.Is(err, ErrTaskGone) {
		t.Errorf("gone = %v", err)
	}
	// 7.x answers a finished task with 200 and node_failures
	d = &fakeDoer{table: map[string]Response{"POST _tasks/n1:12/_cancel": ok(`{"node_failures":[{"type":"failed_node_exception","caused_by":{"type":"resource_not_found_exception","reason":"task [n1:12] is not found"}}],"nodes":{}}`)}}
	if err := CancelTask(ctx, d, "n1:12"); !errors.Is(err, ErrTaskGone) {
		t.Errorf("gone (node_failures) = %v", err)
	}
}

func TestUpdateByQueryRefusesPatterns(t *testing.T) {
	d := &fakeDoer{}
	if _, err := UpdateByQuery(ctx, d, []string{"logs-*"}, nil, "x"); err == nil {
		t.Error("pattern accepted")
	}
	if len(d.requests()) != 0 {
		t.Error("a request was sent")
	}
}

func TestPutClusterSettings(t *testing.T) {
	d := &fakeDoer{fn: func(Request) (Response, error) { return ok(`{}`), nil }}
	s := func(v string) *string { return &v }
	err := PutClusterSettings(ctx, d, map[string]*string{"a": s("1"), "b": nil, "c": s(`["x"]`), "d": s("[oops"), "e": s(`{"k":1}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := d.requests()[0]
	if r.Method != "PUT" || r.Path != "_cluster/settings" ||
		string(r.Body) != `{"persistent":{"a":"1","b":null,"c":["x"],"d":"[oops","e":{"k":1}}}` {
		t.Errorf("request = %s %s %s", r.Method, r.Path, r.Body)
	}
	d = &fakeDoer{}
	if err := PutClusterSettings(ctx, d, nil, map[string]*string{}); err != nil || len(d.requests()) != 0 {
		t.Errorf("empty: %v, %d requests", err, len(d.requests()))
	}
}

func TestIndexExists(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"HEAD a": ok(""), "HEAD c": {Status: 500}}}
	for name, want := range map[string]bool{"a": true, "b": false} {
		if got, err := IndexExists(ctx, d, name); err != nil || got != want {
			t.Errorf("%s: %v, %v", name, got, err)
		}
	}
	if _, err := IndexExists(ctx, d, "c"); err == nil {
		t.Error("500 is not an error")
	}
}

func TestRawSettings(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET orders/_settings?flat_settings=true": fixture(t, "settings_7.json")}}
	got, err := RawSettings(ctx, d, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if string(got["index.refresh_interval"]) != `"5s"` {
		t.Errorf("settings = %v", got)
	}
	_ = json.Valid
}
