package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeConnect is a Kafka Connect REST API with two connectors.
type fakeConnect struct {
	mu      sync.Mutex
	reqs    []string // "METHOD path?query"
	auth    []string // the Authorization header of every request
	configs map[string]map[string]string
	status  int // answered to every request when set
	srv     *httptest.Server
}

func newFakeConnect(t *testing.T) *fakeConnect {
	t.Helper()
	f := &fakeConnect{configs: map[string]map[string]string{
		"orders-sink": {"connector.class": "FileStreamSink", "tasks.max": "1", "topics": "orders", "connection.password": "s3cret"},
		"a/b":         {"connector.class": "FileStreamSource", "tasks.max": "2"},
	}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeConnect) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeConnect) statusOf(name string) map[string]any {
	tasks := []map[string]any{{"id": 0, "state": "RUNNING", "worker_id": "w1:8083"}}
	state := "RUNNING"
	if name == "a/b" {
		tasks = append(tasks, map[string]any{"id": 1, "state": "FAILED", "worker_id": "w2:8083", "trace": "java.lang.Boom\n\tat x"})
	}
	typ := "sink"
	if name == "a/b" {
		typ = "source"
	}
	return map[string]any{"name": name, "connector": map[string]any{"state": state, "worker_id": "w1:8083"}, "tasks": tasks, "type": typ}
}

func (f *fakeConnect) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.EscapedPath()
	f.reqs = append(f.reqs, strings.TrimSuffix(r.Method+" "+path+"?"+r.URL.RawQuery, "?"))
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if f.status != 0 {
		writeJSON(w, f.status, map[string]any{"error_code": f.status, "message": "nope"})
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	name := ""
	if len(parts) > 1 && parts[0] == "connectors" {
		var err error
		if name, err = urlUnescape(parts[1]); err != nil {
			writeJSON(w, 400, map[string]any{"message": "bad name"})
			return
		}
		if _, ok := f.configs[name]; !ok {
			writeJSON(w, 404, map[string]any{"error_code": 404, "message": "Connector " + name + " not found"})
			return
		}
	}
	switch {
	case r.Method == "GET" && path == "/":
		writeJSON(w, 200, map[string]any{"version": "3.7.0"})
	case r.Method == "GET" && path == "/connectors":
		out := map[string]any{}
		for n, cfg := range f.configs {
			out[n] = map[string]any{"info": map[string]any{"name": n, "config": cfg, "type": f.statusOf(n)["type"]}, "status": f.statusOf(n)}
		}
		writeJSON(w, 200, out)
	case r.Method == "GET" && len(parts) == 3 && parts[2] == "status":
		writeJSON(w, 200, f.statusOf(name))
	case r.Method == "GET" && len(parts) == 3 && parts[2] == "config":
		writeJSON(w, 200, f.configs[name])
	case r.Method == "PUT" && len(parts) == 3 && parts[2] == "config":
		var cfg map[string]string
		_ = json.NewDecoder(r.Body).Decode(&cfg)
		f.configs[name] = cfg
		writeJSON(w, 200, map[string]any{"name": name, "config": cfg})
	case r.Method == "PUT" && len(parts) == 3 && (parts[2] == "pause" || parts[2] == "resume"):
		w.WriteHeader(202)
	case r.Method == "POST" && len(parts) == 3 && parts[2] == "restart":
		writeJSON(w, 202, f.statusOf(name))
	case r.Method == "POST" && len(parts) == 5 && parts[2] == "tasks" && parts[4] == "restart":
		w.WriteHeader(204)
	case r.Method == "PUT" && strings.HasPrefix(path, "/connector-plugins/") && strings.HasSuffix(path, "/config/validate"):
		var cfg map[string]string
		_ = json.NewDecoder(r.Body).Decode(&cfg)
		configs := []map[string]any{}
		n := 0
		if cfg["file"] == "" {
			n = 1
			configs = append(configs, map[string]any{"value": map[string]any{"name": "file", "errors": []string{"Missing required configuration \"file\""}}})
		}
		configs = append(configs, map[string]any{"value": map[string]any{"name": "tasks.max", "errors": []string{}}})
		writeJSON(w, 200, map[string]any{"name": "FileStreamSink", "error_count": n, "configs": configs})
	default:
		writeJSON(w, 404, map[string]any{"error_code": 404, "message": "no route " + path})
	}
}

