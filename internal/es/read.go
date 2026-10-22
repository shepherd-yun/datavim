package es

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shepherd-yun/datavim/internal/esx"
)

// indexPath escapes an index expression for a path, keeping its
// wildcards and commas.
func indexPath(expr string) string {
	return strings.NewReplacer("%2A", "*", "%2C", ",").Replace(url.PathEscape(expr))
}

// MaxSource is the _source size past which a document is shown as a
// preview and cannot be edited.
const MaxSource = 1 << 20

// IndexInfo is one row of _cat/indices.
type IndexInfo struct {
	Name, Health, Status, UUID               string
	Docs, Deleted, StoreBytes, PriStoreBytes int64
	Primaries, Replicas                      int
	Created                                  time.Time
	System                                   bool // a "." name
}

// Closed reports a closed index.
func (i IndexInfo) Closed() bool { return i.Status == "close" }

// num reads a _cat number, which is a string or null.
func num(v *string) int64 {
	if v == nil {
		return 0
	}
	n, _ := strconv.ParseInt(*v, 10, 64)
	return n
}

// Indices lists every index, hidden and closed ones included.
func Indices(ctx context.Context, d Doer) ([]IndexInfo, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_cat/indices?format=json&bytes=b&expand_wildcards=all&h=health,status,index,uuid,pri,rep,docs.count,docs.deleted,store.size,pri.store.size,creation.date"})
	if err != nil {
		return nil, err
	}
	var rows []map[string]*string
	if err := json.Unmarshal(resp.Body, &rows); err != nil {
		return nil, err
	}
	str := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	out := make([]IndexInfo, 0, len(rows))
	for _, r := range rows {
		i := IndexInfo{
			Name: str(r["index"]), Health: str(r["health"]), Status: str(r["status"]), UUID: str(r["uuid"]),
			Docs: num(r["docs.count"]), Deleted: num(r["docs.deleted"]),
			StoreBytes: num(r["store.size"]), PriStoreBytes: num(r["pri.store.size"]),
			Primaries: int(num(r["pri"])), Replicas: int(num(r["rep"])),
		}
		if ms := num(r["creation.date"]); ms > 0 {
			i.Created = time.UnixMilli(ms)
		}
		i.System = strings.HasPrefix(i.Name, ".")
		out = append(out, i)
	}
	return out, nil
}

// Alias is one alias → index pair.
type Alias struct {
	Name, Index string
	IsWrite     bool
	Filter      json.RawMessage
	Routing     string
}

// Aliases lists the aliases of every index, by alias then index name.
func Aliases(ctx context.Context, d Doer) ([]Alias, error) {
	return aliasesOf(ctx, d, "_alias")
}

// IndexAliases lists the aliases of the indices target names.
func IndexAliases(ctx context.Context, d Doer, target string) ([]Alias, error) {
	return aliasesOf(ctx, d, indexPath(target)+"/_alias")
}

func aliasesOf(ctx context.Context, d Doer, path string) ([]Alias, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: path})
	if err != nil {
		return nil, err
	}
	var r map[string]struct {
		Aliases map[string]struct {
			IsWrite       bool            `json:"is_write_index"`
			Filter        json.RawMessage `json:"filter"`
			Routing       string          `json:"routing"`
			IndexRouting  string          `json:"index_routing"`
			SearchRouting string          `json:"search_routing"`
		} `json:"aliases"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	var out []Alias
	for index, v := range r {
		for name, a := range v.Aliases {
			routing := a.Routing
			if routing == "" {
				routing = a.IndexRouting
			}
			out = append(out, Alias{Name: name, Index: index, IsWrite: a.IsWrite, Filter: a.Filter, Routing: routing})
		}
	}
	slices.SortFunc(out, func(a, b Alias) int {
		return strings.Compare(a.Name+"\x00"+a.Index, b.Name+"\x00"+b.Index)
	})
	return out, nil
}

// DataStream is one data stream.
type DataStream struct {
	Name, Template string
	Indices        []string // backing indices
}

// DataStreams lists the data streams; a cluster without them gives none.
func DataStreams(ctx context.Context, d Doer) ([]DataStream, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_data_stream"})
	// without x-pack (OpenDistro, some cloud builds) the path falls through
	// to the index handler: invalid_index_name_exception
	var apiErr *APIError
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnsupported) ||
		errors.As(err, &apiErr) && apiErr.Status == 400 && apiErr.hasType("invalid_index_name_exception") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r struct {
		Streams []struct {
			Name     string `json:"name"`
			Template string `json:"template"`
			Indices  []struct {
				Name string `json:"index_name"`
			} `json:"indices"`
		} `json:"data_streams"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	var out []DataStream
	for _, s := range r.Streams {
		ds := DataStream{Name: s.Name, Template: s.Template}
		for _, i := range s.Indices {
			ds.Indices = append(ds.Indices, i.Name)
		}
		out = append(out, ds)
	}
	return out, nil
}

