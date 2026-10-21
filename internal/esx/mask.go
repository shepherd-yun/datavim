package esx

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/jsonmask"
)

var maskedJSON = json.RawMessage(`"` + jsonmask.Masked + `"`)

type maskRule struct {
	indices []string   // globs; empty: every index
	paths   [][]string // in _source
}

// Masker applies the Elasticsearch mask rules of one connection (spec
// §8.4). A nil Masker masks nothing.
type Masker struct{ rules []maskRule }

// NewMasker keeps the Elasticsearch rules that match conn and env; it is
// nil when none does.
func NewMasker(rules []config.MaskRule, conn, env string) *Masker {
	var m Masker
	for _, r := range rules {
		if !r.ForES() || !slices.Contains(r.Connections, conn) && (r.Env == "" || r.Env != env) {
			continue
		}
		mr := maskRule{indices: r.Indices}
		for _, f := range r.Fields {
			if rest, ok := strings.CutPrefix(f, "_source."); ok {
				mr.paths = append(mr.paths, strings.Split(rest, "."))
			}
		}
		m.rules = append(m.rules, mr)
	}
	if len(m.rules) == 0 {
		return nil
	}
	return &m
}

func (m *Masker) paths(index string) [][]string {
	if m == nil {
		return nil
	}
	var out [][]string
	for _, r := range m.rules {
		if len(r.indices) == 0 || slices.ContainsFunc(r.indices, func(g string) bool { ok, _ := path.Match(g, index); return ok }) {
			out = append(out, r.paths...)
		}
	}
	return out
}

// MaskHit returns h with its masked fields replaced and whether any was. A
// truncated _source is masked as a whole: its fields can't be found.
func (m *Masker) MaskHit(h Hit) (Hit, bool) {
	ps := m.paths(h.Index)
	if len(ps) == 0 {
		return h, false
	}
	if h.Truncated || !json.Valid(h.Source) {
		h.Source = maskedJSON
		return h, true
	}
	out, changed := jsonmask.Paths(h.Source, ps)
	if !changed {
		return h, false
	}
	h.Source = out
	return h, true
}

// Covers reports whether a field (a dotted path) of an index's documents
// is masked, or holds a masked field: editing it would write the mask back.
func (m *Masker) Covers(index, field string) bool {
	segs := strings.Split(field, ".")
	for _, p := range m.paths(index) {
		n := min(len(p), len(segs))
		match := true
		for i := range n {
			s := strings.TrimSuffix(p[i], "[]")
			if s == "" {
				continue // [] steps into an array: no segment in a dotted field
			}
			if s != "*" && s != segs[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// MaskResponse masks the documents of a console response: the hits of a
// search, the docs of an _mget and a GET of one document. Other responses
// come back as they are.
func (m *Masker) MaskResponse(body []byte) ([]byte, bool) {
	if m == nil {
		return body, false
	}
	obj, err := decodeObject(body)
	if err != nil {
		return body, false
	}
	changed := false
	if _, ok := obj.get("_source"); ok {
		if out, ch := m.maskDoc(body); ch {
			return out, true
		}
		return body, false
	}
	for i, mem := range obj {
		switch mem.Key {
		case "hits":
			hits, err := decodeObject(mem.Val)
			if err != nil {
				continue
			}
			for j, h := range hits {
				if h.Key == "hits" {
					if out, ch := m.maskDocs(h.Val); ch {
						hits[j].Val, changed = out, true
					}
				}
			}
			obj[i].Val = hits.encode()
		case "docs":
			if out, ch := m.maskDocs(mem.Val); ch {
				obj[i].Val, changed = out, true
			}
		}
	}
	if !changed {
		return body, false
	}
	return obj.encode(), true
}

func (m *Masker) maskDocs(arr json.RawMessage) (json.RawMessage, bool) {
	var docs []json.RawMessage
	if json.Unmarshal(arr, &docs) != nil {
		return arr, false
	}
	changed := false
	for i, d := range docs {
		if out, ch := m.maskDoc(d); ch {
			docs[i], changed = out, true
		}
	}
	if !changed {
		return arr, false
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, d := range docs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(d)
	}
	b.WriteByte(']')
	return json.RawMessage(b.String()), true
}

func (m *Masker) maskDoc(doc json.RawMessage) (json.RawMessage, bool) {
	obj, err := decodeObject(doc)
	if err != nil {
		return doc, false
	}
	var index string
	if v, ok := obj.get("_index"); ok {
		_ = json.Unmarshal(v, &index)
	}
	for i, mem := range obj {
		if mem.Key != "_source" {
			continue
		}
		h, ch := m.MaskHit(Hit{Index: index, Source: mem.Val})
		if !ch {
			return doc, false
		}
		obj[i].Val = h.Source
		return obj.encode(), true
	}
	return doc, false
}

// PartialDoc is the fields (dotted paths) of src, nested as an _update's
// "doc" wants them.
func PartialDoc(src json.RawMessage, fields []string) (json.RawMessage, error) {
	out := json.RawMessage(`{}`)
	for _, f := range fields {
		v, ok := GetPath(src, f)
		if !ok {
			return nil, fmt.Errorf("field %s is not in the document", f)
		}
		var err error
		if out, err = SetPath(out, f, v); err != nil {
			return nil, err
		}
	}
	return out, nil
}
