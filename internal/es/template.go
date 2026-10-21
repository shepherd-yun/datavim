package es

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
)

// TemplateKind is one of the three kinds of index template.
type TemplateKind int

const (
	TemplateComposable TemplateKind = iota // _index_template
	TemplateLegacy                         // _template
	TemplateComponent                      // _component_template
)

func (k TemplateKind) String() string {
	return [...]string{"composable", "legacy", "component"}[k]
}

func (k TemplateKind) api() string {
	return [...]string{"_index_template", "_template", "_component_template"}[k]
}

// Template is one index template.
type Template struct {
	Kind     TemplateKind
	Name     string
	Patterns []string // none for a component template
	Priority int      // a composable template's priority, a legacy one's order
	// Body is the definition as PUT takes it back.
	Body json.RawMessage
}

// missingAPI reports an endpoint the cluster does not have.
func missingAPI(err error) bool {
	var e *APIError
	return errors.Is(err, ErrUnsupported) || errors.Is(err, ErrNotFound) ||
		errors.As(err, &e) && e.Status == 400 && e.hasType("invalid_index_name_exception")
}

// Templates lists every index template, by kind then name. A kind the
// cluster does not support is skipped; none supported is ErrUnsupported.
func Templates(ctx context.Context, d Doer) ([]Template, error) {
	var out []Template
	supported := 0
	for _, k := range []TemplateKind{TemplateComposable, TemplateLegacy, TemplateComponent} {
		resp, err := do(ctx, d, Request{Method: "GET", Path: k.api()})
		if missingAPI(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		supported++
		ts, err := parseTemplates(k, resp.Body)
		if err != nil {
			return nil, err
		}
		out = append(out, ts...)
	}
	if supported == 0 {
		return nil, ErrUnsupported
	}
	slices.SortFunc(out, func(a, b Template) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), strings.Compare(a.Name, b.Name))
	})
	return out, nil
}

type templateHead struct {
	Patterns json.RawMessage `json:"index_patterns"`
	Priority int             `json:"priority"`
	Order    int             `json:"order"`
}

func parseTemplates(k TemplateKind, body []byte) ([]Template, error) {
	var out []Template
	add := func(name string, raw json.RawMessage) error {
		t := Template{Kind: k, Name: name, Body: raw}
		if k != TemplateComponent {
			var h templateHead
			if err := json.Unmarshal(raw, &h); err != nil {
				return err
			}
			t.Patterns = stringOrList(h.Patterns)
			t.Priority = h.Priority
			if k == TemplateLegacy {
				t.Priority = h.Order
			}
		}
		out = append(out, t)
		return nil
	}
	switch k {
	case TemplateLegacy:
		var r map[string]json.RawMessage
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		for name, raw := range r {
			if err := add(name, raw); err != nil {
				return nil, err
			}
		}
	default:
		var r struct {
			Index []struct {
				Name string          `json:"name"`
				Body json.RawMessage `json:"index_template"`
			} `json:"index_templates"`
			Component []struct {
				Name string          `json:"name"`
				Body json.RawMessage `json:"component_template"`
			} `json:"component_templates"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		for _, t := range r.Index {
			if err := add(t.Name, t.Body); err != nil {
				return nil, err
			}
		}
		for _, t := range r.Component {
			if err := add(t.Name, t.Body); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func stringOrList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}
	}
	var l []string
	_ = json.Unmarshal(raw, &l)
	return l
}

// TemplatePath is the REST path of a template.
func TemplatePath(kind TemplateKind, name string) string {
	return kind.api() + "/" + url.PathEscape(name)
}

// PutTemplate creates or replaces a template.
func PutTemplate(ctx context.Context, d Doer, kind TemplateKind, name string, body json.RawMessage) error {
	_, err := do(ctx, d, Request{Method: "PUT", Path: TemplatePath(kind, name), Body: body})
	return err
}

// DeleteTemplate deletes a template.
func DeleteTemplate(ctx context.Context, d Doer, kind TemplateKind, name string) error {
	_, err := do(ctx, d, Request{Method: "DELETE", Path: TemplatePath(kind, name)})
	return err
}
