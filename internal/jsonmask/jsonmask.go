// Package jsonmask hides the values at paths of a JSON document; the
// Kafka and Elasticsearch masking share it.
package jsonmask

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Masked is the text a masked field shows; in JSON it is the string "******".
const Masked = "******"

var maskedJSON = []byte(`"` + Masked + `"`)

// Paths replaces the values at paths in the valid JSON raw with the
// string Masked, keeping the order of keys and every other value as
// written. A path's segments are a key, key[] (each element of its
// array), [] (each element of an array) or * (any key); an empty path is
// all of raw.
func Paths(raw []byte, paths [][]string) ([]byte, bool) {
	for _, p := range paths {
		if len(p) == 0 {
			return maskedJSON, true
		}
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return raw, false
	}
	switch raw[0] {
	case '{':
		return maskObject(raw, paths)
	case '[':
		var rest [][]string
		for _, p := range paths {
			if p[0] == "[]" {
				rest = append(rest, p[1:])
			}
		}
		return maskArray(raw, rest)
	}
	return raw, false
}

func maskObject(raw []byte, paths [][]string) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil { // {
		return raw, false
	}
	var out bytes.Buffer
	out.WriteByte('{')
	changed := false
	for first := true; dec.More(); first = false {
		tok, err := dec.Token()
		k, ok := tok.(string)
		if err != nil || !ok {
			return raw, false
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return raw, false
		}
		var sub [][]string
		for _, p := range paths {
			name, arr := strings.CutSuffix(p[0], "[]")
			if name != k && name != "*" {
				continue
			}
			if arr {
				sub = append(sub, append([]string{"[]"}, p[1:]...))
			} else {
				sub = append(sub, p[1:])
			}
		}
		val := []byte(v)
		if len(sub) > 0 {
			var ch bool
			if val, ch = Paths(val, sub); ch {
				changed = true
			}
		}
		if !first {
			out.WriteByte(',')
		}
		out.Write(jsonString(k))
		out.WriteByte(':')
		out.Write(val)
	}
	out.WriteByte('}')
	if !changed {
		return raw, false
	}
	return out.Bytes(), true
}

func maskArray(raw []byte, paths [][]string) ([]byte, bool) {
	if len(paths) == 0 {
		return raw, false
	}
	var elems []json.RawMessage
	if json.Unmarshal(raw, &elems) != nil {
		return raw, false
	}
	var out bytes.Buffer
	out.WriteByte('[')
	changed := false
	for i, e := range elems {
		val, ch := Paths(e, paths)
		changed = changed || ch
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(val)
	}
	out.WriteByte(']')
	if !changed {
		return raw, false
	}
	return out.Bytes(), true
}

// jsonString is s as a JSON string, without HTML escapes.
func jsonString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
