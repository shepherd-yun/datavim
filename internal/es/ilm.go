package es

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/shepherd-yun/datavim/internal/esx"
)

// systemObject reports a template, ILM policy or pipeline the cluster
// owns: a name starting with "." or a body with _meta.managed set.
func systemObject(name string, body json.RawMessage) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	var b struct {
		Meta struct {
			Managed bool `json:"managed"`
		} `json:"_meta"`
	}
	return json.Unmarshal(body, &b) == nil && b.Meta.Managed
}

// Policy is an ILM policy.
type Policy struct {
	Name   string
	Phases []string // in the order ILM runs them
	InUse  int      // indices using it; -1 when the cluster does not say (7.10)
	System bool
	// Body is what PUT takes back: {"policy": …}.
	Body json.RawMessage
}

var ilmPhases = []string{"hot", "warm", "cold", "frozen", "delete"}

// Policies lists the ILM policies by name.
func Policies(ctx context.Context, d Doer) ([]Policy, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_ilm/policy"})
	if missingAPI(err) {
		return nil, ErrUnsupported
	}
	if err != nil {
		return nil, err
	}
	var r map[string]struct {
		Policy  json.RawMessage `json:"policy"`
		InUseBy *struct {
			Indices []string `json:"indices"`
		} `json:"in_use_by"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	out := []Policy{}
	for name, v := range r {
		var head struct {
			Phases map[string]json.RawMessage `json:"phases"`
		}
		_ = json.Unmarshal(v.Policy, &head)
		p := Policy{Name: name, InUse: -1, System: systemObject(name, v.Policy)}
		for _, ph := range ilmPhases {
			if _, ok := head.Phases[ph]; ok {
				p.Phases = append(p.Phases, ph)
			}
		}
		if v.InUseBy != nil {
			p.InUse = len(v.InUseBy.Indices)
		}
		p.Body, _ = json.Marshal(map[string]json.RawMessage{"policy": v.Policy})
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Policy) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// PutPolicy creates or replaces an ILM policy.
func PutPolicy(ctx context.Context, d Doer, name string, body json.RawMessage) error {
	_, err := do(ctx, d, Request{Method: "PUT", Path: "_ilm/policy/" + url.PathEscape(name), Body: body})
	return err
}

// DeletePolicy deletes an ILM policy.
func DeletePolicy(ctx context.Context, d Doer, name string) error {
	_, err := do(ctx, d, Request{Method: "DELETE", Path: "_ilm/policy/" + url.PathEscape(name)})
	return err
}

// ILMState is where an index is in its ILM policy; Policy is empty for an
// index ILM does not manage.
type ILMState struct {
	Index, Policy, Phase, Action, Step string
	Failed                             string // why the step failed (Step "ERROR")
}

// ILMExplain reads the ILM state of target's indices.
func ILMExplain(ctx context.Context, d Doer, target string) (map[string]ILMState, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: indexPath(target) + "/_ilm/explain"})
	if missingAPI(err) {
		return nil, ErrUnsupported
	}
	if err != nil {
		return nil, err
	}
	var r struct {
		Indices map[string]struct {
			Index, Policy, Phase, Action, Step string
			StepInfo                           struct{ Type, Reason string } `json:"step_info"`
		} `json:"indices"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	out := make(map[string]ILMState, len(r.Indices))
	for name, v := range r.Indices {
		s := ILMState{Index: name, Policy: v.Policy, Phase: v.Phase, Action: v.Action, Step: v.Step}
		if v.Step == "ERROR" {
			s.Failed = cmp.Or(v.StepInfo.Reason, v.StepInfo.Type)
		}
		out[name] = s
	}
	return out, nil
}

// Pipeline is an ingest pipeline.
type Pipeline struct {
	Name, Description string
	Processors        int
	System            bool
	// Body is the definition, which is what PUT takes back.
	Body json.RawMessage
}

// Pipelines lists the ingest pipelines by name.
func Pipelines(ctx context.Context, d Doer) ([]Pipeline, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_ingest/pipeline"})
	if errors.Is(err, ErrNotFound) {
		return []Pipeline{}, nil // 7.x answers 404 when there is none
	}
	if err != nil {
		return nil, err
	}
	var r map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	out := []Pipeline{}
	for name, raw := range r {
		var head struct {
			Description string            `json:"description"`
			Processors  []json.RawMessage `json:"processors"`
		}
		_ = json.Unmarshal(raw, &head)
		out = append(out, Pipeline{
			Name: name, Description: head.Description, Processors: len(head.Processors),
			System: systemObject(name, raw), Body: raw,
		})
	}
	slices.SortFunc(out, func(a, b Pipeline) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// PutPipeline creates or replaces an ingest pipeline.
func PutPipeline(ctx context.Context, d Doer, name string, body json.RawMessage) error {
	_, err := do(ctx, d, Request{Method: "PUT", Path: "_ingest/pipeline/" + url.PathEscape(name), Body: body})
	return err
}

// DeletePipeline deletes an ingest pipeline.
func DeletePipeline(ctx context.Context, d Doer, name string) error {
	_, err := do(ctx, d, Request{Method: "DELETE", Path: "_ingest/pipeline/" + url.PathEscape(name)})
	return err
}

// SimulatePipeline runs a pipeline on docs without indexing them and
// returns the verbose response as it is.
func SimulatePipeline(ctx context.Context, d Doer, name string, docs []esx.Hit) (json.RawMessage, error) {
	type doc struct {
		Index  string          `json:"_index,omitempty"`
		ID     string          `json:"_id,omitempty"`
		Source json.RawMessage `json:"_source"`
	}
	body := struct {
		Docs []doc `json:"docs"`
	}{}
	for _, h := range docs {
		body.Docs = append(body.Docs, doc{h.Index, h.ID, h.Source})
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp, err := do(ctx, d, Request{Method: "POST", Path: "_ingest/pipeline/" + url.PathEscape(name) + "/_simulate?verbose=true", Body: b})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
