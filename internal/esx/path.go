package esx

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// member is one key of a JSON object, in document order.
type member struct {
	Key string
	Val json.RawMessage
}

// object is a JSON object that keeps its key order.
type object []member

var errNotObject = errors.New("not a JSON object")

// decodeObject reads a JSON object keeping its key order.
func decodeObject(b []byte) (object, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errNotObject
	}
	obj := object{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		obj = append(obj, member{key, v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return obj, nil
}

func (o object) get(key string) (json.RawMessage, bool) {
	for _, m := range o {
		if m.Key == key {
			return m.Val, true
		}
	}
	return nil, false
}

func (o object) encode() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(jsonString(m.Key))
		b.WriteByte(':')
		b.Write(m.Val)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func kindOf(v json.RawMessage) byte {
	v = bytes.TrimSpace(v)
	if len(v) == 0 {
		return 0
	}
	return v[0]
}

// GetPath reads a dotted field path from a _source object. A key with
// literal dots ({"a.b":1}) and nested objects ({"a":{"b":1}}) both match;
// the longest literal key wins. Through an array of objects it collects
// the elements' values into an array. Arrays are not indexed: "a.0" does
// not exist.
func GetPath(src json.RawMessage, path string) (json.RawMessage, bool) {
	obj, err := decodeObject(src)
	if err != nil {
		return nil, false
	}
	segs := strings.Split(path, ".")
	for n := len(segs); n >= 1; n-- {
		v, ok := obj.get(strings.Join(segs[:n], "."))
		if !ok {
			continue
		}
		if n == len(segs) {
			return v, true
		}
		rest := strings.Join(segs[n:], ".")
		switch kindOf(v) {
		case '{':
			if got, ok := GetPath(v, rest); ok {
				return got, true
			}
		case '[':
			if got, ok := getEach(v, rest); ok {
				return got, true
			}
		}
	}
	return nil, false
}

func getEach(arr json.RawMessage, path string) (json.RawMessage, bool) {
	var elems []json.RawMessage
	if json.Unmarshal(arr, &elems) != nil {
		return nil, false
	}
	var out []json.RawMessage
	for _, e := range elems {
		if kindOf(e) != '{' {
			continue
		}
		if v, ok := GetPath(e, path); ok {
			out = append(out, v)
		}
	}
	if out == nil {
		return nil, false
	}
	b, _ := json.Marshal(out)
	return b, true
}

// CellText is how a _source value shows in a table cell: "" for a
// missing field, null reported apart, strings unquoted, other values as
// compact JSON.
func CellText(v json.RawMessage, ok bool) (text string, null bool) {
	if !ok {
		return "", false
	}
	v = bytes.TrimSpace(v)
	switch kindOf(v) {
	case 'n':
		return "", true
	case '"':
		var s string
		if json.Unmarshal(v, &s) == nil {
			return s, false
		}
	}
	var b bytes.Buffer
	if json.Compact(&b, v) != nil {
		return string(v), false
	}
	return b.String(), false
}
