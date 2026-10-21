package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/session"
)

// fakeES is an Elasticsearch cluster: indices with documents, aliases
// and data streams, answering the APIs the UI sends. routes, keyed
// "METHOD path" with the query string cut off, override any answer.
type fakeES struct {
	mu      sync.Mutex
	info    es.Info
	health  es.Health
	indices []*fakeIndex
	aliases []es.Alias
	streams []es.DataStream
	routes  map[string]func(r es.Request) es.Response
	reqs    []es.Request
	seq     int64
	cursors map[string]*fakeCursor // open PITs and scrolls by id
}

type fakeIndex struct {
	info     es.IndexInfo
	mapping  string // the "mappings" object
	settings map[string]string
	docs     []*fakeDoc
}

type fakeDoc struct {
	id, src, routing string
	seq, term        int64
}

// fakeCursor is an open PIT or scroll: its hits and how far it got.
type fakeCursor struct {
	hits []fakeHit
	next int
}

type fakeHit struct {
	index string
	doc   *fakeDoc
}

func newFakeES() *fakeES {
	return &fakeES{
		info:    es.Info{ClusterName: "dev", Version: "8.11.0", Distribution: "elasticsearch", Major: 8, Minor: 11},
		health:  es.Health{Status: "green", Nodes: 1, DataNodes: 1},
		routes:  map[string]func(es.Request) es.Response{},
		cursors: map[string]*fakeCursor{},
	}
}

// addIndex adds an open green index with mapping (a "mappings" object;
// empty is {}) and n documents d0…d(n-1) whose _source is {"n":i}.
func (f *fakeES) addIndex(name, mapping string, n int) *fakeIndex {
	f.mu.Lock()
	defer f.mu.Unlock()
	if mapping == "" {
		mapping = "{}"
	}
	ix := &fakeIndex{info: es.IndexInfo{Name: name, Health: "green", Status: "open", Primaries: 1, Replicas: 1, System: strings.HasPrefix(name, ".")}, mapping: mapping}
	for i := range n {
		ix.docs = append(ix.docs, &fakeDoc{id: fmt.Sprintf("d%d", i), src: fmt.Sprintf(`{"n":%d}`, i), seq: int64(i), term: 1})
	}
	ix.info.Docs = int64(n)
	f.indices = append(f.indices, ix)
	return ix
}

// addDoc adds a document to an index.
func (f *fakeES) addDoc(index, id, src string) *fakeDoc {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := &fakeDoc{id: id, src: src, seq: 1, term: 1}
	for _, ix := range f.indices {
		if ix.info.Name == index {
			ix.docs = append(ix.docs, d)
			ix.info.Docs = int64(len(ix.docs))
		}
	}
	return d
}

func (f *fakeES) index(name string) *fakeIndex {
	for _, ix := range f.indices {
		if ix.info.Name == name {
			return ix
		}
	}
	return nil
}

// resolve expands an index expression to indices.
func (f *fakeES) resolve(expr string) []*fakeIndex {
	var out []*fakeIndex
	add := func(ix *fakeIndex) {
		if ix != nil && !slices.Contains(out, ix) {
			out = append(out, ix)
		}
	}
	for part := range strings.SplitSeq(expr, ",") {
		for _, a := range f.aliases {
			if a.Name == part {
				add(f.index(a.Index))
			}
		}
		for _, s := range f.streams {
			if s.Name == part {
				for _, i := range s.Indices {
					add(f.index(i))
				}
			}
		}
		for _, ix := range f.indices {
			if ok, _ := path.Match(part, ix.info.Name); ok || part == "_all" {
				add(ix)
			}
		}
	}
	return out
}

// sent are the requests so far as "METHOD path" (with the query string).
func (f *fakeES) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

// lastBody is the body of the last request to "METHOD path" (query cut).
func (f *fakeES) lastBody(methodPath string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.reqs) - 1; i >= 0; i-- {
		r := f.reqs[i]
		p, _, _ := strings.Cut(r.Path, "?")
		if r.Method+" "+p == methodPath {
			return string(r.Body)
		}
	}
	return ""
}

