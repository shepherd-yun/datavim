package kmsgx

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

// DraftRecord is one message of a producer draft.
type DraftRecord struct {
	Line, EndLine      int // the message's first and last line, 1-based
	Key, Value         []byte
	KeyNull, ValueNull bool
	Partition          int32 // -1: by key
	Headers            []kafka.Header
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
// lines, then optional header lines (key:, partition:, header: name=value)
// and then the body, up to the separator. The headers end at the first line
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
	seen := map[string]bool{}
	for ; i < end; i++ {
		l := lines[i]
		name, val, ok := headerLine(l)
		if !ok {
			if strings.HasPrefix(strings.TrimSpace(l), "#") && len(seen) > 0 {
				continue
			}
			break
		}
		if name != "header" && seen[name] {
			return r, false, DraftError{i + 1, name + " is given twice"}
		}
		seen[name] = true
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
		}
	}
	if len(seen) > 0 && i < end && strings.TrimSpace(lines[i]) == "" {
		i++ // the blank line between the headers and the body
	}
	body := strings.Join(lines[i:end], "\n")
	switch {
	case i >= end:
		r.Value = []byte{}
	case body == "null":
		r.ValueNull = true
	default:
		r.Value = []byte(body)
	}
	return r, true, nil
}

// headerLine splits a key: / partition: / header: line.
func headerLine(l string) (name, val string, ok bool) {
	for _, n := range []string{"key", "partition", "header"} {
		if rest, found := strings.CutPrefix(l, n+":"); found {
			return n, strings.TrimSpace(rest), true
		}
	}
	return "", "", false
}
