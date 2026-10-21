// Package es is datavim's only connection to Elasticsearch and
// OpenSearch servers: one HTTP client per connection and typed
// operations built on the Doer interface.
package es

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Request is one REST request. Path may carry a query string; the
// leading "/" is optional.
type Request struct {
	Method, Path string
	Body         []byte
	NDJSON       bool // _bulk / _msearch bodies
}

// Response is a server's answer, whatever its status.
type Response struct {
	Status int
	Body   []byte
	Took   time.Duration
}

// Doer sends requests. The typed operations of this package take a Doer
// so the session's guarded one sees every request.
type Doer interface {
	Do(ctx context.Context, r Request) (Response, error)
}

// Config is how to reach a cluster.
type Config struct {
	URLs                   []string
	User, Password, APIKey string
	TLSSkipVerify          bool
	TLSCA, TLSCert, TLSKey string // file paths
	// Dial opens the network connections (SSH tunnels); nil dials directly.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// MaxBody bounds a response body; 0 is DefaultMaxBody.
	MaxBody int64
}

// DefaultMaxBody is the response body limit.
const DefaultMaxBody = 50 << 20

// Client is one cluster's HTTP client, safe for concurrent use.
type Client struct {
	urls []*url.URL
	cur  atomic.Int32 // the address that last answered
	hc   *http.Client
	cfg  Config
}

// Open builds a client without sending anything.
func Open(cfg Config) (*Client, error) {
	if len(cfg.URLs) == 0 {
		return nil, errors.New("elasticsearch: no urls")
	}
	if cfg.MaxBody <= 0 {
		cfg.MaxBody = DefaultMaxBody
	}
	c := &Client{cfg: cfg}
	for _, raw := range cfg.URLs {
		u, err := url.Parse(strings.TrimRight(raw, "/"))
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("elasticsearch: bad url %q", redact(raw))
		}
		if u.User != nil { // credentials go in the config, not the URL
			if c.cfg.User == "" && c.cfg.APIKey == "" {
				c.cfg.User = u.User.Username()
				c.cfg.Password, _ = u.User.Password()
			}
			u.User = nil
		}
		c.urls = append(c.urls, u)
	}
	tlsCfg, err := tlsConfig(cfg)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		TLSClientConfig:     tlsCfg,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if cfg.Dial != nil {
		tr.DialContext = cfg.Dial
	} else {
		tr.Proxy = http.ProxyFromEnvironment
		tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	c.hc = &http.Client{Transport: tr}
	return c, nil
}

func tlsConfig(cfg Config) (*tls.Config, error) {
	t := &tls.Config{InsecureSkipVerify: cfg.TLSSkipVerify} //nolint:gosec // the user's tls_skip_verify
	if cfg.TLSCA != "" {
		pem, err := os.ReadFile(cfg.TLSCA)
		if err != nil {
			return nil, fmt.Errorf("elasticsearch tls_ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("elasticsearch tls_ca: no certificates in %s", cfg.TLSCA)
		}
		t.RootCAs = pool
	}
	if cfg.TLSCert != "" || cfg.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("elasticsearch tls_cert / tls_key: %w", err)
		}
		t.Certificates = []tls.Certificate{cert}
	}
	return t, nil
}

// Close drops the idle connections.
func (c *Client) Close() { c.hc.CloseIdleConnections() }

// Do sends r to the addresses in order, starting from the one that last
// answered; only a failure to connect moves on to the next. A non-2xx
// status is no error here: see CheckStatus.
func (c *Client) Do(ctx context.Context, r Request) (Response, error) {
	start := int(c.cur.Load())
	var lastErr error
	for i := range c.urls {
		n := (start + i) % len(c.urls)
		resp, err := c.do(ctx, c.urls[n], r)
		if err == nil {
			c.cur.Store(int32(n))
			return resp, nil
		}
		if !connectFailed(err) || ctx.Err() != nil {
			return resp, err
		}
		lastErr = err
	}
	return Response{}, lastErr
}

func (c *Client) do(ctx context.Context, base *url.URL, r Request) (resp Response, err error) {
	path, query, _ := strings.Cut(strings.TrimLeft(r.Path, "/"), "?")
	u := *base
	u.RawPath = strings.TrimRight(base.EscapedPath(), "/") + "/" + path
	if u.Path, err = url.PathUnescape(u.RawPath); err != nil {
		u.Path, u.RawPath = u.RawPath, ""
	}
	u.RawQuery = query
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(r.Method), u.String(), body)
	if err != nil {
		return Response{}, fmt.Errorf("elasticsearch: %w", err)
	}
	ct := "application/json"
	if r.NDJSON {
		ct = "application/x-ndjson"
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Accept", "application/json")
	switch {
	case c.cfg.APIKey != "":
		req.Header.Set("Authorization", "ApiKey "+c.cfg.APIKey)
	case c.cfg.User != "":
		req.SetBasicAuth(c.cfg.User, c.cfg.Password)
	}
	t0 := time.Now()
	hr, err := c.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return Response{}, &connError{host: base.Host, err: err}
	}
	defer hr.Body.Close()
	b, err := io.ReadAll(io.LimitReader(hr.Body, c.cfg.MaxBody+1))
	took := time.Since(t0)
	slog.Debug("elasticsearch request", "method", req.Method, "path", "/"+path, "status", hr.StatusCode, "took", took)
	if err != nil {
		return Response{}, fmt.Errorf("elasticsearch %s: %w", base.Host, err)
	}
	if int64(len(b)) > c.cfg.MaxBody {
		return Response{}, ErrTooLarge
	}
	return Response{Status: hr.StatusCode, Body: b, Took: took}, nil
}

// connError is a request that got no response from host.
type connError struct {
	host string
	err  error
}

func (e *connError) Error() string { return "elasticsearch " + e.host + ": " + e.err.Error() }
func (e *connError) Unwrap() error { return e.err }

// connectFailed reports an error from before the request reached a server.
func connectFailed(err error) bool {
	var ce *connError
	if !errors.As(err, &ce) {
		return false
	}
	var op *net.OpError
	var dns *net.DNSError
	return errors.As(err, &op) && op.Op == "dial" || errors.As(err, &dns)
}

// redact drops the userinfo of a URL for messages.
func redact(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		return raw[at+1:]
	}
	return raw
}
