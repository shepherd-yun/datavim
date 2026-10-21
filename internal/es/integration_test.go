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
	"time"

	"github.com/testcontainers/testcontainers-go"
	tces "github.com/testcontainers/testcontainers-go/modules/elasticsearch"
	tcos "github.com/testcontainers/testcontainers-go/modules/opensearch"

	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
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

	runClusterOps(t, cl, c)

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

// waitTask polls a task until it completes.
func waitTask(t *testing.T, c *es.Client, task string) es.TaskStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		st, err := es.GetTask(context.Background(), c, task)
		if err != nil {
			t.Fatal(err)
		}
		if st.Completed {
			return st
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("task %s did not complete", task)
	return es.TaskStatus{}
}

// runClusterOps are the E2 cluster operations (spec §7).
func runClusterOps(t *testing.T, cl cluster, c *es.Client) {
	ctx := context.Background()
	t.Run("templates", func(t *testing.T) {
		kinds := []es.TemplateKind{es.TemplateComponent, es.TemplateComposable, es.TemplateLegacy}
		bodies := map[es.TemplateKind]string{
			es.TemplateComponent:  `{"template":{"settings":{"number_of_shards":1}}}`,
			es.TemplateComposable: `{"index_patterns":["dvt-*"],"priority":321,"composed_of":["dvt-base"]}`,
			es.TemplateLegacy:     `{"index_patterns":["dvl-*"],"order":4}`,
		}
		before, err := es.Templates(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if cl.core {
			return // OpenSearch: the read only
		}
		supported := map[es.TemplateKind]bool{}
		for _, k := range kinds {
			name := "dvt-base"
			if k != es.TemplateComponent {
				name = "dvt-" + k.String()
			}
			if err := es.PutTemplate(ctx, c, k, name, json.RawMessage(bodies[k])); err != nil {
				if k == es.TemplateLegacy {
					t.Logf("legacy templates: %v", err)
					continue
				}
				t.Fatal(k, err)
			}
			supported[k] = true
		}
		got, err := es.Templates(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]es.Template{}
		for _, tp := range got {
			found[tp.Kind.String()+"/"+tp.Name] = tp
		}
		if tp := found["composable/dvt-composable"]; tp.Priority != 321 || len(tp.Patterns) != 1 || tp.Patterns[0] != "dvt-*" {
			t.Errorf("composable = %+v", tp)
		}
		if _, ok := found["component/dvt-base"]; !ok {
			t.Errorf("no component template in %d", len(got))
		}
		if supported[es.TemplateLegacy] && found["legacy/dvt-legacy"].Priority != 4 {
			t.Errorf("legacy = %+v", found["legacy/dvt-legacy"])
		}
		for _, k := range []es.TemplateKind{es.TemplateComposable, es.TemplateLegacy, es.TemplateComponent} {
			if !supported[k] {
				continue
			}
			name := "dvt-base"
			if k != es.TemplateComponent {
				name = "dvt-" + k.String()
			}
			if err := es.DeleteTemplate(ctx, c, k, name); err != nil {
				t.Fatal(k, err)
			}
		}
		if after, _ := es.Templates(ctx, c); len(after) != len(before) {
			t.Errorf("templates after delete %d, before %d", len(after), len(before))
		}
	})
	if cl.core {
		return
	}

	t.Run("cluster reads", func(t *testing.T) {
		if err := es.CreateIndex(ctx, c, es.NewIndex{Name: "repl", Shards: 1, Replicas: 1}); err != nil {
			t.Fatal(err)
		}
		shards, err := es.Shards(ctx, c, "repl")
		if err != nil {
			t.Fatal(err)
		}
		var replica *es.Shard
		for i, s := range shards {
			if !s.Primary && s.State == "UNASSIGNED" {
				replica = &shards[i]
			}
		}
		if replica == nil {
			t.Fatalf("shards = %+v", shards)
		}
		raw, err := es.AllocationExplain(ctx, c, "repl", replica.Shard, false)
		if err != nil {
			t.Fatal(err)
		}
		if sum := strings.Join(esx.ExplainSummary(raw), "\n"); !strings.Contains(sum, "same_shard") {
			t.Errorf("explain:\n%s", sum)
		}
		nodes, err := es.Nodes(ctx, c)
		if err != nil || len(nodes) == 0 || !nodes[0].Master || nodes[0].HeapPct <= 0 {
			t.Errorf("nodes %+v %v", nodes, err)
		}
		if st, err := es.NodeStats(ctx, c, nodes[0].ID); err != nil || len(esx.NodeSummary(st)) < 5 {
			t.Errorf("node stats %v: %v", esx.NodeSummary(st), err)
		}
		if a, err := es.Allocation(ctx, c); err != nil || len(a) == 0 {
			t.Errorf("allocation %+v %v", a, err)
		}
		if _, err := es.PendingTasks(ctx, c); err != nil {
			t.Error(err)
		}
		if set, err := es.ClusterSettings(ctx, c); err != nil || len(set) < 10 {
			t.Errorf("cluster settings %d %v", len(set), err)
		}
	})

	t.Run("reindex", func(t *testing.T) {
		if err := es.CreateIndex(ctx, c, es.NewIndex{Name: "rsrc", Shards: 2, Replicas: 0, Mappings: json.RawMessage(`{"properties":{"n":{"type":"long"},"s":{"type":"keyword"}}}`), Settings: json.RawMessage(`{"analysis":{"analyzer":{"my":{"type":"custom","tokenizer":"standard"}}}}`)}); err != nil {
			t.Fatal(err)
		}
		load(t, c, "rsrc", 5000)
		flat, err := es.RawSettings(ctx, c, "rsrc")
		if err != nil {
			t.Fatal(err)
		}
		settings, shards, replicas := esx.CopyableSettings(flat)
		b, _ := json.Marshal(settings)
		maps, _ := es.RawMappings(ctx, c, "rsrc")
		if err := es.CreateIndex(ctx, c, es.NewIndex{Name: "rdst", Shards: shards, Replicas: replicas, Mappings: maps["rsrc"], Settings: b}); err != nil {
			t.Fatal(err)
		}
		task, err := es.Reindex(ctx, c, es.ReindexSpec{Source: "rsrc", Dest: "rdst", OpType: "index", Conflicts: "abort"})
		if err != nil {
			t.Fatal(err)
		}
		if st := waitTask(t, c, task); st.Created != 5000 || st.Err != "" {
			t.Errorf("reindex = %+v", st)
		}
		es.Refresh(ctx, c, "rdst")
		if n, err := es.Count(ctx, c, "rdst", json.RawMessage(`{"match_all":{}}`)); err != nil || n != 5000 {
			t.Errorf("count %d %v", n, err)
		}
		dst, _ := es.Mappings(ctx, c, "rdst")
		src, _ := es.Mappings(ctx, c, "rsrc")
		if fmt.Sprint(dst["rdst"]) != fmt.Sprint(src["rsrc"]) {
			t.Errorf("mapping %v != %v", dst, src)
		}
	})

	t.Run("update by query", func(t *testing.T) {
		q := json.RawMessage(`{"terms":{"n":[0,1,2,3,4,5,6,7,8,9]}}`)
		task, err := es.UpdateByQuery(ctx, c, []string{"rsrc"}, q, "ctx._source.s = 'updated'")
		if err != nil {
			t.Fatal(err)
		}
		if st := waitTask(t, c, task); st.Updated != 10 {
			t.Errorf("update = %+v", st)
		}
	})

	t.Run("tasks and cancel", func(t *testing.T) {
		resp := raw(t, c, "POST", "_reindex?wait_for_completion=false&requests_per_second=1", `{"source":{"index":"rsrc","size":10},"dest":{"index":"slow"}}`)
		var r struct{ Task string }
		json.Unmarshal(resp.Body, &r)
		tasks, err := es.Tasks(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, k := range tasks {
			found = found || k.ID == r.Task && k.Cancellable
		}
		if !found {
			t.Fatalf("task %s not in %+v", r.Task, tasks)
		}
		if err := es.CancelTask(ctx, c, r.Task); err != nil {
			t.Fatal(err)
		}
		waitTask(t, c, r.Task)
		if err := es.CancelTask(ctx, c, r.Task); !errors.Is(err, es.ErrTaskGone) {
			t.Errorf("second cancel: %v", err)
		}
	})

	t.Run("mapping and cluster settings", func(t *testing.T) {
		if err := es.PutMapping(ctx, c, "rsrc", json.RawMessage(`{"note":{"type":"text"}}`)); err != nil {
			t.Fatal(err)
		}
		var e *es.APIError
		if err := es.PutMapping(ctx, c, "rsrc", json.RawMessage(`{"s":{"type":"long"}}`)); !errors.As(err, &e) || e.Status != 400 {
			t.Errorf("type change: %v", err)
		}
		const key = "cluster.routing.allocation.cluster_concurrent_rebalance"
		if err := es.PutClusterSettings(ctx, c, nil, map[string]*string{key: ptr("3")}); err != nil {
			if !strings.Contains(err.Error(), "transient") {
				t.Fatal(err)
			}
			// ES 9 has no transient settings
			if err := es.PutClusterSettings(ctx, c, map[string]*string{key: ptr("3")}, nil); err != nil {
				t.Fatal(err)
			}
		}
		set, _ := es.ClusterSettings(ctx, c)
		got := ""
		for _, s := range set {
			if s.Key == key && s.Scope != "defaults" {
				got = s.Value
			}
		}
		if got != "3" {
			t.Errorf("%s = %q", key, got)
		}
		if err := es.PutClusterSettings(ctx, c, map[string]*string{key: nil}, map[string]*string{key: nil}); err != nil {
			t.Fatal(err)
		}
	})
}