// RawMappings are the "mappings" objects of target's indices.
func RawMappings(ctx context.Context, d Doer, target string) (map[string]json.RawMessage, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: indexPath(target) + "/_mapping"})
	if err != nil {
		return nil, err
	}
	var r map[string]struct {
		Mappings json.RawMessage `json:"mappings"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(r))
	for k, v := range r {
		out[k] = v.Mappings
	}
	return out, nil
}

// Mappings are the flattened fields of target's indices.
func Mappings(ctx context.Context, d Doer, target string) (map[string][]esx.Field, error) {
	raw, err := RawMappings(ctx, d, target)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]esx.Field, len(raw))
	for k, m := range raw {
		if out[k], err = esx.FlattenMapping(m); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Setting is one index setting.
type Setting struct {
	Key, Value string
	Default    bool // not set on the index
	Dynamic    bool // can change on an open index
}

// Settings are an index's settings with the defaults, sorted by key; for
// an alias or pattern, those of its first index.
func Settings(ctx context.Context, d Doer, index string) ([]Setting, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: indexPath(index) + "/_settings?include_defaults=true&flat_settings=true"})
	if err != nil {
		return nil, err
	}
	var r map[string]struct {
		Settings map[string]json.RawMessage `json:"settings"`
		Defaults map[string]json.RawMessage `json:"defaults"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	names := slices.Sorted(maps.Keys(r))
	if len(names) == 0 {
		return nil, nil
	}
	v := r[names[0]]
	var out []Setting
	add := func(m map[string]json.RawMessage, def bool) {
		for k, raw := range m {
			text, null := esx.CellText(raw, true)
			if null {
				text = "null"
			}
			out = append(out, Setting{Key: k, Value: text, Default: def, Dynamic: esx.DynamicSetting(k)})
		}
	}
	add(v.Settings, false)
	add(v.Defaults, true)
	slices.SortFunc(out, func(a, b Setting) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

// IndexStats are one index's numbers (spec §5.1 Stats).
type IndexStats struct {
	Index                                    string
	Docs, Deleted, PriStoreBytes, StoreBytes int64
	Segments, Refreshes, Flushes, Merges     int64
	Queries, Indexed                         int64
}

// Stats are the numbers of target's indices, by name.
func Stats(ctx context.Context, d Doer, target string) ([]IndexStats, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: indexPath(target) + "/_stats"})
	if err != nil {
		return nil, err
	}
	type part struct {
		Docs  struct{ Count, Deleted int64 } `json:"docs"`
		Store struct {
			Size int64 `json:"size_in_bytes"`
		} `json:"store"`
		Segments struct{ Count int64 } `json:"segments"`
		Refresh  struct{ Total int64 } `json:"refresh"`
		Flush    struct{ Total int64 } `json:"flush"`
		Merges   struct{ Total int64 } `json:"merges"`
		Search   struct {
			Query int64 `json:"query_total"`
		} `json:"search"`
		Indexing struct {
			Index int64 `json:"index_total"`
		} `json:"indexing"`
	}
	var r struct {
		Indices map[string]struct {
			Primaries part `json:"primaries"`
			Total     part `json:"total"`
		} `json:"indices"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	var out []IndexStats
	for _, name := range slices.Sorted(maps.Keys(r.Indices)) {
		v := r.Indices[name]
		out = append(out, IndexStats{
			Index: name, Docs: v.Primaries.Docs.Count, Deleted: v.Primaries.Docs.Deleted,
			PriStoreBytes: v.Primaries.Store.Size, StoreBytes: v.Total.Store.Size, Segments: v.Total.Segments.Count,
			Refreshes: v.Total.Refresh.Total, Flushes: v.Total.Flush.Total, Merges: v.Total.Merges.Total,
			Queries: v.Total.Search.Query, Indexed: v.Total.Indexing.Index,
		})
	}
	return out, nil
}

// Health is _cluster/health.
type Health struct {
	Status                                                                                      string
	Nodes, DataNodes, ActivePrimary, Active, Relocating, Initializing, Unassigned, PendingTasks int
}

// ClusterHealth reads _cluster/health.
func ClusterHealth(ctx context.Context, d Doer) (Health, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_cluster/health"})
	if err != nil {
		return Health{}, err
	}
	var r struct {
		Status        string `json:"status"`
		Nodes         int    `json:"number_of_nodes"`
		DataNodes     int    `json:"number_of_data_nodes"`
		ActivePrimary int    `json:"active_primary_shards"`
		Active        int    `json:"active_shards"`
		Relocating    int    `json:"relocating_shards"`
		Initializing  int    `json:"initializing_shards"`
		Unassigned    int    `json:"unassigned_shards"`
		Pending       int    `json:"number_of_pending_tasks"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return Health{}, err
	}
	return Health{r.Status, r.Nodes, r.DataNodes, r.ActivePrimary, r.Active, r.Relocating, r.Initializing, r.Unassigned, r.Pending}, nil
}

