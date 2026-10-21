package esx

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
)

var esMaskRules = []config.MaskRule{
	{Connections: []string{"search"}, Indices: []string{"users*"}, Fields: []string{"_source.password", "_source.cards[].no"}},
	{Connections: []string{"other"}, Fields: []string{"_source.secret"}},
	{Connections: []string{"search"}, Fields: []string{"value.x"}}, // a Kafka rule
}

func TestESMasker(t *testing.T) {
	if NewMasker(esMaskRules, "nothing", "dev") != nil {
		t.Error("no rule: want nil")
	}
	m := NewMasker(esMaskRules, "search", "dev")
	h, masked := m.MaskHit(Hit{Index: "users-1", ID: "1", Source: json.RawMessage(`{"name":"a","password":"pw","cards":[{"no":"4111","exp":"1"}]}`)})
	if !masked || string(h.Source) != `{"name":"a","password":"******","cards":[{"no":"******","exp":"1"}]}` {
		t.Errorf("users-1: %s %v", h.Source, masked)
	}
	src := json.RawMessage(`{"password":"pw"}`)
	if h, masked := m.MaskHit(Hit{Index: "orders", Source: src}); masked || string(h.Source) != string(src) {
		t.Errorf("orders: %s", h.Source)
	}
	if h, masked := m.MaskHit(Hit{Index: "users-1", Source: json.RawMessage(`{"name":"a"}`)}); masked || string(h.Source) != `{"name":"a"}` {
		t.Errorf("nothing to hide: %s %v", h.Source, masked)
	}
	h, masked = m.MaskHit(Hit{Index: "users-2", Truncated: true, Source: json.RawMessage(`{"password":"pw","na`)})
	if !masked || string(h.Source) != `"******"` {
		t.Errorf("truncated: %s", h.Source)
	}
	all := NewMasker(esMaskRules, "other", "dev")
	if h, masked := all.MaskHit(Hit{Index: "anything", Source: json.RawMessage(`{"secret":1}`)}); !masked || string(h.Source) != `{"secret":"******"}` {
		t.Errorf("no indices: %s", h.Source)
	}
	for _, tc := range []struct {
		index, field string
		want         bool
	}{
		{"users-1", "cards.no", true},
		{"users-1", "password", true},
		{"users-1", "cards", true}, // holds a masked field
		{"users-1", "cards.exp", false},
		{"users-1", "name", false},
		{"orders", "password", false},
	} {
		if got := m.Covers(tc.index, tc.field); got != tc.want {
			t.Errorf("Covers(%s, %s) = %v", tc.index, tc.field, got)
		}
	}
	var nilM *Masker
	if nilM.Covers("users-1", "password") {
		t.Error("nil covers")
	}
	if h, masked := nilM.MaskHit(Hit{Source: src}); masked || string(h.Source) != string(src) {
		t.Error("nil masks")
	}
}

func TestMaskResponse(t *testing.T) {
	m := NewMasker(esMaskRules, "search", "dev")
	for _, tc := range []struct{ name, in, want string }{
		{
			"search",
			`{"took":1,"hits":{"total":{"value":2},"hits":[{"_index":"users-1","_id":"1","_source":{"password":"pw","n":1}},{"_index":"orders","_id":"2","_source":{"password":"keep"}}]},"aggregations":{"x":{"buckets":[{"key":"pw"}]}}}`,
			`{"took":1,"hits":{"total":{"value":2},"hits":[{"_index":"users-1","_id":"1","_source":{"password":"******","n":1}},{"_index":"orders","_id":"2","_source":{"password":"keep"}}]},"aggregations":{"x":{"buckets":[{"key":"pw"}]}}}`,
		},
		{
			"mget",
			`{"docs":[{"_index":"users-1","_id":"1","found":true,"_source":{"password":"pw"}},{"_index":"users-1","_id":"9","found":false}]}`,
			`{"docs":[{"_index":"users-1","_id":"1","found":true,"_source":{"password":"******"}},{"_index":"users-1","_id":"9","found":false}]}`,
		},
		{
			"get doc",
			`{"_index":"users-1","_id":"1","_version":1,"_source":{"password":"pw"}}`,
			`{"_index":"users-1","_id":"1","_version":1,"_source":{"password":"******"}}`,
		},
	} {
		got, masked := m.MaskResponse([]byte(tc.in))
		if !masked || string(got) != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
	for _, in := range []string{`[{"index":"users-1"}]`, `{"acknowledged":true}`, `not json`} {
		if got, masked := m.MaskResponse([]byte(in)); masked || string(got) != in {
			t.Errorf("%s: %s %v", in, got, masked)
		}
	}
}

func TestPartialDoc(t *testing.T) {
	got, err := PartialDoc(json.RawMessage(`{"a":{"b":1,"c":2},"d":3}`), []string{"a.b"})
	if err != nil || string(got) != `{"a":{"b":1}}` {
		t.Errorf("got %s %v", got, err)
	}
	got, err = PartialDoc(json.RawMessage(`{"a":{"b":1,"c":2},"d":3}`), []string{"d", "a.c"})
	if err != nil || string(got) != `{"d":3,"a":{"c":2}}` {
		t.Errorf("got %s %v", got, err)
	}
	if _, err := PartialDoc(json.RawMessage(`{"a":1}`), []string{"x"}); err == nil || !strings.Contains(err.Error(), "x") {
		t.Errorf("missing: %v", err)
	}
}