func openConnect(t *testing.T, f *fakeConnect, cfg ConnectConfig) Connect {
	t.Helper()
	if cfg.URL == "" {
		cfg.URL = f.srv.URL
	}
	c, err := OpenConnect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConnectorsExpand(t *testing.T) {
	f := newFakeConnect(t)
	c := openConnect(t, f, ConnectConfig{})
	if v, err := c.Version(context.Background()); err != nil || v != "3.7.0" {
		t.Fatalf("Version = %q, %v", v, err)
	}
	cs, err := c.Connectors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].Name != "a/b" || cs[1].Name != "orders-sink" {
		t.Fatalf("connectors = %+v", cs)
	}
	ab := cs[0]
	if ab.Type != "source" || ab.Class != "FileStreamSource" || ab.State != "RUNNING" || ab.Worker != "w1:8083" ||
		len(ab.Tasks) != 2 || ab.Tasks[1].State != "FAILED" || ab.Tasks[1].ID != 1 || !strings.Contains(ab.Tasks[1].Trace, "Boom") {
		t.Fatalf("a/b = %+v", ab)
	}
	if cs[1].Config["topics"] != "orders" {
		t.Fatalf("config = %v", cs[1].Config)
	}
	if got := f.requests(); !slices.Contains(got, "GET /connectors?expand=status&expand=info") {
		t.Fatalf("requests = %v", got)
	}
	one, err := c.Connector(context.Background(), "orders-sink")
	if err != nil || one.Name != "orders-sink" || one.Type != "sink" || one.Config["tasks.max"] != "1" || len(one.Tasks) != 1 {
		t.Fatalf("Connector = %+v, %v", one, err)
	}
	if _, err := c.Connector(context.Background(), "nope"); !errors.Is(err, ErrConnectorNotFound) {
		t.Fatalf("missing connector: %v", err)
	}
}

func TestConnectActions(t *testing.T) {
	f := newFakeConnect(t)
	c := openConnect(t, f, ConnectConfig{})
	ctx := context.Background()
	for _, err := range []error{
		c.Pause(ctx, "a/b"), c.Resume(ctx, "a/b"), c.Restart(ctx, "a/b"), c.RestartTask(ctx, "a/b", 2),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"PUT /connectors/a%2Fb/pause", "PUT /connectors/a%2Fb/resume",
		"POST /connectors/a%2Fb/restart?includeTasks=true&onlyFailed=true", "POST /connectors/a%2Fb/tasks/2/restart",
	}
	if got := f.requests(); !slices.Equal(got, want) {
		t.Fatalf("requests = %v\nwant %v", got, want)
	}
}

func TestSetConfigChanged(t *testing.T) {
	f := newFakeConnect(t)
	c := openConnect(t, f, ConnectConfig{})
	ctx := context.Background()
	old := maps.Clone(f.configs["orders-sink"])
	cfg := maps.Clone(old)
	cfg["tasks.max"] = "3"
	f.configs["orders-sink"]["topics"] = "orders,refunds" // someone else
	if err := c.SetConfig(ctx, "orders-sink", old, cfg); !errors.Is(err, ErrConnectorChanged) {
		t.Fatalf("err = %v", err)
	}
	for _, r := range f.requests() {
		if strings.HasPrefix(r, "PUT") {
			t.Fatalf("sent %s", r)
		}
	}
	old = maps.Clone(f.configs["orders-sink"])
	cfg = maps.Clone(old)
	cfg["tasks.max"] = "3"
	if err := c.SetConfig(ctx, "orders-sink", old, cfg); err != nil {
		t.Fatal(err)
	}
	if f.configs["orders-sink"]["tasks.max"] != "3" {
		t.Fatalf("config = %v", f.configs["orders-sink"])
	}
}

func TestValidate(t *testing.T) {
	f := newFakeConnect(t)
	c := openConnect(t, f, ConnectConfig{})
	probs, err := c.Validate(context.Background(), "FileStreamSink", map[string]string{"tasks.max": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(probs) != 1 || probs[0].Name != "file" || !strings.Contains(probs[0].Errors[0], "Missing required") {
		t.Fatalf("problems = %+v", probs)
	}
	if got := f.requests(); got[0] != "PUT /connector-plugins/FileStreamSink/config/validate" {
		t.Fatalf("requests = %v", got)
	}
}

func TestConnectCredentials(t *testing.T) {
	f := newFakeConnect(t)
	u := strings.Replace(f.srv.URL, "http://", "http://u:p4ss@", 1)
	c := openConnect(t, f, ConnectConfig{URL: u})
	if _, err := c.Connectors(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.auth[0]; got != "Basic dTpwNHNz" { // u:p4ss
		t.Fatalf("Authorization = %q", got)
	}
	f.status = 401
	_, err := c.Connectors(context.Background())
	if !errors.Is(err, ErrConnectAuth) || strings.Contains(err.Error(), "p4ss") {
		t.Fatalf("err = %v", err)
	}
	f.srv.Close() // a connection error names the URL: never with its password
	_, err = c.Connectors(context.Background())
	if err == nil || strings.Contains(err.Error(), "p4ss") || strings.Contains(err.Error(), "u:") {
		t.Fatalf("err = %v", err)
	}
}

func TestConnectDial(t *testing.T) {
	f := newFakeConnect(t)
	var dials atomic.Int32
	c := openConnect(t, f, ConnectConfig{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}})
	if _, err := c.Version(context.Background()); err != nil {
		t.Fatal(err)
	}
	if dials.Load() == 0 {
		t.Fatal("the Connect requests did not go through Dial")
	}
}

func TestConnectServerError(t *testing.T) {
	f := newFakeConnect(t)
	c := openConnect(t, f, ConnectConfig{})
	f.status = 409
	err := c.Pause(context.Background(), "a/b")
	if err == nil || !strings.Contains(err.Error(), "nope") || strings.Contains(err.Error(), f.srv.URL) {
		t.Fatalf("err = %v", err)
	}
}

func urlUnescape(s string) (string, error) { return url.PathUnescape(s) }
