package es

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

const catPath = "GET _cat/indices?format=json&bytes=b&expand_wildcards=all&h=health,status,index,uuid,pri,rep,docs.count,docs.deleted,store.size,pri.store.size,creation.date"

func TestIndices(t *testing.T) {
	for _, file := range []string{"cat_indices_7.json", "cat_indices_8.json"} {
		d := &fakeDoer{table: map[string]Response{catPath: fixture(t, file)}}
		got, err := Indices(ctx, d)
		if err != nil {
			t.Fatal(file, err)
		}
		o := got[0]
		if o.Name != "orders" || o.Health != "green" || o.Status != "open" || o.Docs != 8812301 || o.Deleted != 12 ||
			o.StoreBytes != 4400000000 || o.PriStoreBytes != 2200000000 || o.Primaries != 5 || o.Replicas != 1 ||
			!o.Created.Equal(time.UnixMilli(1772409600000)) || o.System {
			t.Errorf("%s: orders = %+v", file, o)
		}
		u := got[len(got)-1]
		if u.Name != "users" || u.Status != "close" || u.Docs != 0 {
			t.Errorf("%s: users = %+v", file, u)
		}
	}
	d := &fakeDoer{table: map[string]Response{catPath: fixture(t, "cat_indices_7.json")}}
	got, _ := Indices(ctx, d)
	if !got[1].System {
		t.Errorf(".kibana_1 = %+v", got[1])
	}
}

