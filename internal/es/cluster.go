package es

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/shepherd-yun/datavim/internal/esx"
)

// numOr reads a _cat number, def when it is null or not a number.
func numOr(v *string, def int64) int64 {
	if v == nil {
		return def
	}
	n, err := strconv.ParseInt(*v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

// floatOr reads a _cat decimal, def when it is null or not a number.
func floatOr(v *string, def float64) float64 {
	if v == nil {
		return def
	}
	f, err := strconv.ParseFloat(*v, 64)
	if err != nil {
		return def
	}
	return f
}

func str(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// catRows reads a _cat?format=json response.
func catRows(ctx context.Context, d Doer, path string) ([]map[string]*string, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: path})
	if err != nil {
		return nil, err
	}
	var rows []map[string]*string
	err = json.Unmarshal(resp.Body, &rows)
	return rows, err
}

// Node is one row of _cat/nodes.
type Node struct {
	ID, Name, IP, Roles, Version string // Roles is _cat's abbreviation, "dimr"
	Master                       bool   // the elected master
	HeapPct, RAMPct, CPUPct      int
	Load1m                       float64 // -1 when not reported
	DiskUsedPct                  float64 // -1 when not reported
	DiskUsed, DiskTotal          int64   // -1 when not reported
}

// Nodes lists the cluster's nodes.
func Nodes(ctx context.Context, d Doer) ([]Node, error) {
	rows, err := catRows(ctx, d, "_cat/nodes?format=json&bytes=b&full_id=true&h=id,name,ip,node.role,master,heap.percent,ram.percent,cpu,load_1m,disk.used_percent,disk.used,disk.total,version")
	if err != nil {
		return nil, err
	}
	out := make([]Node, 0, len(rows))
	for _, r := range rows {
		out = append(out, Node{
			ID: str(r["id"]), Name: str(r["name"]), IP: str(r["ip"]), Roles: str(r["node.role"]), Version: str(r["version"]),
			Master:  str(r["master"]) == "*",
			HeapPct: int(numOr(r["heap.percent"], 0)), RAMPct: int(numOr(r["ram.percent"], 0)), CPUPct: int(numOr(r["cpu"], 0)),
			Load1m: floatOr(r["load_1m"], -1), DiskUsedPct: floatOr(r["disk.used_percent"], -1),
			DiskUsed: numOr(r["disk.used"], -1), DiskTotal: numOr(r["disk.total"], -1),
		})
	}
	return out, nil
}

// NodeAllocation is one row of _cat/allocation.
type NodeAllocation struct {
	Node, IP                                    string // Node is "UNASSIGNED" for the unassigned shards' row
	Shards                                      int
	DiskIndices, DiskUsed, DiskAvail, DiskTotal int64 // -1 when not reported
	DiskPct                                     int   // -1 when not reported
}

// Allocation lists the shards and disk of every data node.
func Allocation(ctx context.Context, d Doer) ([]NodeAllocation, error) {
	rows, err := catRows(ctx, d, "_cat/allocation?format=json&bytes=b")
	if err != nil {
		return nil, err
	}
	out := make([]NodeAllocation, 0, len(rows))
	for _, r := range rows {
		out = append(out, NodeAllocation{
			Node: str(r["node"]), IP: str(r["ip"]), Shards: int(numOr(r["shards"], 0)),
			DiskIndices: numOr(r["disk.indices"], -1), DiskUsed: numOr(r["disk.used"], -1),
			DiskAvail: numOr(r["disk.avail"], -1), DiskTotal: numOr(r["disk.total"], -1),
			DiskPct: int(numOr(r["disk.percent"], -1)),
		})
	}
	return out, nil
}

// Shard is one row of _cat/shards.
type Shard struct {
	Index       string
	Shard       int
	Primary     bool
	State, Node string // Node is empty when unassigned
	Docs, Bytes int64  // -1 when not reported
	Reason      string // unassigned.reason
}

// Shards lists the shards of index, every index when it is empty.
func Shards(ctx context.Context, d Doer, index string) ([]Shard, error) {
	p := "_cat/shards"
	if index != "" {
		p += "/" + indexPath(index)
	}
	rows, err := catRows(ctx, d, p+"?format=json&bytes=b&h=index,shard,prirep,state,docs,store,node,unassigned.reason")
	if err != nil {
		return nil, err
	}
	out := make([]Shard, 0, len(rows))
	for _, r := range rows {
		out = append(out, Shard{
			Index: str(r["index"]), Shard: int(numOr(r["shard"], 0)), Primary: str(r["prirep"]) == "p",
			State: str(r["state"]), Node: str(r["node"]),
			Docs: numOr(r["docs"], -1), Bytes: numOr(r["store"], -1), Reason: str(r["unassigned.reason"]),
		})
	}
	return out, nil
}

// PendingTask is one queued cluster state update.
type PendingTask struct {
	Order                     int64
	Priority, Source, Waiting string
}

// PendingTasks reads _cluster/pending_tasks.
func PendingTasks(ctx context.Context, d Doer) ([]PendingTask, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_cluster/pending_tasks"})
	if err != nil {
		return nil, err
	}
	var r struct {
		Tasks []struct {
			Order    int64  `json:"insert_order"`
			Priority string `json:"priority"`
			Source   string `json:"source"`
			Waiting  string `json:"time_in_queue"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	out := make([]PendingTask, 0, len(r.Tasks))
	for _, t := range r.Tasks {
		out = append(out, PendingTask{t.Order, t.Priority, t.Source, t.Waiting})
	}
	return out, nil
}

// AllocationExplain explains why a shard copy is where it is, or why it
// is unassigned.
func AllocationExplain(ctx context.Context, d Doer, index string, shard int, primary bool) (json.RawMessage, error) {
	b, _ := json.Marshal(struct {
		Index   string `json:"index"`
		Shard   int    `json:"shard"`
		Primary bool   `json:"primary"`
	}{index, shard, primary})
	resp, err := do(ctx, d, Request{Method: "POST", Path: "_cluster/allocation/explain", Body: b})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// NodeStats are one node's _nodes/stats.
func NodeStats(ctx context.Context, d Doer, id string) (json.RawMessage, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_nodes/" + url.PathEscape(id) + "/stats/jvm,os,fs,process,indices,thread_pool"})
	if err != nil {
		return nil, err
	}
	var r struct {
		Nodes map[string]json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	if s, ok := r.Nodes[id]; ok {
		return s, nil
	}
	for _, s := range r.Nodes {
		return s, nil
	}
	return nil, ErrNotFound
}

// ClusterSetting is one cluster setting.
type ClusterSetting struct {
	Key, Value string
	Scope      string // "persistent", "transient" or "defaults"
}

var settingScopes = []string{"persistent", "transient", "defaults"}

// ClusterSettings are the cluster's settings with the defaults, by scope
// then key.
func ClusterSettings(ctx context.Context, d Doer) ([]ClusterSetting, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_cluster/settings?include_defaults=true&flat_settings=true"})
	if err != nil {
		return nil, err
	}
	var r map[string]map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	var out []ClusterSetting
	for _, scope := range settingScopes {
		for k, raw := range r[scope] {
			text, null := esx.CellText(raw, true)
			if null {
				text = "null"
			}
			out = append(out, ClusterSetting{Key: k, Value: text, Scope: scope})
		}
	}
	slices.SortFunc(out, func(a, b ClusterSetting) int {
		if c := slices.Index(settingScopes, a.Scope) - slices.Index(settingScopes, b.Scope); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out, nil
}