// ClusterStats are _cluster/stats totals.
type ClusterStats struct {
	Indices, Shards  int
	Docs, StoreBytes int64
}

// GetClusterStats reads _cluster/stats.
func GetClusterStats(ctx context.Context, d Doer) (ClusterStats, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_cluster/stats"})
	if err != nil {
		return ClusterStats{}, err
	}
	var r struct {
		Indices struct {
			Count  int `json:"count"`
			Shards struct {
				Total int `json:"total"`
			} `json:"shards"`
			Docs struct {
				Count int64 `json:"count"`
			} `json:"docs"`
			Store struct {
				Size int64 `json:"size_in_bytes"`
			} `json:"store"`
		} `json:"indices"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return ClusterStats{}, err
	}
	return ClusterStats{r.Indices.Count, r.Indices.Shards.Total, r.Indices.Docs.Count, r.Indices.Store.Size}, nil
}

// rawHit is a hit or a GET _doc response.
type rawHit struct {
	Index       string              `json:"_index"`
	ID          string              `json:"_id"`
	Routing     string              `json:"_routing"`
	SeqNo       int64               `json:"_seq_no"`
	PrimaryTerm int64               `json:"_primary_term"`
	Version     int64               `json:"_version"`
	Score       *float64            `json:"_score"`
	Source      json.RawMessage     `json:"_source"`
	Highlight   map[string][]string `json:"highlight"`
	Sort        []json.RawMessage   `json:"sort"`
}

func (r rawHit) hit() esx.Hit {
	h := esx.Hit{
		Index: r.Index, ID: r.ID, Routing: r.Routing, SeqNo: r.SeqNo, PrimaryTerm: r.PrimaryTerm, Version: r.Version,
		Score: r.Score, Source: r.Source, Highlight: r.Highlight, Sort: r.Sort, SourceSize: len(r.Source),
	}
	if len(h.Source) > MaxSource {
		h.Source, h.Truncated = esx.TruncateSource(r.Source, MaxSource), true
	}
	return h
}

// GetDoc reads one document; a missing one is ErrNotFound.
func GetDoc(ctx context.Context, d Doer, index, id string) (esx.Hit, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: indexPath(index) + "/_doc/" + url.PathEscape(id)})
	if err != nil {
		return esx.Hit{}, err
	}
	var r rawHit
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return esx.Hit{}, err
	}
	return r.hit(), nil
}

// Count is how many documents of target match query.
func Count(ctx context.Context, d Doer, target string, query json.RawMessage) (int64, error) {
	body, _ := json.Marshal(map[string]json.RawMessage{"query": query})
	resp, err := do(ctx, d, Request{Method: "POST", Path: indexPath(target) + "/_count", Body: body})
	if err != nil {
		return 0, err
	}
	var r struct {
		Count int64 `json:"count"`
	}
	err = json.Unmarshal(resp.Body, &r)
	return r.Count, err
}

// Resolved is what an index expression names. Indices are the concrete
// indices, with those behind its aliases and data streams.
type Resolved struct{ Indices, Aliases, DataStreams []string }

// ResolveIndex expands an expression to concrete indices with
// _resolve/index, or _cat/indices where that is missing.
func ResolveIndex(ctx context.Context, d Doer, expr string) (Resolved, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_resolve/index/" + indexPath(expr) + "?expand_wildcards=open,closed"})
	if errors.Is(err, ErrUnsupported) {
		return resolveByCat(ctx, d, expr)
	}
	if err != nil {
		return Resolved{}, err
	}
	var r struct {
		Indices []struct {
			Name string `json:"name"`
		} `json:"indices"`
		Aliases []struct {
			Name    string   `json:"name"`
			Indices []string `json:"indices"`
		} `json:"aliases"`
		Streams []struct {
			Name    string   `json:"name"`
			Backing []string `json:"backing_indices"`
		} `json:"data_streams"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return Resolved{}, err
	}
	var out Resolved
	for _, i := range r.Indices {
		out.Indices = append(out.Indices, i.Name)
	}
	for _, a := range r.Aliases {
		out.Aliases = append(out.Aliases, a.Name)
		out.Indices = append(out.Indices, a.Indices...)
	}
	for _, s := range r.Streams {
		out.DataStreams = append(out.DataStreams, s.Name)
		out.Indices = append(out.Indices, s.Backing...)
	}
	slices.Sort(out.Indices)
	out.Indices = slices.Compact(out.Indices)
	return out, nil
}

