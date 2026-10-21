package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/session"
)

// fakeES is an Elasticsearch cluster answering from routes, keyed
// "METHOD path" with the path's query string cut off.
type fakeES struct {
	mu     sync.Mutex
	info   es.Info
	routes map[string]func(r es.Request) es.Response
	reqs   []es.Request
}

func newFakeES() *fakeES {
	return &fakeES{
		info:   es.Info{ClusterName: "dev", Version: "8.11.0", Distribution: "elasticsearch", Major: 8, Minor: 11},
		routes: map[string]func(es.Request) es.Response{},
	}
}

func (f *fakeES) Do(_ context.Context, r es.Request) (es.Response, error) {
	f.mu.Lock()
	r.Path = strings.TrimLeft(r.Path, "/")
	f.reqs = append(f.reqs, r)
	path, _, _ := strings.Cut(r.Path, "?")
	h := f.routes[r.Method+" "+path]
	f.mu.Unlock()
	if h == nil {
		return es.Response{Status: 404, Body: []byte(fmt.Sprintf(`{"error":{"type":"fake","reason":"no route %s %s"},"status":404}`, r.Method, path))}, nil
	}
	return h(r), nil
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
