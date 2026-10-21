//go:build integration

package es_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tces "github.com/testcontainers/testcontainers-go/modules/elasticsearch"
	tcos "github.com/testcontainers/testcontainers-go/modules/opensearch"

	"github.com/shepherd-yun/datavim/internal/es"
)

type cluster struct {
	name string
	cfg  es.Config
	core bool // OpenSearch: the core subtests only
}

func startES(t *testing.T, img string, secure bool) cluster {
	t.Helper()
	ctx := context.Background()
	var opts []testcontainers.ContainerCustomizer
	if secure {
		opts = append(opts, tces.WithPassword("s3cret-pw"))
	}
	ctr, err := tces.Run(ctx, img, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	cfg := es.Config{URLs: []string{ctr.Settings.Address}}
	if ctr.Settings.Password != "" {
		cfg.User, cfg.Password = ctr.Settings.Username, ctr.Settings.Password
	}
	if ctr.Settings.CACert != nil {
		ca := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(ca, ctr.Settings.CACert, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.TLSCA = ca
	}
	return cluster{name: img, cfg: cfg}
}

func startOS(t *testing.T, img string) cluster {
	t.Helper()
	ctx := context.Background()
	ctr, err := tcos.Run(ctx, img)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	addr, err := ctr.Address(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return cluster{name: img, cfg: es.Config{URLs: []string{addr}}, core: true}
}

func raw(t *testing.T, c *es.Client, method, path, body string) es.Response {
	t.Helper()
	r := es.Request{Method: method, Path: path, NDJSON: strings.HasSuffix(strings.Split(path, "?")[0], "_bulk")}
	if body != "" {
		r.Body = []byte(body)
	}
	resp, err := c.Do(context.Background(), r)
	if err == nil {
		err = es.CheckStatus(resp)
	}
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func load(t *testing.T, c *es.Client, index string, n int) {
	t.Helper()
	for start := 0; start < n; start += 5000 {
		var b strings.Builder
		for i := start; i < min(start+5000, n); i++ {
			fmt.Fprintf(&b, "{\"index\":{\"_index\":%q,\"_id\":\"d%d\"}}\n{\"n\":%d,\"s\":\"v%d\"}\n", index, i, i, i%7)
		}
		resp := raw(t, c, "POST", "_bulk", b.String())
		if strings.Contains(string(resp.Body[:min(len(resp.Body), 200)]), `"errors":true`) {
			t.Fatalf("bulk: %.500s", resp.Body)
		}
	}
	raw(t, c, "POST", index+"/_refresh", "")
}

func TestIntegration(t *testing.T) {
	var clusters []cluster
	clusters = append(clusters, startES(t, "docker.elastic.co/elasticsearch/elasticsearch:7.10.2", false))
	sec := startES(t, "docker.elastic.co/elasticsearch/elasticsearch:8.15.3", true)
	clusters = append(clusters, sec)
	clusters = append(clusters, startES(t, "docker.elastic.co/elasticsearch/elasticsearch:9.1.0", true))
	clusters = append(clusters, startOS(t, "opensearchproject/opensearch:2.11.1"))
	for _, cl := range clusters {
		t.Run(cl.name, func(t *testing.T) { runCluster(t, cl) })
	}
	t.Run("api key", func(t *testing.T) {
		c, err := es.Open(sec.cfg)
		if err != nil {
			t.Fatal(err)
		}
		resp := raw(t, c, "POST", "_security/api_key", `{"name":"datavim-test"}`)
		var k struct{ Encoded string }
		json.Unmarshal(resp.Body, &k)
		cfg := sec.cfg
		cfg.User, cfg.Password, cfg.APIKey = "", "", k.Encoded
		kc, _ := es.Open(cfg)
		if _, err := es.GetInfo(context.Background(), kc); err != nil {
			t.Fatal(err)
		}
		cfg.APIKey = "bad"
		bc, _ := es.Open(cfg)
		if _, err := es.GetInfo(context.Background(), bc); !errors.Is(err, es.ErrAuthFailed) {
			t.Errorf("bad key: %v", err)
		}
	})
}

func openContexts(t *testing.T, c *es.Client) int64 {
	t.Helper()
	resp := raw(t, c, "GET", "_nodes/stats/indices/search", "")
	var r struct {
		Nodes map[string]struct {
			Indices struct {
				Search struct {
					Open int64 `json:"open_contexts"`
				} `json:"search"`
			} `json:"indices"`
		} `json:"nodes"`
	}
	json.Unmarshal(resp.Body, &r)
	var n int64
	for _, v := range r.Nodes {
		n += v.Indices.Search.Open
	}
	return n
}

func runCluster(t *testing.T, cl cluster) {
	ctx := context.Background()
	c, err := es.Open(cl.cfg)
	if err != nil {
		t.Fatal(err)
	}
	info, err := es.GetInfo(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s %s", info.Distribution, info.Version)

	if err := es.CreateIndex(ctx, c, es.NewIndex{Name: "docs", Shards: 2, Replicas: 0, Mappings: json.RawMessage(`{"properties":{"n":{"type":"long"},"s":{"type":"keyword"}}}`)}); err != nil {
		t.Fatal(err)
	}
	load(t, c, "docs", 25000)

	t.Run("meta", func(t *testing.T) {
		idx, err := es.Indices(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, i := range idx {
			if i.Name == "docs" {
				found = i.Docs == 25000 && i.Primaries == 2
			}
		}
		if !found {
			t.Errorf("indices = %+v", idx)
		}
		m, err := es.Mappings(ctx, c, "docs")
		if err != nil || len(m["docs"]) != 2 {
			t.Errorf("mappings %+v %v", m, err)
		}
		s, err := es.Settings(ctx, c, "docs")
		if err != nil || len(s) < 10 {
			t.Errorf("settings %d %v", len(s), err)
		}
	})

	t.Run("cursor", func(t *testing.T) {
		cur := es.OpenCursor(info, c, es.SearchSpec{Target: "docs", PageSize: 1000, ExactTotal: true})
		seen := map[string]bool{}
		for {
			p, err := cur.Next(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if p.Total != 25000 {
				t.Fatalf("total %d", p.Total)
			}
			for _, h := range p.Hits {
				if seen[h.ID] {
					t.Fatalf("%s twice", h.ID)
				}
				seen[h.ID] = true
			}
			if p.Done {
				break
			}
		}
		if len(seen) != 25000 {
			t.Errorf("saw %d", len(seen))
		}
		if err := cur.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if n := openContexts(t, c); n != 0 {
			t.Errorf("open contexts after close: %d", n)
		}
	})

	t.Run("conflict", func(t *testing.T) {
		h, err := es.GetDoc(ctx, c, "docs", "d1")
		if err != nil {
			t.Fatal(err)
		}
		raw(t, c, "PUT", "docs/_doc/d1?refresh=true", `{"n":1,"s":"other"}`)
		_, err = es.PutDoc(ctx, c, "docs", "d1", "", json.RawMessage(`{"n":1,"s":"mine"}`), es.Version{SeqNo: h.SeqNo, PrimaryTerm: h.PrimaryTerm})
		if !errors.Is(err, es.ErrConflict) {
			t.Fatalf("err = %v", err)
		}
		h, _ = es.GetDoc(ctx, c, "docs", "d1")
		if !strings.Contains(string(h.Source), "other") {
			t.Errorf("source = %s", h.Source)
		}
		if _, err := es.PutDoc(ctx, c, "docs", "d1", "", json.RawMessage(`{"n":1,"s":"mine"}`), es.Version{SeqNo: h.SeqNo, PrimaryTerm: h.PrimaryTerm}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("delete by query", func(t *testing.T) {
		q := json.RawMessage(`{"term":{"s":"v3"}}`)
		n, err := es.Count(ctx, c, "docs", q)
		if err != nil || n == 0 {
			t.Fatal(n, err)
		}
		task, err := es.DeleteByQuery(ctx, c, []string{"docs"}, q)
		if err != nil {
			t.Fatal(err)
		}
		var st es.TaskStatus
		for !st.Completed {
			if st, err = es.GetTask(ctx, c, task); err != nil {
				t.Fatal(err)
			}
		}
		if st.Deleted != n {
			t.Errorf("deleted %d of %d", st.Deleted, n)
		}
	})

	t.Run("index ops", func(t *testing.T) {
		for _, n := range []string{"tmp-1", "tmp-2"} {
			if err := es.CreateIndex(ctx, c, es.NewIndex{Name: n, Shards: 1, Replicas: 0}); err != nil {
				t.Fatal(err)
			}
		}
		if err := es.CloseIndex(ctx, c, "tmp-1"); err != nil {
			t.Fatal(err)
		}
		if err := es.OpenIndex(ctx, c, "tmp-1"); err != nil {
			t.Fatal(err)
		}
		for _, f := range []func(context.Context, es.Doer, string) error{es.Refresh, es.Flush, es.ClearCache} {
			if err := f(ctx, c, "tmp-1"); err != nil {
				t.Fatal(err)
			}
		}
		r, err := es.ResolveIndex(ctx, c, "tmp-*")
		if err != nil || len(r.Indices) != 2 {
			t.Fatalf("resolve %+v %v", r, err)
		}
		// an index made after the expansion is not deleted with it
		es.CreateIndex(ctx, c, es.NewIndex{Name: "tmp-3", Shards: 1, Replicas: 0})
		if err := es.DeleteIndices(ctx, c, r.Indices); err != nil {
			t.Fatal(err)
		}
		if r, _ := es.ResolveIndex(ctx, c, "tmp-*"); len(r.Indices) != 1 || r.Indices[0] != "tmp-3" {
			t.Errorf("left %+v", r)
		}
	})

	t.Run("alias", func(t *testing.T) {
		yes := true
		if err := es.UpdateAliases(ctx, c, []es.AliasAction{{Add: true, Index: "docs", Alias: "docs-alias", IsWrite: &yes}}); err != nil {
			t.Fatal(err)
		}
		h, err := es.GetDoc(ctx, c, "docs-alias", "d2")
		if err != nil || h.Index != "docs" {
			t.Fatalf("%+v %v", h, err)
		}
		if _, err := es.PutDoc(ctx, c, h.Index, h.ID, h.Routing, json.RawMessage(`{"n":2,"s":"via alias"}`), es.Version{SeqNo: h.SeqNo, PrimaryTerm: h.PrimaryTerm}); err != nil {
			t.Fatal(err)
		}
		if err := es.PutSettings(ctx, c, "docs", map[string]*string{"index.refresh_interval": ptr("5s")}); err != nil {
			t.Fatal(err)
		}
		if err := es.ForceMerge(ctx, c, "docs", 1); err != nil {
			t.Fatal(err)
		}
	})
}

func ptr(s string) *string { return &s }