func (f *fakeES) Do(_ context.Context, r es.Request) (es.Response, error) {
	f.mu.Lock()
	r.Path = strings.TrimLeft(r.Path, "/")
	f.reqs = append(f.reqs, r)
	p, _, _ := strings.Cut(r.Path, "?")
	h := f.routes[r.Method+" "+p]
	f.mu.Unlock()
	if h != nil {
		return h(r), nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.answer(r), nil
}

func jsonResp(v any) es.Response {
	b, _ := json.Marshal(v)
	return es.Response{Status: 200, Body: b}
}

func errResp(status int, typ, reason string) es.Response {
	return es.Response{Status: status, Body: []byte(fmt.Sprintf(`{"error":{"root_cause":[{"type":%q,"reason":%q}],"type":%q,"reason":%q},"status":%d}`, typ, reason, typ, reason, status))}
}

func (f *fakeES) answer(r es.Request) es.Response {
	p, query, _ := strings.Cut(r.Path, "?")
	segs := strings.Split(p, "/")
	str := func(n int64) string { return strconv.FormatInt(n, 10) }
	switch {
	case r.Method == "GET" && p == "":
		return jsonResp(map[string]any{"cluster_name": f.info.ClusterName, "version": map[string]string{"number": f.info.Version, "distribution": f.info.Distribution}})
	case r.Method == "GET" && p == "_cat/indices":
		var rows []map[string]any
		for _, ix := range f.indices {
			i := ix.info
			rows = append(rows, map[string]any{
				"health": i.Health, "status": i.Status, "index": i.Name, "uuid": i.UUID, "pri": str(int64(i.Primaries)), "rep": str(int64(i.Replicas)),
				"docs.count": str(i.Docs), "docs.deleted": str(i.Deleted), "store.size": str(i.StoreBytes), "pri.store.size": str(i.PriStoreBytes),
				"creation.date": str(i.Created.UnixMilli()),
			})
		}
		return jsonResp(rows)
	case r.Method == "GET" && (p == "_alias" || len(segs) == 2 && segs[1] == "_alias"):
		out := map[string]map[string]map[string]any{}
		names := map[string]bool{}
		if len(segs) == 2 {
			for _, ix := range f.resolve(segs[0]) {
				names[ix.info.Name] = true
			}
		}
		for _, a := range f.aliases {
			if len(segs) == 2 && !names[a.Index] {
				continue
			}
			if out[a.Index] == nil {
				out[a.Index] = map[string]map[string]any{"aliases": {}}
			}
			v := map[string]any{"is_write_index": a.IsWrite}
			if a.Routing != "" {
				v["routing"] = a.Routing
			}
			out[a.Index]["aliases"][a.Name] = v
		}
		return jsonResp(out)
	case r.Method == "GET" && p == "_data_stream":
		var ds []map[string]any
		for _, s := range f.streams {
			var idx []map[string]string
			for _, i := range s.Indices {
				idx = append(idx, map[string]string{"index_name": i})
			}
			ds = append(ds, map[string]any{"name": s.Name, "template": s.Template, "indices": idx})
		}
		return jsonResp(map[string]any{"data_streams": ds})
	case r.Method == "GET" && p == "_cluster/health":
		h := f.health
		return jsonResp(map[string]any{"status": h.Status, "number_of_nodes": h.Nodes, "number_of_data_nodes": h.DataNodes,
			"active_primary_shards": h.ActivePrimary, "active_shards": h.Active, "relocating_shards": h.Relocating,
			"initializing_shards": h.Initializing, "unassigned_shards": h.Unassigned, "number_of_pending_tasks": h.PendingTasks})
	case r.Method == "GET" && p == "_cluster/stats":
		var docs int64
		for _, ix := range f.indices {
			docs += ix.info.Docs
		}
		return jsonResp(map[string]any{"indices": map[string]any{"count": len(f.indices), "shards": map[string]int{"total": len(f.indices)}, "docs": map[string]int64{"count": docs}, "store": map[string]int64{"size_in_bytes": 0}}})
	case len(segs) == 2 && segs[1] == "_mapping" && r.Method == "GET":
		out := map[string]any{}
		for _, ix := range f.resolve(segs[0]) {
			out[ix.info.Name] = map[string]json.RawMessage{"mappings": json.RawMessage(ix.mapping)}
		}
		if len(out) == 0 {
			return errResp(404, "index_not_found_exception", "no such index ["+segs[0]+"]")
		}
		return jsonResp(out)
	case len(segs) == 2 && segs[1] == "_settings" && r.Method == "GET":
		out := map[string]any{}
		for _, ix := range f.resolve(segs[0]) {
			set := map[string]string{"index.number_of_shards": str(int64(ix.info.Primaries)), "index.number_of_replicas": str(int64(ix.info.Replicas))}
			for k, v := range ix.settings {
				set[k] = v
			}
			out[ix.info.Name] = map[string]any{"settings": set, "defaults": map[string]string{"index.refresh_interval": "1s", "index.max_result_window": "10000"}}
		}
		return jsonResp(out)
	case len(segs) == 2 && segs[1] == "_stats" && r.Method == "GET":
		out := map[string]any{}
		for _, ix := range f.resolve(segs[0]) {
			out[ix.info.Name] = map[string]any{"primaries": map[string]any{"docs": map[string]int64{"count": ix.info.Docs}}, "total": map[string]any{}}
		}
		return jsonResp(map[string]any{"indices": out})
	case len(segs) == 3 && segs[0] == "_resolve":
		var names []map[string]string
		for _, ix := range f.resolve(segs[2]) {
			names = append(names, map[string]string{"name": ix.info.Name})
		}
		return jsonResp(map[string]any{"indices": names})
	case len(segs) == 2 && segs[1] == "_count":
		return jsonResp(map[string]int64{"count": int64(len(f.matching(segs[0], r.Body)))})
	case len(segs) == 2 && segs[1] == "_pit" && r.Method == "POST":
		id := fmt.Sprintf("pit-%d", len(f.cursors)+1)
		f.cursors[id] = &fakeCursor{hits: f.matching(segs[0], nil)}
		return jsonResp(map[string]string{"id": id})
	case p == "_pit" && r.Method == "DELETE", p == "_search/scroll" && r.Method == "DELETE":
		var b struct {
			ID       string `json:"id"`
			ScrollID string `json:"scroll_id"`
		}
		json.Unmarshal(r.Body, &b)
		delete(f.cursors, b.ID+b.ScrollID)
		return jsonResp(map[string]bool{"succeeded": true})
	case p == "_search" && r.Method == "POST":
		var b struct {
			Size int `json:"size"`
			Pit  struct{ ID string } `json:"pit"`
		}
		json.Unmarshal(r.Body, &b)
		c := f.cursors[b.Pit.ID]
		if c == nil {
			return errResp(404, "search_context_missing_exception", "No search context found for id ["+b.Pit.ID+"]")
		}
		if c.next == 0 {
			c.hits = f.filterHits(c.hits, r.Body)
		}
		return f.page(c, b.Size, b.Pit.ID, "pit_id")
	case len(segs) == 2 && segs[1] == "_search" && r.Method == "POST" && strings.Contains(query, "scroll="):
		var b struct{ Size int `json:"size"` }
		json.Unmarshal(r.Body, &b)
		id := fmt.Sprintf("scroll-%d", len(f.cursors)+1)
		c := &fakeCursor{hits: f.matching(segs[0], r.Body)}
		f.cursors[id] = c
		return f.page(c, b.Size, id, "_scroll_id")
	case p == "_search/scroll" && r.Method == "POST":
		var b struct{ ScrollID string `json:"scroll_id"` }
		json.Unmarshal(r.Body, &b)
		c := f.cursors[b.ScrollID]
		if c == nil {
			return errResp(404, "search_context_missing_exception", "No search context found")
		}
		return f.page(c, 100, b.ScrollID, "_scroll_id")
	case len(segs) == 3 && segs[1] == "_doc" && r.Method == "GET":
		id, _ := url.PathUnescape(segs[2])
		for _, h := range f.matching(segs[0], nil) {
			if h.doc.id == id {
				return jsonResp(f.hitJSON(h, nil))
			}
		}
		return es.Response{Status: 404, Body: []byte(`{"found":false}`)}
	case len(segs) == 3 && (segs[1] == "_doc" || segs[1] == "_create") && (r.Method == "PUT" || r.Method == "POST"),
		len(segs) == 2 && segs[1] == "_doc" && r.Method == "POST":
		ix := f.index(segs[0])
		if ix == nil {
			return errResp(404, "index_not_found_exception", "no such index")
		}
		id := fmt.Sprintf("gen-%d", len(ix.docs))
		if len(segs) == 3 {
			id, _ = url.PathUnescape(segs[2])
		}
		f.seq++
		for _, d := range ix.docs {
			if d.id == id {
				d.src, d.seq = string(r.Body), d.seq+100
				return jsonResp(map[string]any{"_id": id, "_seq_no": d.seq, "_primary_term": d.term, "result": "updated"})
			}
		}
		ix.docs = append(ix.docs, &fakeDoc{id: id, src: string(r.Body), seq: f.seq, term: 1})
		ix.info.Docs++
		return es.Response{Status: 201, Body: []byte(fmt.Sprintf(`{"_id":%q,"_seq_no":%d,"_primary_term":1,"result":"created"}`, id, f.seq))}
	case len(segs) == 3 && segs[1] == "_doc" && r.Method == "DELETE":
		ix := f.index(segs[0])
		if ix != nil {
			id, _ := url.PathUnescape(segs[2])
			ix.docs = slices.DeleteFunc(ix.docs, func(d *fakeDoc) bool { return d.id == id })
			ix.info.Docs = int64(len(ix.docs))
		}
		return jsonResp(map[string]string{"result": "deleted"})
	case r.Method != "GET":
		return jsonResp(map[string]any{"acknowledged": true})
	}
	return errResp(404, "fake", "no route "+r.Method+" "+p)
}

// matching are the hits of expr in index order, filtered by an ids query
// in body.
func (f *fakeES) matching(expr string, body []byte) []fakeHit {
	var out []fakeHit
	for _, ix := range f.resolve(expr) {
		for _, d := range ix.docs {
			out = append(out, fakeHit{index: ix.info.Name, doc: d})
		}
	}
	return f.filterHits(out, body)
}

func (f *fakeES) filterHits(hits []fakeHit, body []byte) []fakeHit {
	var b struct {
		Query struct {
			IDs *struct{ Values []string } `json:"ids"`
		} `json:"query"`
	}
	if json.Unmarshal(body, &b) != nil || b.Query.IDs == nil {
		return hits
	}
	return slices.DeleteFunc(slices.Clone(hits), func(h fakeHit) bool { return !slices.Contains(b.Query.IDs.Values, h.doc.id) })
}

func (f *fakeES) hitJSON(h fakeHit, sort []int) map[string]any {
	m := map[string]any{"_index": h.index, "_id": h.doc.id, "_seq_no": h.doc.seq, "_primary_term": h.doc.term, "_version": 1, "found": true, "_source": json.RawMessage(h.doc.src)}
	if h.doc.routing != "" {
		m["_routing"] = h.doc.routing
	}
	if sort != nil {
		m["sort"] = sort
	}
	return m
}

func (f *fakeES) page(c *fakeCursor, size int, id, idKey string) es.Response {
	if size <= 0 {
		size = 100
	}
	end := min(c.next+size, len(c.hits))
	hits := []map[string]any{}
	for i := c.next; i < end; i++ {
		hits = append(hits, f.hitJSON(c.hits[i], []int{i}))
	}
	total := len(c.hits)
	c.next = end
	return jsonResp(map[string]any{idKey: id, "hits": map[string]any{"total": map[string]any{"value": total, "relation": "eq"}, "hits": hits}})
}

// openCursors is how many PITs and scrolls are open.
func (f *fakeES) openCursors() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cursors)
}

