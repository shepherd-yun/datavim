package kmsgx

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/shepherd-yun/datavim/internal/kafka"
)

// DraftRecord is one message of a producer draft.
type DraftRecord struct {
	Line, EndLine      int // the message's first and last line, 1-based
	Key, Value         []byte
	KeyNull, ValueNull bool
	Partition          int32 // -1: by key
	Headers            []kafka.Header
	// KeySchema and ValueSchema, when set, serialize the key / value (JSON
	// text here) with a Schema Registry schema before sending.
	KeySchema, ValueSchema *SchemaSpec
}

// SchemaSpec is a key-schema: / value-schema: line: subject[@version][#message].
type SchemaSpec struct {
	Subject string
	Version int    // -1: latest
	Message string // protobuf message full name, "" the first
}

func (s SchemaSpec) String() string {
	out := s.Subject
	if s.Version > 0 {
		out += "@" + strconv.Itoa(s.Version)
	}
	if s.Message != "" {
		out += "#" + s.Message
	}
	return out
}

// DraftError is a draft that does not parse.
type DraftError struct {
	Line int
	Msg  string
}

func (e DraftError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// draftPart is the lines of one message: lines[start:end], 0-based.
type draftPart struct{ start, end int }

// splitDraft cuts the draft at lines that are only ===. A separator line
// belongs to the part before it.
func splitDraft(lines []string) []draftPart {
	var parts []draftPart
	start := 0
	for i, l := range lines {
		if strings.TrimSpace(l) == "===" {
			parts = append(parts, draftPart{start, i})
			start = i + 1
		}
	}
	return append(parts, draftPart{start, len(lines)})
}

// ParseDraft reads every message of a producer draft. Messages are
// separated by a line of ===. Each one starts with optional # comment
// lines, then optional header lines (key:, partition:, header: name=value,
// header-base64: name=<base64>, header-binary: <base64 name>,<base64 value>,
// key-encoding: / value-encoding: base64|hex,
// key-schema: / value-schema: subject[@version][#message]) and then the
// body, up to the separator. The headers end at the first line
// that is not one; a blank line there is dropped. No key: line, or
// key: null, is a null key; a body of just null is a null value.
// Any error fails the whole draft.
func ParseDraft(text string) ([]DraftRecord, error) {
	lines := strings.Split(text, "\n")
	var out []DraftRecord
	for _, p := range splitDraft(lines) {
		r, ok, err := parsePart(lines, p)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// DraftAt reads the message line (1-based) belongs to; other messages'
// errors do not matter.
func DraftAt(text string, line int) (DraftRecord, error) {
	lines := strings.Split(text, "\n")
	parts := splitDraft(lines)
	for i, p := range parts {
		sep := p.end // the separator line, if any
		if line-1 >= p.start && (line-1 <= sep || i == len(parts)-1) {
			r, ok, err := parsePart(lines, p)
			if err != nil {
				return DraftRecord{}, err
			}
			if !ok {
				return DraftRecord{}, fmt.Errorf("no message at line %d", line)
			}
			return r, nil
		}
	}
	return DraftRecord{}, fmt.Errorf("no message at line %d", line)
}

// parsePart reads one message; ok is false for a part with only comments
// and blank lines.
func parsePart(lines []string, p draftPart) (DraftRecord, bool, error) {
	i := p.start
	for i < p.end && (strings.TrimSpace(lines[i]) == "" || strings.HasPrefix(strings.TrimSpace(lines[i]), "#")) {
		i++
	}
	end := p.end
	for end > i && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if i >= end {
		return DraftRecord{}, false, nil
	}
	r := DraftRecord{Line: i + 1, EndLine: end, KeyNull: true, Partition: -1}
	seen := map[string]int{} // header line name -> its line, 1-based
	var keyEnc, valueEnc string
	for ; i < end; i++ {
		l := lines[i]
		name, val, ok := headerLine(l)
		if !ok {
			if strings.HasPrefix(strings.TrimSpace(l), "#") && len(seen) > 0 {
				continue
			}
			break
		}
		if name != "header" && name != "header-base64" && name != "header-binary" && seen[name] > 0 {
			return r, false, DraftError{i + 1, name + " is given twice"}
		}
		seen[name] = i + 1
		switch name {
		case "key":
			r.Key, r.KeyNull = []byte(val), val == "null"
			if r.KeyNull {
				r.Key = nil
			}
		case "partition":
			n, err := strconv.ParseInt(val, 10, 32)
			if err != nil || n < 0 {
				return r, false, DraftError{i + 1, "partition must be a number ≥ 0"}
			}
			r.Partition = int32(n)
		case "header":
			k, v, ok := strings.Cut(val, "=")
			if !ok || strings.TrimSpace(k) == "" {
				return r, false, DraftError{i + 1, "header needs name=value"}
			}
			r.Headers = append(r.Headers, kafka.Header{Key: strings.TrimSpace(k), Value: []byte(v)})
		case "header-base64":
			k, v, ok := strings.Cut(val, "=")
			b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
			if !ok || strings.TrimSpace(k) == "" || err != nil {
				return r, false, DraftError{i + 1, "header-base64 needs name=<base64>"}
			}
			r.Headers = append(r.Headers, kafka.Header{Key: strings.TrimSpace(k), Value: b})
		case "header-binary":
			k, v, ok := strings.Cut(val, ",")
			kb, kerr := base64.StdEncoding.DecodeString(strings.TrimSpace(k))
			vb, verr := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
			if !ok || kerr != nil || verr != nil {
				return r, false, DraftError{i + 1, "header-binary needs <base64 name>,<base64 value>"}
			}
			r.Headers = append(r.Headers, kafka.Header{Key: string(kb), Value: vb})
		case "key-encoding", "value-encoding":
			if val != "base64" && val != "hex" {
				return r, false, DraftError{i + 1, name + " must be base64 or hex"}
			}
			if name == "key-encoding" {
				keyEnc = val
			} else {
				valueEnc = val
			}
		case "key-schema", "value-schema":
			spec, err := parseSchemaSpec(val)
			if err != nil {
				return r, false, DraftError{i + 1, name + " wants subject[@version][#message]"}
			}
			if name == "key-schema" {
				r.KeySchema = &spec
			} else {
				r.ValueSchema = &spec
			}
		}
	}
	for _, side := range []string{"key", "value"} {
		if s, e := seen[side+"-schema"], seen[side+"-encoding"]; s > 0 && e > 0 {
			return r, false, DraftError{max(s, e), side + "-schema and " + side + "-encoding can't both be given"}
		}
	}
	if keyEnc != "" && !r.KeyNull {
		b, err := decodeText(keyEnc, string(r.Key))
		if err != nil {
			return r, false, DraftError{seen["key"], "the key is not " + keyEnc}
		}
		r.Key = b
	}
	if len(seen) > 0 && i < end && strings.TrimSpace(lines[i]) == "" {
		i++ // the blank line between the headers and the body
	}
	body := strings.Join(lines[i:end], "\n")
	switch {
	case valueEnc != "":
		b, err := decodeText(valueEnc, strings.TrimSpace(body))
		if err != nil {
			return r, false, DraftError{i + 1, "the body is not " + valueEnc}
		}
		r.Value = b
	case i >= end:
		r.Value = []byte{}
	case body == "null":
		r.ValueNull = true
	default:
		r.Value = []byte(body)
	}
	return r, true, nil
}

func decodeText(enc, s string) ([]byte, error) {
	if enc == "hex" {
		return hex.DecodeString(s)
	}
	return base64.StdEncoding.DecodeString(s)
}

func parseSchemaSpec(v string) (SchemaSpec, error) {
	spec := SchemaSpec{Version: -1}
	errSpec := errors.New("bad schema spec")
	rest, msg, hasMsg := strings.Cut(v, "#")
	if hasMsg {
		if msg = strings.TrimSpace(msg); msg == "" {
			return spec, errSpec
		}
		spec.Message = msg
	}
	subject, ver, hasVer := strings.Cut(rest, "@")
	if hasVer {
		n, err := strconv.Atoi(strings.TrimSpace(ver))
		if err != nil || n < 1 {
			return spec, errSpec
		}
		spec.Version = n
	}
	if spec.Subject = strings.TrimSpace(subject); spec.Subject == "" {
		return spec, errSpec
	}
	return spec, nil
}

// headerNames are the lines a message may start with, before its body.
var headerNames = []string{
	"key", "partition", "header", "header-base64", "header-binary",
	"key-encoding", "value-encoding", "key-schema", "value-schema",
}

// headerLine splits a header line: key:, partition:, header: and the rest
// of headerNames.
func headerLine(l string) (name, val string, ok bool) {
	for _, n := range headerNames {
		if rest, found := strings.CutPrefix(l, n+":"); found {
			return n, strings.TrimSpace(rest), true
		}
	}
	return "", "", false
}
