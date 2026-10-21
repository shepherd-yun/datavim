package kmsgx

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

// Wire splits the Confluent wire format: a 0 byte, then the schema id as
// 4 big-endian bytes. ok is false when b is not in it.
func Wire(b []byte) (id int, payload []byte, ok bool) {
	if len(b) < 5 || b[0] != 0 {
		return 0, nil, false
	}
	return int(binary.BigEndian.Uint32(b[1:5])), b[5:], true
}

func wireHeader(id int) []byte {
	h := make([]byte, 5, 64)
	binary.BigEndian.PutUint32(h[1:], uint32(id)) //nolint:gosec // registry ids are positive int32s
	return h
}

// Decode turns payload (after the 5-byte header) of schema s into compact
// JSON. refs are s's references by name (Registry.Resolve). For PROTOBUF
// the payload starts with the message indexes.
func Decode(s kafka.Schema, refs map[string]kafka.Schema, payload []byte) ([]byte, error) {
	switch s.Type {
	case "AVRO", "":
		return decodeAvro(s, refs, payload)
	case "PROTOBUF":
		return decodeProto(s, refs, payload)
	case "JSON":
		if !json.Valid(payload) {
			return nil, fmt.Errorf("not JSON")
		}
		return compactJSON(payload)
	}
	return nil, fmt.Errorf("unsupported schema type %q", s.Type)
}

// Encode is the wire bytes (header included) of the JSON body under s.
// message picks a PROTOBUF message by full name; "" is the first one.
func Encode(s kafka.Schema, refs map[string]kafka.Schema, message string, body []byte) ([]byte, error) {
	var payload []byte
	var err error
	switch s.Type {
	case "AVRO", "":
		payload, err = encodeAvro(s, refs, body)
	case "PROTOBUF":
		payload, err = encodeProto(s, refs, message, body)
	case "JSON":
		if !json.Valid(body) {
			return nil, fmt.Errorf("the body is not JSON")
		}
		payload, err = compactJSON(body)
	default:
		return nil, fmt.Errorf("unsupported schema type %q", s.Type)
	}
	if err != nil {
		return nil, err
	}
	return append(wireHeader(s.ID), payload...), nil
}

func compactJSON(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// parsed caches what a schema text and its references parse or compile
// to, for the process's life: the same few schemas decode every message.
var parsed sync.Map // key -> any

func schemaKey(kind string, s kafka.Schema, refs map[string]kafka.Schema) string {
	h := sha256.New()
	h.Write([]byte(kind + "\x00" + s.Text))
	names := make([]string, 0, len(refs))
	for n := range refs {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		h.Write([]byte("\x00" + n + "\x00" + refs[n].Text))
	}
	return string(h.Sum(nil))
}

func cached[T any](key string, build func() (T, error)) (T, error) {
	if v, ok := parsed.Load(key); ok {
		return v.(T), nil
	}
	v, err := build()
	if err != nil {
		return v, err
	}
	parsed.Store(key, v)
	return v, nil
}
