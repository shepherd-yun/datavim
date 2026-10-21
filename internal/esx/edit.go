package esx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var numberTypes = map[string]bool{
	"long": true, "integer": true, "short": true, "byte": true, "unsigned_long": true,
	"double": true, "float": true, "half_float": true, "scaled_float": true,
}

// ParseCell turns a cell's typed input into a JSON value for a field of
// fieldType (spec §5.6): "null" is null, numbers and booleans by type,
// objects, nested fields and mixed types ("keyword|long") JSON, unmapped
// fields JSON when it parses, everything else a string.
func ParseCell(fieldType, input string) (json.RawMessage, error) {
	in := strings.TrimSpace(input)
	if in == "null" {
		return json.RawMessage("null"), nil
	}
	switch {
	case numberTypes[fieldType]:
		if _, err := strconv.ParseFloat(in, 64); err != nil || !json.Valid([]byte(in)) {
			return nil, fmt.Errorf("%s field: want a number, got %q", fieldType, in)
		}
		return json.RawMessage(in), nil
	case fieldType == "boolean":
		if in != "true" && in != "false" {
			return nil, fmt.Errorf("boolean field: want true or false, got %q", in)
		}
		return json.RawMessage(in), nil
	case fieldType == "object" || fieldType == "nested" || strings.Contains(fieldType, "|"):
		if !json.Valid([]byte(in)) {
			return nil, fmt.Errorf("%s field: want JSON (quote strings), got %q", fieldType, in)
		}
		return compactJSON(in), nil
	case fieldType == "" && json.Valid([]byte(in)):
		return compactJSON(in), nil
	}
	return json.RawMessage(jsonString(input)), nil
}

func compactJSON(s string) json.RawMessage {
	var b bytes.Buffer
	_ = json.Compact(&b, []byte(s))
	return b.Bytes()
}

// SetPath writes v at a dotted path of a _source object in the shape the
// document already has: a key with literal dots is updated when it
// exists, otherwise nested objects are followed and made where missing.
// Other keys keep their order.
func SetPath(src json.RawMessage, path string, v json.RawMessage) (json.RawMessage, error) {
	out, err := setPath(src, strings.Split(path, "."), v)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := json.Compact(&b, out); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func setPath(src json.RawMessage, segs []string, v json.RawMessage) (json.RawMessage, error) {
	obj, err := decodeObject(src)
	if err != nil {
		return nil, err
	}
	for n := len(segs); n >= 1; n-- {
		key := strings.Join(segs[:n], ".")
		for i, m := range obj {
			if m.Key != key {
				continue
			}
			if n == len(segs) {
				obj[i].Val = v
				return obj.encode(), nil
			}
			if kindOf(m.Val) != '{' {
				return nil, fmt.Errorf("%s is not an object", key)
			}
			sub, err := setPath(m.Val, segs[n:], v)
			if err != nil {
				return nil, err
			}
			obj[i].Val = sub
			return obj.encode(), nil
		}
	}
	val := v
	for i := len(segs) - 1; i >= 1; i-- {
		val = object{{segs[i], val}}.encode()
	}
	if segs[0] == "" {
		return nil, errors.New("empty field path")
	}
	return append(obj, member{segs[0], val}).encode(), nil
}
