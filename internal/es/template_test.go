package es

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

const (
	composableBody = `{"index_patterns":["logs-*"],"template":{"settings":{"number_of_shards":"1"}},"priority":100,"composed_of":["base"]}`
	legacyBody     = `{"order":3,"index_patterns":["old-*","older-*"],"settings":{},"mappings":{},"aliases":{}}`
	componentBody  = `{"template":{"mappings":{"properties":{"a":{"type":"keyword"}}}}}`
)

func templateDoer(component Response) *fakeDoer {
	return &fakeDoer{table: map[string]Response{
		"GET _index_template":     ok(`{"index_templates":[{"name":"logs","index_template":` + composableBody + `}]}`),
		"GET _template":           ok(`{"old":` + legacyBody + `}`),
		"GET _component_template": component,
	}}
}

func sameJSON(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

func TestTemplates(t *testing.T) {
	d := templateDoer(ok(`{"component_templates":[{"name":"base","component_template":` + componentBody + `}]}`))
	got, err := Templates(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("templates = %+v", got)
	}
	c, l, p := got[0], got[1], got[2]
	if c.Kind != TemplateComposable || c.Name != "logs" || !reflect.DeepEqual(c.Patterns, []string{"logs-*"}) || c.Priority != 100 || !sameJSON(c.Body, []byte(composableBody)) {
		t.Errorf("composable = %+v", c)
	}
	if l.Kind != TemplateLegacy || l.Name != "old" || !reflect.DeepEqual(l.Patterns, []string{"old-*", "older-*"}) || l.Priority != 3 || !sameJSON(l.Body, []byte(legacyBody)) {
		t.Errorf("legacy = %+v", l)
	}
	if p.Kind != TemplateComponent || p.Name != "base" || p.Patterns != nil || !sameJSON(p.Body, []byte(componentBody)) {
		t.Errorf("component = %+v", p)
	}
	if TemplateComposable.String() != "composable" || TemplateLegacy.String() != "legacy" || TemplateComponent.String() != "component" {
		t.Error("kind names")
	}
}

func TestTemplatesSkipsUnsupported(t *testing.T) {
	d := templateDoer(Response{Status: 400, Body: []byte(`{"error":"no handler found for uri [/_component_template] and method [GET]"}`)})
	got, err := Templates(ctx, d)
	if err != nil || len(got) != 2 {
		t.Errorf("templates = %+v, %v", got, err)
	}
	d = &fakeDoer{} // every route 404
	if _, err := Templates(ctx, d); !errors.Is(err, ErrUnsupported) {
		t.Errorf("none supported: %v", err)
	}
	d = templateDoer(ok(`{"component_templates":[]}`))
	d.table["GET _template"] = Response{Status: 500, Body: []byte(`{"error":{"type":"x","reason":"boom"}}`)}
	if _, err := Templates(ctx, d); err == nil {
		t.Error("500 ignored")
	}
}

func TestPutDeleteTemplate(t *testing.T) {
	for kind, p := range map[TemplateKind]string{TemplateComposable: "_index_template/a%2Fb", TemplateLegacy: "_template/a%2Fb", TemplateComponent: "_component_template/a%2Fb"} {
		d := &fakeDoer{fn: func(Request) (Response, error) { return ok(`{}`), nil }}
		if err := PutTemplate(ctx, d, kind, "a/b", json.RawMessage(`{"x":1}`)); err != nil {
			t.Fatal(err)
		}
		if err := DeleteTemplate(ctx, d, kind, "a/b"); err != nil {
			t.Fatal(err)
		}
		r := d.requests()
		if r[0].Method != "PUT" || r[0].Path != p || string(r[0].Body) != `{"x":1}` || r[1].Method != "DELETE" || r[1].Path != p {
			t.Errorf("%v: %+v", kind, r)
		}
	}
}