// guardedFakeES refuses writes on a read-only connection like the session.
type guardedFakeES struct {
	f  *fakeES
	ro bool
}

func (g guardedFakeES) Do(ctx context.Context, r es.Request) (es.Response, error) {
	if g.ro && esx.Classify(r.Method, r.Path, r.Body).Level != esx.LevelRead {
		return es.Response{}, session.ErrReadOnly
	}
	return g.f.Do(ctx, r)
}

// addES adds an elasticsearch connection backed by f.es (made on first use).
func (f *fakeBackend) addES(cfg config.Connection) *fakeES {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cfg.Driver == "" {
		cfg.Driver = "elasticsearch"
	}
	if cfg.Env == "" {
		cfg.Env = config.EnvDev
	}
	if f.es == nil {
		f.es = newFakeES()
	}
	f.conns = append(f.conns, cfg)
	return f.es
}

func (f *fakeBackend) ES(_ context.Context, name string) (es.Doer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.esOpens = append(f.esOpens, name)
	if f.connErr != nil {
		return nil, f.connErr
	}
	if f.needPassword {
		return nil, session.ErrPasswordRequired
	}
	for _, c := range f.conns {
		if c.Name == name && c.Driver == "elasticsearch" && f.es != nil {
			return guardedFakeES{f: f.es, ro: c.IsReadOnly()}, nil
		}
	}
	return nil, fmt.Errorf("connection %q is not an elasticsearch connection", name)
}

func (f *fakeBackend) ESInfo(name string) (es.Info, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.es == nil {
		return es.Info{}, false
	}
	return f.es.info, true
}
