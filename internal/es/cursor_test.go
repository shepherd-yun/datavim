package es

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/esx"
)

// hitsPage is a search response with n hits numbered from first; sort
// values are the hit numbers.
func hitsPage(first, n int, total int, extra string) Response {
	var hits []string
	for i := first; i < first+n; i++ {
		hits = append(hits, fmt.Sprintf(`{"_index":"orders","_id":"d%d","_seq_no":%d,"_primary_term":1,"_source":{"n":%d},"sort":[%d,%d]}`, i, i, i, i, i))
	}
	return ok(fmt.Sprintf(`{%s"hits":{"total":{"value":%d,"relation":"eq"},"hits":[%s]}}`, extra, total, strings.Join(hits, ",")))
}

func body(t *testing.T, r Request) map[string]any {
	t.Helper()
	var m map[string]any
	if len(r.Body) > 0 {
		if err := json.Unmarshal(r.Body, &m); err != nil {
			t.Fatalf("%s %s body %s: %v", r.Method, r.Path, r.Body, err)
		}
	}
	return m
}

func info(dist, version string) Info {
	var i Info
	i.Distribution, i.Version = dist, version
	_, _ = fmt.Sscanf(version, "%d.%d", &i.Major, &i.Minor)
	return i
}

func TestCursorPIT(t *testing.T) {
	page := 0
	d := &fakeDoer{fn: func(r Request) (Response, error) {
		switch r.Method + " " + r.Path {
		case "POST orders/_pit?keep_alive=5m":
			return ok(`{"id":"pit1"}`), nil
		case "POST _search":
			page++
			if page == 1 {
				return hitsPage(0, 2, 3, `"pit_id":"pit2",`), nil
			}
			return hitsPage(2, 1, 3, `"pit_id":"pit3",`), nil
		case "DELETE _pit":
			return ok(`{"succeeded":true}`), nil
		}
		return Response{Status: 500}, nil
	}}
	c := OpenCursor(info("elasticsearch", "8.11.0"), d, SearchSpec{Target: "orders", Query: json.RawMessage(`{"match_all":{}}`), PageSize: 2, ExactTotal: true})
	p, err := c.Next(ctx)
	if err != nil || len(p.Hits) != 2 || p.Total != 3 || !p.TotalExact || p.Done {
		t.Fatalf("page 1 = %+v %v", p, err)
	}
	p, err = c.Next(ctx)
	if err != nil || len(p.Hits) != 1 || !p.Done || p.Hits[0].ID != "d2" {
		t.Fatalf("page 2 = %+v %v", p, err)
	}
	reqs := d.requests()
	b1 := body(t, reqs[1])
	if b1["pit"].(map[string]any)["id"] != "pit1" || b1["track_total_hits"] != true {
		t.Errorf("first search = %s", reqs[1].Body)
	}
	if s := fmt.Sprint(b1["sort"]); !strings.Contains(s, "_shard_doc") {
		t.Errorf("sort = %s", s)
	}
	b2 := body(t, reqs[2])
	if b2["pit"].(map[string]any)["id"] != "pit2" || fmt.Sprint(b2["search_after"]) != "[1 1]" {
		t.Errorf("second search = %s", reqs[2].Body)
	}
	_ = c.Close(ctx)
	last := d.requests()[len(d.requests())-1]
	if last.Method != "DELETE" || last.Path != "_pit" || body(t, last)["id"] != "pit3" {
		t.Errorf("close = %s %s %s", last.Method, last.Path, last.Body)
	}
}

func TestCursorSort(t *testing.T) {
	d := &fakeDoer{fn: func(r Request) (Response, error) {
		if strings.HasSuffix(r.Path, "_pit?keep_alive=5m") {
			return ok(`{"id":"p"}`), nil
		}
		return hitsPage(0, 0, 0, ""), nil
	}}
	c := OpenCursor(info("elasticsearch", "8.11.0"), d, SearchSpec{Target: "orders", PageSize: 10, Sort: []SortField{{Field: "@timestamp", Desc: true}, {Field: "_score"}}})
	_, _ = c.Next(ctx)
	b := body(t, d.requests()[1])
	if got := fmt.Sprint(b["sort"]); got != "[map[@timestamp:map[order:desc]] map[_score:map[order:asc]] map[_shard_doc:asc]]" {
		t.Errorf("sort = %s", got)
	}
	if b["track_total_hits"] != float64(10000) {
		t.Errorf("track_total_hits = %v", b["track_total_hits"])
	}
}

func scrollDoer(pages ...Response) *fakeDoer {
	n := 0
	return &fakeDoer{fn: func(r Request) (Response, error) {
		switch {
		case r.Method == "DELETE":
			return ok(`{"succeeded":true}`), nil
		case n < len(pages):
			n++
			return pages[n-1], nil
		}
		return Response{Status: 500}, nil
	}}
}

