package es

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func closedURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr
}

func TestDoBasicAndAPIKey(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization")+" "+r.Header.Get("Content-Type"))
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	for _, cfg := range []Config{
		{URLs: []string{srv.URL}},
		{URLs: []string{srv.URL}, User: "elastic", Password: "pw"},
		{URLs: []string{srv.URL}, APIKey: "k1"},
	} {
		c, err := Open(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/"}); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := Open(Config{URLs: []string{srv.URL}})
	c.Do(context.Background(), Request{Method: "POST", Path: "_bulk", Body: []byte("{}\n"), NDJSON: true})
	want := []string{" application/json", "Basic ZWxhc3RpYzpwdw== application/json", "ApiKey k1 application/json", " application/x-ndjson"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q", got)
	}
}

func TestDoPathPrefixAndQuery(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.RequestURI()
	}))
	defer srv.Close()
	c, _ := Open(Config{URLs: []string{srv.URL + "/es"}})
	resp, err := c.Do(context.Background(), Request{Method: "GET", Path: "orders/_search?q=a:1&size=2"})
	if err != nil || resp.Status != 200 {
		t.Fatal(resp, err)
	}
	if got != "GET /es/orders/_search?q=a:1&size=2" {
		t.Errorf("got %q", got)
	}
}

func TestDoFailover(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	c, _ := Open(Config{URLs: []string{closedURL(t), srv.URL}})
	for range 2 {
		if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/"}); err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 2 || c.cur.Load() != 1 {
		t.Errorf("hits %d cur %d", hits.Load(), c.cur.Load())
	}
}

func TestDoNoRetryOnHTTPError(t *testing.T) {
	var second atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { second.Add(1) }))
	defer good.Close()
	c, _ := Open(Config{URLs: []string{bad.URL, good.URL}})
	resp, err := c.Do(context.Background(), Request{Method: "GET", Path: "/"})
	if err != nil || resp.Status != 500 || second.Load() != 0 {
		t.Errorf("resp %+v err %v second %d", resp, err, second.Load())
	}
}

func TestDoBodyLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", 11)))
	}))
	defer srv.Close()
	c, _ := Open(Config{URLs: []string{srv.URL}, MaxBody: 10})
	_, err := c.Do(context.Background(), Request{Method: "GET", Path: "/"})
	if !errors.Is(err, ErrTooLarge) || err.Error() != "response larger than 50 MB; add size / filter_path" {
		t.Errorf("err = %v", err)
	}
	c, _ = Open(Config{URLs: []string{srv.URL}, MaxBody: 11})
	if _, err := c.Do(context.Background(), Request{Method: "GET", Path: "/"}); err != nil {
		t.Errorf("at the limit: %v", err)
	}
}

func TestDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`ok`)) }))
	defer srv.Close()
	var dialed []string
	c, _ := Open(Config{
		URLs: []string{"http://es.no-such-host.invalid:9200"},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed = append(dialed, addr)
			var d net.Dialer
			return d.DialContext(ctx, network, srv.Listener.Addr().String())
		},
	})
	resp, err := c.Do(context.Background(), Request{Method: "GET", Path: "/"})
	if err != nil || string(resp.Body) != "ok" {
		t.Fatal(resp, err)
	}
	if len(dialed) != 1 || dialed[0] != "es.no-such-host.invalid:9200" {
		t.Errorf("dialed %v", dialed)
	}
}

func TestCheckStatus(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
		text   string
	}{
		{401, `{"error":{"type":"security_exception","reason":"missing authentication credentials"},"status":401}`, ErrAuthFailed, "security_exception: missing authentication credentials"},
		{403, `{"error":{"type":"security_exception","reason":"action denied"},"status":403}`, ErrForbidden, "security_exception: action denied"},
		{404, `{"error":{"root_cause":[{"type":"index_not_found_exception","reason":"no such index [x]"}],"type":"index_not_found_exception","reason":"no such index [x]"},"status":404}`, ErrNotFound, "index_not_found_exception: no such index [x]"},
		{409, `{"error":{"type":"version_conflict_engine_exception","reason":"[1]: version conflict"},"status":409}`, ErrConflict, "version_conflict_engine_exception: [1]: version conflict"},
		{400, `{"error":"no handler found for uri [/_foo] and method [GET]"}`, ErrUnsupported, "no handler found for uri [/_foo] and method [GET]"},
		{404, `{"error":{"root_cause":[{"type":"search_context_missing_exception","reason":"No search context found for id [1]"}],"type":"search_phase_execution_exception","reason":"all shards failed"},"status":404}`, ErrCursorExpired, "search_phase_execution_exception: all shards failed"},
		{404, `{"_index":"a","_id":"1","found":false}`, ErrNotFound, "HTTP 404"},
	}
	for _, tc := range cases {
		err := CheckStatus(Response{Status: tc.status, Body: []byte(tc.body)})
		var ae *APIError
		if !errors.Is(err, tc.want) || !errors.As(err, &ae) || err.Error() != tc.text {
			t.Errorf("%d %s: %v", tc.status, tc.body, err)
		}
	}
	if err := CheckStatus(Response{Status: 201}); err != nil {
		t.Errorf("201: %v", err)
	}
	if err := CheckStatus(Response{Status: 500, Body: []byte(`{"error":{"type":"x","reason":"y"}}`)}); errors.Is(err, ErrNotFound) || err == nil {
		t.Errorf("500: %v", err)
	}
}

func TestGetInfo(t *testing.T) {
	cases := []struct {
		body string
		want Info
		os   bool
	}{
		{`{"name":"n1","cluster_name":"c","cluster_uuid":"u","version":{"number":"7.10.2","build_flavor":"default"},"tagline":"You Know, for Search"}`,
			Info{Name: "n1", ClusterName: "c", UUID: "u", Version: "7.10.2", Distribution: "elasticsearch", Major: 7, Minor: 10}, false},
		{`{"name":"n1","cluster_name":"c","cluster_uuid":"u","version":{"distribution":"opensearch","number":"2.11.0"}}`,
			Info{Name: "n1", ClusterName: "c", UUID: "u", Version: "2.11.0", Distribution: "opensearch", Major: 2, Minor: 11}, true},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tc.body)) }))
		c, _ := Open(Config{URLs: []string{srv.URL}})
		got, err := GetInfo(context.Background(), c)
		srv.Close()
		if err != nil || got != tc.want || got.OpenSearch() != tc.os {
			t.Errorf("got %+v %v", got, err)
		}
	}
}

func TestErrorHasNoPassword(t *testing.T) {
	u := closedURL(t)
	c, err := Open(Config{URLs: []string{strings.Replace(u, "http://", "http://bob:hunter2@", 1)}, User: "elastic", Password: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Do(context.Background(), Request{Method: "GET", Path: "/"})
	if err == nil || strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("err = %v", err)
	}
}