func resolveByCat(ctx context.Context, d Doer, expr string) (Resolved, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_cat/indices/" + indexPath(expr) + "?format=json&h=index&expand_wildcards=open,closed"})
	if err != nil {
		return Resolved{}, err
	}
	var rows []struct {
		Index string `json:"index"`
	}
	if err := json.Unmarshal(resp.Body, &rows); err != nil {
		return Resolved{}, err
	}
	var out Resolved
	for _, r := range rows {
		out.Indices = append(out.Indices, r.Index)
	}
	slices.Sort(out.Indices)
	return out, nil
}

// SearchIDs finds the documents with _id id in target: an alias or
// pattern may hold several.
func SearchIDs(ctx context.Context, d Doer, target, id string) ([]esx.Hit, error) {
	body, _ := json.Marshal(map[string]any{
		"query": map[string]any{"ids": map[string]any{"values": []string{id}}},
		"size":  100, "seq_no_primary_term": true, "version": true,
	})
	resp, err := do(ctx, d, Request{Method: "POST", Path: indexPath(target) + "/_search", Body: body})
	if err != nil {
		return nil, err
	}
	var r struct {
		Hits struct {
			Hits []rawHit `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	out := make([]esx.Hit, len(r.Hits.Hits))
	for i, h := range r.Hits.Hits {
		out[i] = h.hit()
	}
	return out, nil
}
