package es

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/shepherd-yun/datavim/internal/esx"
)

// SearchSpec is what a Documents view searches.
type SearchSpec struct {
	Target    string          // an index, alias, pattern or data stream
	Query     json.RawMessage // nil is match_all
	Sort      []SortField     // empty: index order
	PageSize  int
	Highlight bool
	// ExactTotal counts every hit; otherwise counting stops at 10,000.
	ExactTotal bool
}

// SortField is one sort key; "_score" is one too.
type SortField struct {
	Field string
	Desc  bool
}

// Page is one page of hits.
type Page struct {
	Hits       []esx.Hit
	Total      int64 // from the first page
	TotalExact bool
	Done       bool // no more pages
}

// Cursor pages through a search without gaps or repeats, past
// max_result_window, holding a PIT or scroll context on the server.
type Cursor interface {
	// Next is the next page; the first call gives the first page.
	Next(ctx context.Context) (Page, error)
	// Close releases the server context; calling it again does nothing.
	Close(ctx context.Context) error
}

const keepAlive = "5m"

// OpenCursor makes a cursor for spec; nothing is sent before Next. ES
// 7.12 and later page with a PIT, search_after and _shard_doc; ES 7.10 /
// 7.11 and OpenSearch scroll. The cursor sends read requests only.
func OpenCursor(info Info, d Doer, spec SearchSpec) Cursor {
	if spec.PageSize <= 0 {
		spec.PageSize = 100
	}
	if len(spec.Query) == 0 {
		spec.Query = json.RawMessage(`{"match_all":{}}`)
	}
	return &cursor{d: readOnlyDoer{d}, spec: spec, pit: !info.OpenSearch() && info.AtLeast(7, 12)}
}

// readOnlyDoer refuses every request esx.Classify does not grade read.
type readOnlyDoer struct{ d Doer }

func (r readOnlyDoer) Do(ctx context.Context, req Request) (Response, error) {
	if c := esx.Classify(req.Method, req.Path, req.Body); c.Level != esx.LevelRead {
		return Response{}, fmt.Errorf("elasticsearch cursor: refused %s %s (%s)", req.Method, req.Path, c.Level)
	}
	return r.d.Do(ctx, req)
}

type cursor struct {
	mu          sync.Mutex
	d           Doer
	spec        SearchSpec
	pit         bool
	id          string // the PIT or scroll id
	after       []json.RawMessage
	started     bool
	done        bool
	noHighlight bool
	total       int64
	exact       bool
}

func (c *cursor) sortBody() []any {
	var out []any
	for _, s := range c.spec.Sort {
		order := "asc"
		if s.Desc {
			order = "desc"
		}
		out = append(out, map[string]any{s.Field: map[string]string{"order": order}})
	}
	switch {
	case c.pit:
		out = append(out, map[string]string{"_shard_doc": "asc"})
	case len(out) == 0:
		out = append(out, "_doc")
	}
	return out
}

func (c *cursor) searchBody(first bool) []byte {
	b := map[string]any{"size": c.spec.PageSize, "query": c.spec.Query, "sort": c.sortBody(), "seq_no_primary_term": true, "version": true}
	switch {
	case !first:
		b["track_total_hits"] = false
	case c.spec.ExactTotal:
		b["track_total_hits"] = true
	default:
		b["track_total_hits"] = 10000
	}
	if c.spec.Highlight && !c.noHighlight {
		b["highlight"] = map[string]any{
			"fields": map[string]any{"*": map[string]any{}}, "require_field_match": false,
			"number_of_fragments": 1, "fragment_size": 100,
		}
	}
	if c.pit {
		b["pit"] = map[string]string{"id": c.id, "keep_alive": keepAlive}
		if c.after != nil {
			b["search_after"] = c.after
		}
	}
	out, _ := json.Marshal(b)
	return out
}

func (c *cursor) Next(ctx context.Context) (Page, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return Page{Total: c.total, TotalExact: c.exact, Done: true}, nil
	}
	first := !c.started
	if first && c.pit {
		resp, err := do(ctx, c.d, Request{Method: "POST", Path: indexPath(c.spec.Target) + "/_pit?keep_alive=" + keepAlive})
		if err != nil {
			return Page{}, err
		}
		var r struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(resp.Body, &r); err != nil {
			return Page{}, err
		}
		c.id = r.ID
	}
	c.started = true
	resp, err := c.search(ctx, first)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == 400 && !errors.Is(err, ErrCursorExpired) && c.spec.Highlight && !c.noHighlight {
		c.noHighlight = true // some field types do not highlight: search again without
		resp, err = c.search(ctx, first)
	}
	if err != nil {
		return Page{}, err
	}
	var r struct {
		ScrollID string `json:"_scroll_id"`
		PitID    string `json:"pit_id"`
		Hits     struct {
			Total *struct {
				Value    int64  `json:"value"`
				Relation string `json:"relation"`
			} `json:"total"`
			Hits []rawHit `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return Page{}, err
	}
	if id := r.ScrollID + r.PitID; id != "" {
		c.id = id
	}
	if first && r.Hits.Total != nil {
		c.total, c.exact = r.Hits.Total.Value, r.Hits.Total.Relation == "eq"
	}
	p := Page{Total: c.total, TotalExact: c.exact}
	for _, h := range r.Hits.Hits {
		p.Hits = append(p.Hits, h.hit())
	}
	if n := len(r.Hits.Hits); n > 0 {
		c.after = r.Hits.Hits[n-1].Sort
	}
	c.done = len(p.Hits) < c.spec.PageSize
	p.Done = c.done
	return p, nil
}

func (c *cursor) search(ctx context.Context, first bool) (Response, error) {
	switch {
	case c.pit:
		return do(ctx, c.d, Request{Method: "POST", Path: "_search", Body: c.searchBody(first)})
	case first:
		return do(ctx, c.d, Request{Method: "POST", Path: indexPath(c.spec.Target) + "/_search?scroll=" + keepAlive, Body: c.searchBody(true)})
	}
	b, _ := json.Marshal(map[string]string{"scroll": keepAlive, "scroll_id": c.id})
	return do(ctx, c.d, Request{Method: "POST", Path: "_search/scroll", Body: b})
}

func (c *cursor) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.id
	c.id, c.done = "", true
	if id == "" {
		return nil
	}
	var r Request
	if c.pit {
		b, _ := json.Marshal(map[string]string{"id": id})
		r = Request{Method: "DELETE", Path: "_pit", Body: b}
	} else {
		b, _ := json.Marshal(map[string]string{"scroll_id": id})
		r = Request{Method: "DELETE", Path: "_search/scroll", Body: b}
	}
	if _, err := do(ctx, c.d, r); err != nil && !errors.Is(err, ErrNotFound) {
		slog.Info("elasticsearch: releasing a search context failed", "err", err)
		return err
	}
	return nil
}
