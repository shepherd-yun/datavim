package kmsgx

import (
	"bytes"
	"encoding/json"
	"path"
	"slices"
	"strings"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/jsonmask"
	"github.com/shepherd-yun/datavim/internal/kafka"
)

// Masked is the text a masked field shows; in JSON it is the string "******".
const Masked = jsonmask.Masked

var maskedJSON = []byte(`"` + Masked + `"`)

// maskPart is what the rules hide in a key or a value: all of it, or the
// fields at paths (segments: a key, key[] for each element of its array,
// [] for each element of an array, * for any key).
type maskPart struct {
	whole bool
	paths [][]string
}

func (p maskPart) any() bool { return p.whole || len(p.paths) > 0 }

// Masker applies the mask rules of one connection and topic. A nil Masker
// masks nothing.
type Masker struct {
	key, value maskPart
	headers    []string // lower-cased globs
}

// NewMasker keeps the rules that match conn, env and topic; it is nil when
// none does.
func NewMasker(rules []config.MaskRule, conn, env, topic string) *Masker {
	var m Masker
	found := false
	for _, r := range rules {
		if r.ForES() {
			continue
		}
		if !slices.Contains(r.Connections, conn) && (r.Env == "" || r.Env != env) {
			continue
		}
		if len(r.Topics) > 0 && !slices.ContainsFunc(r.Topics, func(g string) bool { ok, _ := path.Match(g, topic); return ok }) {
			continue
		}
		found = true
		for _, f := range r.Fields {
			name, rest, dotted := strings.Cut(f, ".")
			part := &m.value
			if name == "key" {
				part = &m.key
			}
			if !dotted {
				part.whole = true
			} else {
				part.paths = append(part.paths, strings.Split(rest, "."))
			}
		}
		for _, h := range r.Headers {
			m.headers = append(m.headers, strings.ToLower(h))
		}
	}
	if !found {
		return nil
	}
	return &m
}

// Covers reports whether a rule hides something of the key (or value):
// all of it or a path in it.
func (m *Masker) Covers(key bool) bool {
	if m == nil {
		return false
	}
	if key {
		return m.key.any()
	}
	return m.value.any()
}

// Pending reports whether a part needs its decoded JSON before Mask can
// show it (a path rule, on a part in the wire format): show it as Masked
// until then.
func (m *Masker) Pending(key bool) bool {
	if m == nil {
		return false
	}
	if key {
		return len(m.key.paths) > 0
	}
	return len(m.value.paths) > 0
}

// Mask returns r with its masked parts replaced and whether anything was
// masked. key and value are the JSON to mask the paths in (the decoded
// Schema Registry JSON), or nil to use r.Key / r.Value. A part a path rule
// covers that is not JSON becomes Masked as a whole. A masked part comes
// back as JSON bytes; a null key or value stays null. r is not changed.
func (m *Masker) Mask(r kafka.Record, key, value []byte) (out kafka.Record, masked bool) {
	if m == nil {
		return r, false
	}
	out = r
	var km, vm bool
	out.Key, km = m.key.mask(r.Key, key, r.KeyNull)
	out.Value, vm = m.value.mask(r.Value, value, r.ValueNull)
	masked = km || vm
	if len(m.headers) > 0 && len(r.Headers) > 0 {
		out.Headers = slices.Clone(r.Headers)
		for i, h := range out.Headers {
			name := strings.ToLower(h.Key)
			if slices.ContainsFunc(m.headers, func(g string) bool { ok, _ := path.Match(g, name); return ok }) {
				out.Headers[i].Value, masked = []byte(Masked), true
			}
		}
	}
	return out, masked
}

func (p maskPart) mask(raw, decoded []byte, null bool) ([]byte, bool) {
	if null || !p.any() {
		return raw, false
	}
	if p.whole {
		return maskedJSON, true
	}
	src := raw
	if decoded != nil {
		src = decoded
	}
	if !json.Valid(src) {
		return maskedJSON, true // fail closed: the fields can't be found
	}
	out, changed := jsonmask.Paths(src, p.paths)
	if !changed {
		return raw, false
	}
	var b bytes.Buffer
	if json.Compact(&b, out) != nil {
		return maskedJSON, true
	}
	return b.Bytes(), true
}

// MaskJSON masks the decoded JSON of a key or a value (Schema Registry
// messages decoded after they arrived): js unchanged when nothing applies.
func (m *Masker) MaskJSON(key bool, js []byte) ([]byte, bool) {
	if m == nil {
		return js, false
	}
	if key {
		return m.key.mask(js, nil, false)
	}
	return m.value.mask(js, nil, false)
}
