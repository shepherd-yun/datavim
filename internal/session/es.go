package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/history"
)

// ESClient is an open Elasticsearch client; *es.Client implements it.
type ESClient interface {
	es.Doer
	Close()
}

// ESOpener opens an Elasticsearch client without sending anything.
type ESOpener func(cfg es.Config) (ESClient, error)

// esConn is one open Elasticsearch connection.
type esConn struct {
	raw     ESClient
	info    es.Info
	guarded *guardedES
}

// guardedES is the one Doer the UI gets for an Elasticsearch connection,
// so every request — the typed operations and the console's — passes
// here (spec §9.3): anything esx.Classify does not grade a read is
// refused on a read-only connection and audited otherwise, without its
// body. Confirmations are the UI's, before the call.
type guardedES struct {
	m    *Manager
	name string
	raw  es.Doer
}

func (g *guardedES) Do(ctx context.Context, r es.Request) (es.Response, error) {
	cl := esx.Classify(r.Method, r.Path, r.Body)
	if cl.Level == esx.LevelRead {
		return g.raw.Do(ctx, r)
	}
	c, ok := g.m.Connection(g.name)
	if !ok {
		return es.Response{}, fmt.Errorf("unknown connection %q", g.name)
	}
	if c.IsReadOnly() {
		return es.Response{}, ErrReadOnly
	}
	start := time.Now()
	resp, err := g.raw.Do(ctx, r)
	failed := err
	if failed == nil {
		failed = es.CheckStatus(resp)
	}
	path, _, _ := strings.Cut(strings.TrimLeft(r.Path, "/"), "?")
	g.m.record(history.AuditEntry{
		Conn: g.name, Kind: history.AuditES, Op: strings.ToUpper(r.Method) + " " + path,
		Object: strings.Join(cl.Targets, ","), Detail: cl.Level.String(),
	}, start, failed)
	return resp, err
}

// ES returns the connection's guarded Doer, opening the client on first
// use and checking it with GET /.
func (m *Manager) ES(ctx context.Context, name string) (es.Doer, error) {
	cfg, ok := m.Connection(name)
	switch {
	case !ok:
		return nil, fmt.Errorf("unknown connection %q", name)
	case cfg.Driver != "elasticsearch":
		return nil, fmt.Errorf("connection %q is not an elasticsearch connection", name)
	case m.ESOpen == nil:
		return nil, fmt.Errorf("connection %q: driver %q not available", name, cfg.Driver)
	}
	m.mu.Lock()
	c, ok := m.es[name]
	gen := m.esGen[name]
	m.mu.Unlock()
	if ok {
		return c.guarded, nil
	}
	ec, err := m.esConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	raw, err := m.ESOpen(ec)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", name, err)
	}
	info, err := es.GetInfo(ctx, raw)
	if err != nil {
		raw.Close()
		switch {
		case !errors.Is(err, es.ErrAuthFailed):
			return nil, fmt.Errorf("connect %s: %w", name, err)
		case cfg.Params["auth"] == "apikey":
			return nil, m.refusedSecret(name, SecretAPIKey)
		}
		return nil, ErrPasswordRequired
	}
	m.mu.Lock()
	existing, ok := m.es[name]
	stale := m.esGen[name] != gen
	if !ok && !stale {
		c := &esConn{raw: raw, info: info, guarded: &guardedES{m: m, name: name, raw: raw}}
		m.es[name] = c
		m.mu.Unlock()
		return c.guarded, nil
	}
	m.mu.Unlock()
	raw.Close()
	if ok {
		return existing.guarded, nil // lost a race with another opener
	}
	return m.ES(ctx, name) // reset while opening: the settings are old
}

// ESInfo is what GET / said when the connection opened.
func (m *Manager) ESInfo(name string) (es.Info, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.es[name]
	if !ok {
		return es.Info{}, false
	}
	return c.info, true
}

// esConfig maps the connection to an es.Config: urls, Basic auth with
// the known password when a user is set (none known: ErrPasswordRequired)
// or the API key (*SecretRequiredError), TLS params and the SSH tunnel.
func (m *Manager) esConfig(ctx context.Context, cfg config.Connection) (es.Config, error) {
	urls, err := config.ESURLs(cfg)
	if err != nil {
		return es.Config{}, err
	}
	ec := es.Config{
		URLs: urls, TLSSkipVerify: param(cfg, "tls_skip_verify"),
		TLSCA: cfg.Params["tls_ca"], TLSCert: cfg.Params["tls_cert"], TLSKey: cfg.Params["tls_key"],
	}
	switch {
	case cfg.Params["auth"] == "apikey":
		if ec.APIKey, err = m.secretFor(cfg.Name, SecretAPIKey); err != nil {
			return es.Config{}, err
		}
	case cfg.User != "":
		pw, err := m.password(cfg.Name)
		if err != nil {
			return es.Config{}, err
		}
		ec.User, ec.Password = cfg.User, pw
	}
	dial, err := m.dial(ctx, cfg)
	if err != nil {
		return es.Config{}, err
	}
	ec.Dial = dial
	return ec, nil
}
