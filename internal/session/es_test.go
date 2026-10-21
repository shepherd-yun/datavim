package session

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/history"
)

// fakeES counts the requests that reach the "server"; GET / answers as
// ES 8.11 unless status says otherwise.
type fakeES struct {
	mu     sync.Mutex
	reqs   []es.Request
	closed int
	status int // GET /'s status; 0 is 200
}

func (f *fakeES) Do(_ context.Context, r es.Request) (es.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == "GET" && r.Path == "/" {
		if f.status != 0 {
			return es.Response{Status: f.status, Body: []byte(`{"error":{"type":"security_exception","reason":"unable to authenticate"}}`)}, nil
		}
		return es.Response{Status: 200, Body: []byte(`{"version":{"number":"8.11.0"}}`)}, nil
	}
	f.reqs = append(f.reqs, r)
	return es.Response{Status: 200, Body: []byte(`{"_id":"x","_seq_no":1,"_primary_term":1,"task":"t"}`)}, nil
}

func (f *fakeES) Close() { f.mu.Lock(); f.closed++; f.mu.Unlock() }

func (f *fakeES) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.reqs) }

func setupES(c config.Connection) (*Manager, *fakeES, *[]es.Config) {
	c.Name, c.Driver = "e", "elasticsearch"
	c, _ = config.Normalize(c)
	m := NewManager([]config.Connection{c}, &fakeSecrets{m: map[string]string{}})
	f := &fakeES{}
	var cfgs []es.Config
	m.ESOpen = func(cfg es.Config) (ESClient, error) {
		cfgs = append(cfgs, cfg)
		return f, nil
	}
	return m, f, &cfgs
}

