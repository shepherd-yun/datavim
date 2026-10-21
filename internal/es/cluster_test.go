package es

import (
	"encoding/json"
	"slices"
	"testing"
)

const (
	nodesPath  = "GET _cat/nodes?format=json&bytes=b&full_id=true&h=id,name,ip,node.role,master,heap.percent,ram.percent,cpu,load_1m,disk.used_percent,disk.used,disk.total,version"
	shardsPath = "?format=json&bytes=b&h=index,shard,prirep,state,docs,store,node,unassigned.reason"
)

func TestNodes(t *testing.T) {
	for _, file := range []string{"cat_nodes_7.json", "cat_nodes_8.json"} {
		d := &fakeDoer{table: map[string]Response{nodesPath: fixture(t, file)}}
		got, err := Nodes(ctx, d)
		if err != nil {
			t.Fatal(file, err)
		}
		if len(got) != 2 {
			t.Fatalf("%s: %d nodes", file, len(got))
		}
		n := got[0]
		if n.ID != "n1aaaa" || n.Name != "es-1" || n.IP != "10.0.0.1" || !n.Master || n.HeapPct != 62 || n.RAMPct != 91 ||
			n.CPUPct != 7 || n.Load1m != 0.52 || n.DiskUsedPct != 41.27 || n.DiskUsed != 44300000000 || n.DiskTotal != 107300000000 {
			t.Errorf("%s: node 0 = %+v", file, n)
		}
		if got[1].Master || got[1].HeapPct != 90 {
			t.Errorf("%s: node 1 = %+v", file, got[1])
		}
	}
	d := &fakeDoer{table: map[string]Response{nodesPath: fixture(t, "cat_nodes_7.json")}}
	got, _ := Nodes(ctx, d)
	if n := got[1]; n.Load1m != -1 || n.DiskUsedPct != -1 || n.DiskUsed != -1 || n.DiskTotal != -1 || n.Roles != "dilrt" || n.Version != "7.10.2" {
		t.Errorf("null columns = %+v", n)
	}
}

func TestAllocation(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET _cat/allocation?format=json&bytes=b": fixture(t, "cat_allocation.json")}}
	got, err := Allocation(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	want := []NodeAllocation{
		{Node: "es-1", IP: "10.0.0.1", Shards: 12, DiskIndices: 5000000, DiskUsed: 44300000000, DiskAvail: 63000000000, DiskTotal: 107300000000, DiskPct: 41},
		{Node: "UNASSIGNED", Shards: 3, DiskIndices: -1, DiskUsed: -1, DiskAvail: -1, DiskTotal: -1, DiskPct: -1},
	}
	if !slices.Equal(got, want) {
		t.Errorf("allocation = %+v", got)
	}
}

func TestShards(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET _cat/shards" + shardsPath: fixture(t, "cat_shards_7.json")}}
	got, err := Shards(ctx, d, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []Shard{
		{Index: "orders", Shard: 0, Primary: true, State: "STARTED", Node: "es-1", Docs: 1200, Bytes: 340000},
		{Index: "orders", Shard: 0, State: "UNASSIGNED", Docs: -1, Bytes: -1, Reason: "INDEX_CREATED"},
		{Index: "logs-2026.10", Shard: 1, Primary: true, State: "RELOCATING", Node: "es-1 -> 10.0.0.2 n2bbbb es-2", Docs: 5, Bytes: 900},
	}
	if !slices.Equal(got, want) {
		t.Errorf("shards = %+v", got)
	}
	d = &fakeDoer{table: map[string]Response{"GET _cat/shards/logs-*" + shardsPath: ok(`[]`)}}
	if _, err := Shards(ctx, d, "logs-*"); err != nil {
		t.Error(err)
	}
}

func TestPendingTasks(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET _cluster/pending_tasks": fixture(t, "pending_tasks.json")}}
	got, err := PendingTasks(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	want := []PendingTask{
		{Order: 101, Priority: "URGENT", Source: "create-index [foo_9], cause [api]", Waiting: "86ms"},
		{Order: 46, Priority: "HIGH", Source: "shard-started", Waiting: "842ms"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("pending = %+v", got)
	}
}

func TestClusterSettings(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET _cluster/settings?include_defaults=true&flat_settings=true": fixture(t, "cluster_settings.json")}}
	got, err := ClusterSettings(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	want := []ClusterSetting{
		{Key: "cluster.max_shards_per_node", Value: "2000", Scope: "persistent"},
		{Key: "cluster.routing.allocation.awareness.attributes", Value: `["zone"]`, Scope: "persistent"},
		{Key: "cluster.routing.allocation.enable", Value: "primaries", Scope: "transient"},
		{Key: "action.auto_create_index", Value: "true", Scope: "defaults"},
		{Key: "cluster.routing.allocation.disk.watermark.high", Value: "90%", Scope: "defaults"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("settings = %+v", got)
	}
}

func TestAllocationExplain(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"POST _cluster/allocation/explain": ok(`{"index":"o"}`)}}
	got, err := AllocationExplain(ctx, d, "o", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"index":"o"}` {
		t.Errorf("explain = %s", got)
	}
	if b := string(d.requests()[0].Body); b != `{"index":"o","shard":0,"primary":false}` {
		t.Errorf("body = %s", b)
	}
}

func TestNodeStats(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{
		"GET _nodes/n1/stats/jvm,os,fs,process,indices,thread_pool": ok(`{"nodes":{"n1":{"name":"es-1"}}}`),
	}}
	got, err := NodeStats(ctx, d, "n1")
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Name string }
	if json.Unmarshal(got, &v) != nil || v.Name != "es-1" {
		t.Errorf("stats = %s", got)
	}
}
