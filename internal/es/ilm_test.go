package es

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/esx"
)

func TestPolicies(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET _ilm/policy": ok(`{
		"p1":{"version":3,"modified_date":"2026-10-01T00:00:00.000Z","policy":{"phases":{"delete":{"min_age":"30d","actions":{"delete":{}}},"hot":{"actions":{"rollover":{"max_size":"50gb"}}},"warm":{"min_age":"7d","actions":{}}}}}
	}`)}}
	got, err := Policies(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "p1" || !reflect.DeepEqual(got[0].Phases, []string{"hot", "warm", "delete"}) || got[0].InUse != -1 || got[0].System {
		t.Fatalf("7.10: %+v", got)
	}
	if b := string(got[0].Body); !strings.HasPrefix(b, `{"policy":`) || strings.Contains(b, "version") || strings.Contains(b, "modified_date") {
		t.Errorf("body %s", b)
	}
	var body struct {
		Policy struct{ Phases map[string]json.RawMessage } `json:"policy"`
	}
	if json.Unmarshal(got[0].Body, &body) != nil || len(body.Policy.Phases) != 3 {
		t.Errorf("body %s", got[0].Body)
	}

	d = &fakeDoer{table: map[string]Response{"GET _ilm/policy": ok(`{
		"logs":{"version":1,"policy":{"phases":{"hot":{"actions":{}}},"_meta":{"managed":true}},"in_use_by":{"indices":[],"data_streams":[],"composable_templates":["logs"]}},
		"app":{"version":1,"policy":{"phases":{"hot":{"actions":{}},"delete":{"actions":{"delete":{}}}}},"in_use_by":{"indices":["a","b","c"],"data_streams":[]}}
	}`)}}
	got, err = Policies(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "app" || got[0].InUse != 3 || got[0].System || got[1].Name != "logs" || !got[1].System || got[1].InUse != 0 {
		t.Errorf("8.x: %+v", got)
	}
	if _, err := Policies(ctx, &fakeDoer{fn: func(Request) (Response, error) {
		return Response{Status: 400, Body: []byte(`{"error":"no handler found for uri [/_ilm/policy] and method [GET]"}`)}, nil
	}}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("no ILM: %v", err)
	}
}

func TestPutDeletePolicy(t *testing.T) {
	d := &fakeDoer{fn: func(Request) (Response, error) { return ok(`{"acknowledged":true}`), nil }}
	if err := PutPolicy(ctx, d, "p1", json.RawMessage(`{"policy":{}}`)); err != nil {
		t.Fatal(err)
	}
	if err := DeletePolicy(ctx, d, "p1"); err != nil {
		t.Fatal(err)
	}
	r := d.requests()
	if r[0].Method+" "+r[0].Path != "PUT _ilm/policy/p1" || string(r[0].Body) != `{"policy":{}}` || r[1].Method+" "+r[1].Path != "DELETE _ilm/policy/p1" {
		t.Errorf("reqs %+v", r)
	}
}

func TestILMExplain(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET logs-*/_ilm/explain": ok(`{"indices":{
		"logs-1":{"index":"logs-1","managed":true,"policy":"p1","phase":"hot","action":"rollover","step":"check-rollover-ready"},
		"logs-2":{"index":"logs-2","managed":false},
		"logs-3":{"index":"logs-3","managed":true,"policy":"p1","phase":"warm","action":"shrink","step":"ERROR","failed_step":"shrink","step_info":{"type":"illegal_argument_exception","reason":"too many shards"}},
		"logs-4":{"index":"logs-4","managed":true,"policy":"p1","phase":"warm","action":"shrink","step":"ERROR","step_info":{"type":"cluster_block_exception"}}
	}}`)}}
	got, err := ILMExplain(ctx, d, "logs-*")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ILMState{
		"logs-1": {Index: "logs-1", Policy: "p1", Phase: "hot", Action: "rollover", Step: "check-rollover-ready"},
		"logs-2": {Index: "logs-2"},
		"logs-3": {Index: "logs-3", Policy: "p1", Phase: "warm", Action: "shrink", Step: "ERROR", Failed: "too many shards"},
		"logs-4": {Index: "logs-4", Policy: "p1", Phase: "warm", Action: "shrink", Step: "ERROR", Failed: "cluster_block_exception"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestPipelines(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{"GET _ingest/pipeline": ok(`{
		"b-pipe":{"description":"sets b","processors":[{"set":{"field":"b","value":1}},{"lowercase":{"field":"c"}}]},
		"a-pipe":{"processors":[{"set":{"field":"a","value":1}}],"_meta":{"managed":true}}
	}`)}}
	got, err := Pipelines(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "a-pipe" || got[0].Processors != 1 || !got[0].System ||
		got[1].Name != "b-pipe" || got[1].Description != "sets b" || got[1].Processors != 2 || got[1].System {
		t.Errorf("got %+v", got)
	}
	if !sameJSON(got[1].Body, []byte(`{"description":"sets b","processors":[{"set":{"field":"b","value":1}},{"lowercase":{"field":"c"}}]}`)) {
		t.Errorf("body %s", got[1].Body)
	}
	for _, r := range []Response{ok(`{}`), {Status: 404, Body: []byte(`{}`)}} {
		got, err := Pipelines(ctx, &fakeDoer{table: map[string]Response{"GET _ingest/pipeline": r}})
		if err != nil || len(got) != 0 {
			t.Errorf("empty: %v %v", got, err)
		}
	}
}

func TestPutDeletePipeline(t *testing.T) {
	d := &fakeDoer{fn: func(Request) (Response, error) { return ok(`{"acknowledged":true}`), nil }}
	if err := PutPipeline(ctx, d, "p1", json.RawMessage(`{"processors":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := DeletePipeline(ctx, d, "p1"); err != nil {
		t.Fatal(err)
	}
	r := d.requests()
	if r[0].Method+" "+r[0].Path != "PUT _ingest/pipeline/p1" || r[1].Method+" "+r[1].Path != "DELETE _ingest/pipeline/p1" {
		t.Errorf("reqs %+v", r)
	}
}

func TestSimulatePipeline(t *testing.T) {
	d := &fakeDoer{fn: func(Request) (Response, error) { return ok(`{"docs":[{"processor_results":[]}]}`), nil }}
	out, err := SimulatePipeline(ctx, d, "p1", []esx.Hit{{Index: "orders", ID: "1", Source: json.RawMessage(`{"a":1}`)}})
	if err != nil || string(out) != `{"docs":[{"processor_results":[]}]}` {
		t.Fatalf("out %s %v", out, err)
	}
	r := d.requests()[0]
	if r.Method != "POST" || r.Path != "_ingest/pipeline/p1/_simulate?verbose=true" ||
		!sameJSON(r.Body, []byte(`{"docs":[{"_index":"orders","_id":"1","_source":{"a":1}}]}`)) {
		t.Errorf("req %s %s %s", r.Method, r.Path, r.Body)
	}
}