func TestESReadOnlyRefusesEveryWrite(t *testing.T) {
	ro := true
	m, f, _ := setupES(config.Connection{ReadOnly: &ro})
	d, err := m.ES(context.Background(), "e")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	src := json.RawMessage(`{}`)
	calls := map[string]func(es.Doer) error{
		"PutDoc":    func(d es.Doer) error { _, err := es.PutDoc(ctx, d, "o", "1", "", src, es.Version{}); return err },
		"CreateDoc": func(d es.Doer) error { _, err := es.CreateDoc(ctx, d, "o", "", src); return err },
		"DeleteDoc": func(d es.Doer) error { return es.DeleteDoc(ctx, d, "o", "1", "", es.Version{}) },
		"DeleteByQuery": func(d es.Doer) error {
			_, err := es.DeleteByQuery(ctx, d, []string{"o"}, json.RawMessage(`{"term":{"a":1}}`))
			return err
		},
		"CreateIndex":   func(d es.Doer) error { return es.CreateIndex(ctx, d, es.NewIndex{Name: "n"}) },
		"DeleteIndices": func(d es.Doer) error { return es.DeleteIndices(ctx, d, []string{"o"}) },
		"OpenIndex":     func(d es.Doer) error { return es.OpenIndex(ctx, d, "o") },
		"CloseIndex":    func(d es.Doer) error { return es.CloseIndex(ctx, d, "o") },
		"Refresh":       func(d es.Doer) error { return es.Refresh(ctx, d, "o") },
		"Flush":         func(d es.Doer) error { return es.Flush(ctx, d, "o") },
		"ClearCache":    func(d es.Doer) error { return es.ClearCache(ctx, d, "o") },
		"ForceMerge":    func(d es.Doer) error { return es.ForceMerge(ctx, d, "o", 1) },
		"UpdateAliases": func(d es.Doer) error {
			return es.UpdateAliases(ctx, d, []es.AliasAction{{Add: true, Index: "o", Alias: "a"}})
		},
		"PutSettings": func(d es.Doer) error {
			v := "1"
			return es.PutSettings(ctx, d, "o", map[string]*string{"index.number_of_replicas": &v})
		},
		"CancelTask": func(d es.Doer) error { return es.CancelTask(ctx, d, "n1:7") },
		"Reindex": func(d es.Doer) error {
			_, err := es.Reindex(ctx, d, es.ReindexSpec{Source: "o", Dest: "n"})
			return err
		},
		"UpdateByQuery": func(d es.Doer) error {
			_, err := es.UpdateByQuery(ctx, d, []string{"o"}, nil, "ctx._source.a = 1")
			return err
		},
		"PutMapping": func(d es.Doer) error { return es.PutMapping(ctx, d, "o", json.RawMessage(`{"n":{"type":"text"}}`)) },
		"PutTemplate": func(d es.Doer) error {
			return es.PutTemplate(ctx, d, es.TemplateComposable, "t", json.RawMessage(`{"index_patterns":["x"]}`))
		},
		"DeleteTemplate": func(d es.Doer) error { return es.DeleteTemplate(ctx, d, es.TemplateLegacy, "t") },
		"CreateSnapshot": func(d es.Doer) error { return es.CreateSnapshot(ctx, d, es.NewSnapshot{Repo: "r", Name: "s"}) },
		"DeleteSnapshot": func(d es.Doer) error { return es.DeleteSnapshot(ctx, d, "r", "s") },
		"RestoreSnapshot": func(d es.Doer) error {
			return es.RestoreSnapshot(ctx, d, es.RestoreSpec{Repo: "r", Name: "s"})
		},
		"PutRepository":    func(d es.Doer) error { return es.PutRepository(ctx, d, "r", json.RawMessage(`{"type":"fs"}`)) },
		"DeleteRepository": func(d es.Doer) error { return es.DeleteRepository(ctx, d, "r") },
		"PutClusterSettings": func(d es.Doer) error {
			v := "1"
			return es.PutClusterSettings(ctx, d, map[string]*string{"a": &v}, nil)
		},
	}
	for _, name := range es.WriteFuncs {
		call, ok := calls[name]
		if !ok {
			t.Errorf("es.%s has no read-only test: add it to calls", name)
			continue
		}
		if err := call(d); !errors.Is(err, ErrReadOnly) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if got := slices.Sorted(maps.Keys(calls)); !slices.Equal(got, slices.Sorted(slices.Values(es.WriteFuncs))) {
		t.Errorf("calls %v != es.WriteFuncs", got)
	}
	if n := f.count(); n != 0 {
		t.Errorf("%d writes reached the server", n)
	}
	// raw console writes too, and an unknown request counts as one
	for _, r := range []es.Request{{Method: "POST", Path: "_foo/bar"}, {Method: "PUT", Path: "o/_doc/1", Body: src}} {
		if _, err := d.Do(ctx, r); !errors.Is(err, ErrReadOnly) {
			t.Errorf("%s %s: %v", r.Method, r.Path, err)
		}
	}
}

func TestESReadsPassOnReadOnly(t *testing.T) {
	ro := true
	m, f, _ := setupES(config.Connection{ReadOnly: &ro})
	d, _ := m.ES(context.Background(), "e")
	for _, r := range []es.Request{{Method: "GET", Path: "o/_search"}, {Method: "POST", Path: "o/_search", Body: []byte(`{}`)}} {
		if _, err := d.Do(context.Background(), r); err != nil {
			t.Errorf("%s %s: %v", r.Method, r.Path, err)
		}
	}
	if f.count() != 2 {
		t.Errorf("sent %d", f.count())
	}
	// :set noreadonly applies to the Doer already handed out
	_ = m.SetReadOnly("e", false)
	if _, err := d.Do(context.Background(), es.Request{Method: "PUT", Path: "o/_doc/1", Body: []byte(`{}`)}); err != nil {
		t.Errorf("after noreadonly: %v", err)
	}
}

func TestESAudit(t *testing.T) {
	m, _, _ := setupES(config.Connection{})
	var got []history.AuditEntry
	m.Audit = func(e history.AuditEntry) { got = append(got, e) }
	d, _ := m.ES(context.Background(), "e")
	_, _ = d.Do(context.Background(), es.Request{Method: "GET", Path: "o/_search"})
	_, _ = d.Do(context.Background(), es.Request{Method: "PUT", Path: "orders/_doc/1?refresh=wait_for", Body: []byte(`{"secret":"hunter2"}`)})
	_, _ = d.Do(context.Background(), es.Request{Method: "DELETE", Path: "logs-*"})
	if len(got) != 2 {
		t.Fatalf("audit = %+v", got)
	}
	e := got[0]
	if e.Kind != history.AuditES || e.Op != "PUT orders/_doc/1" || e.Object != "orders" || e.Detail != "write" || e.Conn != "e" {
		t.Errorf("entry = %+v", e)
	}
	if strings.Contains(e.Op+e.Object+e.Detail, "hunter2") {
		t.Error("body in the audit")
	}
	if got[1].Detail != "critical" || got[1].Object != "logs-*" {
		t.Errorf("entry = %+v", got[1])
	}
}

func TestESConfig(t *testing.T) {
	m, _, cfgs := setupES(config.Connection{Host: "a,b:9201", User: "elastic", Params: map[string]string{"tls": "true", "tls_ca": "/ca.pem"}})
	if _, err := m.ES(context.Background(), "e"); !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("no password: %v", err)
	}
	_ = m.SetPassword("e", "pw", false)
	if _, err := m.ES(context.Background(), "e"); err != nil {
		t.Fatal(err)
	}
	c := (*cfgs)[0]
	if !slices.Equal(c.URLs, []string{"https://a:9200", "https://b:9201"}) || c.User != "elastic" || c.Password != "pw" || c.TLSCA != "/ca.pem" || c.APIKey != "" {
		t.Errorf("cfg = %+v", c)
	}
	if info, ok := m.ESInfo("e"); !ok || info.Version != "8.11.0" {
		t.Errorf("info = %+v %v", info, ok)
	}
	// no user: no password asked
	m, _, _ = setupES(config.Connection{})
	if _, err := m.ES(context.Background(), "e"); err != nil {
		t.Errorf("anonymous: %v", err)
	}
}