func TestAliases(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET _alias": ok(`{
	  "orders-1":{"aliases":{"orders":{"is_write_index":true},"paid":{"filter":{"term":{"status":"paid"}},"index_routing":"1","search_routing":"1"}}},
	  "orders-2":{"aliases":{"orders":{}}},
	  "logs":{"aliases":{}}
	}`)}}
	got, err := Aliases(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, a := range got {
		s = append(s, a.Name+">"+a.Index)
	}
	if !slices.Equal(s, []string{"orders>orders-1", "orders>orders-2", "paid>orders-1"}) {
		t.Errorf("aliases = %v", s)
	}
	if !got[0].IsWrite || got[1].IsWrite || string(got[2].Filter) != `{"term":{"status":"paid"}}` || got[2].Routing != "1" {
		t.Errorf("aliases = %+v", got)
	}
}

func TestDataStreams(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET _data_stream": ok(`{"data_streams":[{"name":"logs-app","template":"logs","indices":[{"index_name":".ds-logs-app-2026.10.21-000001"}]}]}`)}}
	got, err := DataStreams(ctx, d)
	if err != nil || len(got) != 1 || got[0].Name != "logs-app" || got[0].Template != "logs" || got[0].Indices[0] != ".ds-logs-app-2026.10.21-000001" {
		t.Errorf("got %+v %v", got, err)
	}
	for _, resp := range []Response{
		ok(`{"data_streams":[]}`),
		{Status: 400, Body: []byte(`{"error":"no handler found for uri [/_data_stream] and method [GET]"}`)},
		{Status: 404, Body: []byte(`{}`)},
	} {
		d := &fakeDoer{table: map[string]Response{"GET _data_stream": resp}}
		if got, err := DataStreams(ctx, d); err != nil || len(got) != 0 {
			t.Errorf("%d: %v %v", resp.Status, got, err)
		}
	}
}

func TestMappings(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET orders/_mapping": ok(`{
	  "orders-1":{"mappings":{"properties":{"a":{"type":"keyword"}}}},
	  "orders-2":{"mappings":{}}
	}`)}}
	got, err := Mappings(ctx, d, "orders")
	if err != nil || len(got) != 2 || got["orders-1"][0].Path != "a" || len(got["orders-2"]) != 0 {
		t.Errorf("got %+v %v", got, err)
	}
}

func TestSettings(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET orders/_settings?include_defaults=true&flat_settings=true": ok(`{"orders":{
	  "settings":{"index.number_of_shards":"5","index.refresh_interval":"5s","index.routing.allocation.include._tier_preference":"data_content"},
	  "defaults":{"index.max_result_window":"10000","index.analysis.x":["a","b"]}
	}}`)}}
	got, err := Settings(ctx, d, "orders")
	if err != nil {
		t.Fatal(err)
	}
	find := func(k string) Setting {
		for _, s := range got {
			if s.Key == k {
				return s
			}
		}
		t.Fatalf("no %s in %+v", k, got)
		return Setting{}
	}
	if s := find("index.refresh_interval"); !s.Dynamic || s.Default || s.Value != "5s" {
		t.Errorf("refresh_interval = %+v", s)
	}
	if s := find("index.number_of_shards"); s.Dynamic {
		t.Errorf("number_of_shards = %+v", s)
	}
	if s := find("index.max_result_window"); !s.Default || !s.Dynamic {
		t.Errorf("max_result_window = %+v", s)
	}
	if s := find("index.analysis.x"); s.Value != `["a","b"]` {
		t.Errorf("array = %+v", s)
	}
	if !slices.IsSortedFunc(got, func(a, b Setting) int { return strings.Compare(a.Key, b.Key) }) {
		t.Error("not sorted")
	}
}

func TestStats(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET orders/_stats": ok(`{"indices":{
	  "orders-2":{"primaries":{"docs":{"count":5,"deleted":1},"store":{"size_in_bytes":100}},
	              "total":{"docs":{"count":10},"store":{"size_in_bytes":200},"segments":{"count":3},"refresh":{"total":4},"flush":{"total":2},"merges":{"total":1},"search":{"query_total":9},"indexing":{"index_total":7}}},
	  "orders-1":{"primaries":{},"total":{}}
	}}`)}}
	got, err := Stats(ctx, d, "orders")
	if err != nil || len(got) != 2 || got[0].Index != "orders-1" {
		t.Fatalf("got %+v %v", got, err)
	}
	want := IndexStats{Index: "orders-2", Docs: 5, Deleted: 1, PriStoreBytes: 100, StoreBytes: 200, Segments: 3, Refreshes: 4, Flushes: 2, Merges: 1, Queries: 9, Indexed: 7}
	if got[1] != want {
		t.Errorf("got %+v", got[1])
	}
}

func TestClusterHealthAndStats(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{
		"GET _cluster/health": ok(`{"cluster_name":"c","status":"yellow","number_of_nodes":3,"number_of_data_nodes":2,"active_primary_shards":10,"active_shards":15,"relocating_shards":1,"initializing_shards":2,"unassigned_shards":3,"number_of_pending_tasks":4}`),
		"GET _cluster/stats":  ok(`{"indices":{"count":156,"shards":{"total":1004},"docs":{"count":89000000},"store":{"size_in_bytes":58600000000}}}`),
	}}
	h, err := ClusterHealth(ctx, d)
	want := Health{Status: "yellow", Nodes: 3, DataNodes: 2, ActivePrimary: 10, Active: 15, Relocating: 1, Initializing: 2, Unassigned: 3, PendingTasks: 4}
	if err != nil || h != want {
		t.Errorf("health %+v %v", h, err)
	}
	s, err := GetClusterStats(ctx, d)
	if err != nil || s != (ClusterStats{Indices: 156, Shards: 1004, Docs: 89000000, StoreBytes: 58600000000}) {
		t.Errorf("stats %+v %v", s, err)
	}
}

func TestGetDoc(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{
		"GET orders/_doc/ord%2F1": ok(`{"_index":"orders","_id":"ord/1","_version":4,"_seq_no":1022,"_primary_term":3,"_routing":"r","found":true,"_source":{"a":1}}`),
		"GET orders/_doc/nope":    {Status: 404, Body: []byte(`{"_index":"orders","_id":"nope","found":false}`)},
	}}
	h, err := GetDoc(ctx, d, "orders", "ord/1")
	if err != nil || h.Index != "orders" || h.ID != "ord/1" || h.SeqNo != 1022 || h.PrimaryTerm != 3 || h.Routing != "r" || h.Version != 4 || string(h.Source) != `{"a":1}` {
		t.Errorf("got %+v %v", h, err)
	}
	if _, err := GetDoc(ctx, d, "orders", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}

func TestGetDocTruncates(t *testing.T) {
	big := `{"small":"x","big":"` + strings.Repeat("y", MaxSource) + `","after":1}`
	d := &fakeDoer{table: map[string]Response{"GET orders/_doc/1": ok(`{"_index":"orders","_id":"1","_seq_no":1,"_primary_term":1,"found":true,"_source":` + big + `}`)}}
	h, err := GetDoc(ctx, d, "orders", "1")
	if err != nil || !h.Truncated || h.SourceSize != len(big) {
		t.Fatalf("got truncated=%v size=%d %v", h.Truncated, h.SourceSize, err)
	}
	if string(h.Source) != `{"small":"x","after":1}` {
		t.Errorf("preview = %.80s", h.Source)
	}
}

func TestCount(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"POST orders/_count": ok(`{"count":12}`)}}
	n, err := Count(ctx, d, "orders", json.RawMessage(`{"term":{"a":1}}`))
	if err != nil || n != 12 || string(d.requests()[0].Body) != `{"query":{"term":{"a":1}}}` {
		t.Errorf("n %d err %v body %s", n, err, d.requests()[0].Body)
	}
}

func TestResolveIndex(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET _resolve/index/logs-*,orders?expand_wildcards=open,closed": ok(`{
	  "indices":[{"name":"logs-1"},{"name":"logs-2"}],
	  "aliases":[{"name":"orders","indices":["orders-2","orders-1"]}],
	  "data_streams":[{"name":"logs-ds","backing_indices":[".ds-logs-ds-000001"]}]
	}`)}}
	got, err := ResolveIndex(ctx, d, "logs-*,orders")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Indices, []string{".ds-logs-ds-000001", "logs-1", "logs-2", "orders-1", "orders-2"}) ||
		!slices.Equal(got.Aliases, []string{"orders"}) || !slices.Equal(got.DataStreams, []string{"logs-ds"}) {
		t.Errorf("got %+v", got)
	}
	d = &fakeDoer{table: map[string]Response{
		"GET _resolve/index/logs-*?expand_wildcards=open,closed":                   {Status: 400, Body: []byte(`{"error":"no handler found for uri [/_resolve/index/logs-*] and method [GET]"}`)},
		"GET _cat/indices/logs-*?format=json&h=index&expand_wildcards=open,closed": ok(`[{"index":"logs-2"},{"index":"logs-1"}]`),
	}}
	got, err = ResolveIndex(ctx, d, "logs-*")
	if err != nil || !slices.Equal(got.Indices, []string{"logs-1", "logs-2"}) {
		t.Errorf("fallback: %+v %v", got, err)
	}
}