func TestCursorScroll710(t *testing.T) {
	d := scrollDoer(hitsPage(0, 2, 3, `"_scroll_id":"s1",`), hitsPage(2, 1, 3, `"_scroll_id":"s2",`))
	c := OpenCursor(info("elasticsearch", "7.10.2"), d, SearchSpec{Target: "orders", PageSize: 2})
	p, err := c.Next(ctx)
	if err != nil || len(p.Hits) != 2 {
		t.Fatal(p, err)
	}
	p, err = c.Next(ctx)
	if err != nil || len(p.Hits) != 1 || !p.Done {
		t.Fatal(p, err)
	}
	reqs := d.requests()
	if reqs[0].Method != "POST" || reqs[0].Path != "orders/_search?scroll=5m" || fmt.Sprint(body(t, reqs[0])["sort"]) != "[_doc]" {
		t.Errorf("first = %s %s %s", reqs[0].Method, reqs[0].Path, reqs[0].Body)
	}
	if reqs[1].Path != "_search/scroll" || body(t, reqs[1])["scroll_id"] != "s1" || body(t, reqs[1])["scroll"] != "5m" {
		t.Errorf("second = %s %s", reqs[1].Path, reqs[1].Body)
	}
	_ = c.Close(ctx)
	last := d.requests()[len(d.requests())-1]
	if last.Method != "DELETE" || last.Path != "_search/scroll" || body(t, last)["scroll_id"] != "s2" {
		t.Errorf("close = %s %s %s", last.Method, last.Path, last.Body)
	}
}

func TestCursorOpenSearch(t *testing.T) {
	for _, v := range []string{"2.11.0", "2.3.0"} {
		d := scrollDoer(hitsPage(0, 1, 1, `"_scroll_id":"s1",`))
		c := OpenCursor(info("opensearch", v), d, SearchSpec{Target: "orders", PageSize: 2})
		if _, err := c.Next(ctx); err != nil {
			t.Fatal(v, err)
		}
		if p := d.requests()[0].Path; p != "orders/_search?scroll=5m" {
			t.Errorf("%s: first request %s", v, p)
		}
	}
}

func TestCursorExpired(t *testing.T) {
	expired := Response{Status: 404, Body: []byte(`{"error":{"root_cause":[{"type":"search_context_missing_exception","reason":"No search context found for id [1]"}],"type":"search_phase_execution_exception","reason":"all shards failed"},"status":404}`)}
	d := scrollDoer(hitsPage(0, 2, 5, `"_scroll_id":"s1",`), expired)
	c := OpenCursor(info("elasticsearch", "7.10.2"), d, SearchSpec{Target: "orders", PageSize: 2})
	_, _ = c.Next(ctx)
	if _, err := c.Next(ctx); !errors.Is(err, ErrCursorExpired) {
		t.Errorf("err = %v", err)
	}
}

func TestCursorReadOnly(t *testing.T) {
	for _, v := range []Info{info("elasticsearch", "8.11.0"), info("elasticsearch", "7.10.2"), info("opensearch", "2.11.0")} {
		d := &fakeDoer{fn: func(r Request) (Response, error) {
			if strings.Contains(r.Path, "_pit") && r.Method == "POST" {
				return ok(`{"id":"p"}`), nil
			}
			return hitsPage(0, 2, 4, `"_scroll_id":"s","pit_id":"p",`), nil
		}}
		c := OpenCursor(v, d, SearchSpec{Target: "orders", PageSize: 2, Highlight: true})
		_, _ = c.Next(ctx)
		_, _ = c.Next(ctx)
		_ = c.Close(ctx)
		for _, r := range d.requests() {
			if cl := esx.Classify(r.Method, r.Path, r.Body); cl.Level != esx.LevelRead {
				t.Errorf("%s: %s %s is %v", v.Version, r.Method, r.Path, cl.Level)
			}
		}
	}
	// a non-read request through the cursor's Doer is refused
	d := &fakeDoer{fn: func(Request) (Response, error) { return ok(`{}`), nil }}
	if _, err := (readOnlyDoer{d}).Do(ctx, Request{Method: "DELETE", Path: "orders"}); err == nil || len(d.requests()) != 0 {
		t.Errorf("err %v, sent %d", err, len(d.requests()))
	}
}

func TestCursorHighlightRetry(t *testing.T) {
	n := 0
	d := &fakeDoer{fn: func(r Request) (Response, error) {
		n++
		if strings.Contains(string(r.Body), "highlight") {
			return Response{Status: 400, Body: []byte(`{"error":{"type":"illegal_argument_exception","reason":"bad highlight"},"status":400}`)}, nil
		}
		return hitsPage(0, 1, 1, `"_scroll_id":"s",`), nil
	}}
	c := OpenCursor(info("elasticsearch", "7.10.2"), d, SearchSpec{Target: "orders", PageSize: 2, Highlight: true, Query: json.RawMessage(`{"match":{"a":"x"}}`)})
	p, err := c.Next(ctx)
	if err != nil || len(p.Hits) != 1 || n != 2 {
		t.Errorf("page %+v err %v requests %d", p, err, n)
	}
	b := body(t, d.requests()[0])["highlight"].(map[string]any)
	if b["require_field_match"] != false || b["number_of_fragments"] != float64(1) || b["fragment_size"] != float64(100) {
		t.Errorf("highlight = %v", b)
	}
}

func TestCursorClosesTwice(t *testing.T) {
	d := scrollDoer(hitsPage(0, 2, 5, `"_scroll_id":"s1",`))
	c := OpenCursor(info("elasticsearch", "7.10.2"), d, SearchSpec{Target: "orders", PageSize: 2})
	_, _ = c.Next(ctx)
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	dels := 0
	for _, r := range d.requests() {
		if r.Method == "DELETE" {
			dels++
		}
	}
	if dels != 1 {
		t.Errorf("%d deletes", dels)
	}
	// closing a cursor that never opened sends nothing
	d = scrollDoer()
	_ = OpenCursor(info("elasticsearch", "7.10.2"), d, SearchSpec{Target: "orders"}).Close(ctx)
	if len(d.requests()) != 0 {
		t.Errorf("sent %v", d.requests())
	}
}