func TestESAPIKeyPrompt(t *testing.T) {
	m, f, cfgs := setupES(config.Connection{Params: map[string]string{"auth": "apikey"}})
	_, err := m.ES(context.Background(), "e")
	var se *SecretRequiredError
	if !errors.As(err, &se) || se.Kind != SecretAPIKey || se.Wrong {
		t.Fatalf("err = %v", err)
	}
	if err := m.SetSecret("e", SecretAPIKey, "k1"); err != nil {
		t.Fatal(err)
	}
	f.status = 401
	if _, err := m.ES(context.Background(), "e"); !errors.As(err, &se) || !se.Wrong {
		t.Fatalf("refused key: %v", err)
	}
	if f.closed != 1 {
		t.Errorf("closed %d", f.closed)
	}
	f.status = 0
	_ = m.SetSecret("e", SecretAPIKey, "k2")
	if _, err := m.ES(context.Background(), "e"); err != nil {
		t.Fatal(err)
	}
	if last := (*cfgs)[len(*cfgs)-1]; last.APIKey != "k2" || last.User != "" {
		t.Errorf("cfg = %+v", last)
	}
}

func TestESBasicRefused(t *testing.T) {
	m, f, _ := setupES(config.Connection{User: "elastic"})
	_ = m.SetPassword("e", "bad", false)
	f.status = 401
	if _, err := m.ES(context.Background(), "e"); !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("err = %v", err)
	}
}

func TestESResetReopens(t *testing.T) {
	m, f, cfgs := setupES(config.Connection{})
	d1, _ := m.ES(context.Background(), "e")
	d2, _ := m.ES(context.Background(), "e")
	if d1 != d2 || len(*cfgs) != 1 {
		t.Fatalf("opened %d times", len(*cfgs))
	}
	m.Reset("e")
	_, _ = m.ES(context.Background(), "e")
	if len(*cfgs) != 2 {
		t.Errorf("opened %d times after reset", len(*cfgs))
	}
	m.CloseAll()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed != 2 {
		t.Errorf("closed %d", f.closed)
	}
}

func TestESNotAvailable(t *testing.T) {
	m, _, _ := setupES(config.Connection{})
	m.ESOpen = nil
	if _, err := m.ES(context.Background(), "e"); err == nil {
		t.Error("no error without an opener")
	}
}
