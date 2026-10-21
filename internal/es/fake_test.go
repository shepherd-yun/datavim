package es

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
)

// fakeDoer answers "METHOD path" (path with its query string, no leading
// "/") from a table, or through fn when set; it records every request.
type fakeDoer struct {
	mu    sync.Mutex
	table map[string]Response
	fn    func(r Request) (Response, error)
	reqs  []Request
}

func (f *fakeDoer) Do(_ context.Context, r Request) (Response, error) {
	f.mu.Lock()
	r.Path = strings.TrimLeft(r.Path, "/")
	f.reqs = append(f.reqs, r)
	f.mu.Unlock()
	if f.fn != nil {
		return f.fn(r)
	}
	if resp, ok := f.table[r.Method+" "+r.Path]; ok {
		return resp, nil
	}
	return Response{Status: 404, Body: []byte(`{"error":{"type":"fake","reason":"no route ` + r.Method + " " + r.Path + `"},"status":404}`)}, nil
}

func (f *fakeDoer) requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.reqs...)
}

func ok(body string) Response { return Response{Status: 200, Body: []byte(body)} }

func fixture(t *testing.T, name string) Response {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return Response{Status: 200, Body: b}
}
