package kmsgx

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

// FormatDraft is recs as producer draft text, one message each, that
// ParseDraft reads back to the same bytes. A "# from <topic> p<P>:<offset>"
// comment heads each. Partitions are left to the key. Bytes that the
// draft's plain lines would change are written in base64: Schema Registry
// wire-format values are sent again as they are.
func FormatDraft(recs []kafka.Record) string {
	var b strings.Builder
	for i, r := range recs {
		if i > 0 {
			b.WriteString("\n===\n")
		}
		fmt.Fprintf(&b, "# from %s p%d:%d\n", r.Topic, r.Partition, r.Offset)
		if !r.KeyNull {
			if plainKey(r.Key) {
				b.WriteString("key: " + string(r.Key) + "\n")
			} else {
				b.WriteString("key-encoding: base64\nkey: " + base64.StdEncoding.EncodeToString(r.Key) + "\n")
			}
		}
		for _, h := range r.Headers {
			if plainLine(h.Value) {
				b.WriteString("header: " + h.Key + "=" + string(h.Value) + "\n")
			} else {
				b.WriteString("header-base64: " + h.Key + "=" + base64.StdEncoding.EncodeToString(h.Value) + "\n")
			}
		}
		switch {
		case r.ValueNull:
			b.WriteString("null")
		case plainValue(r.Value):
			b.WriteString(string(r.Value))
		default:
			b.WriteString("value-encoding: base64\n" + base64.StdEncoding.EncodeToString(r.Value))
		}
	}
	return b.String() + "\n"
}

// plainLine reports whether v survives a header line: UTF-8, one line, no
// space at the ends (header lines are trimmed).
func plainLine(v []byte) bool {
	s := string(v)
	return utf8.Valid(v) && !strings.ContainsAny(s, "\r\n") && strings.TrimSpace(s) == s
}

func plainKey(k []byte) bool { return plainLine(k) && string(k) != "null" }

// plainValue reports whether v can be the body as it is.
func plainValue(v []byte) bool {
	s := string(v)
	if len(v) == 0 || !utf8.Valid(v) || v[0] == 0 || strings.TrimSpace(s) != s || s == "null" || strings.Contains(s, "\r") {
		return false
	}
	lines := strings.Split(s, "\n")
	first := lines[0]
	if strings.HasPrefix(strings.TrimSpace(first), "#") {
		return false
	}
	if _, _, ok := headerLine(first); ok {
		return false
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "===" {
			return false
		}
	}
	return true
}

// jsonlRecord is one line of kmsgx.JSON.
type jsonlRecord struct {
	Key           *string `json:"key"`
	KeyEncoding   string  `json:"key_encoding"`
	Value         *string `json:"value"`
	ValueEncoding string  `json:"value_encoding"`
	Headers       []struct {
		Key      string `json:"key"`
		Value    string `json:"value"`
		Encoding string `json:"encoding"`
	} `json:"headers"`
}

// ParseJSONL reads JSON Lines as kmsgx.JSON writes them; partition and
// offset are ignored, blank lines skipped. Errors name the line.
func ParseJSONL(r io.Reader) ([]DraftRecord, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 64<<20)
	var out []DraftRecord
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		rec, err := jsonlLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		rec.Line, rec.EndLine = n, n
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("line %d: %w", n+1, err)
	}
	return out, nil
}

func jsonlLine(line string) (DraftRecord, error) {
	var j jsonlRecord
	if err := json.Unmarshal([]byte(line), &j); err != nil {
		return DraftRecord{}, err
	}
	rec := DraftRecord{Partition: -1}
	var err error
	if rec.Key, rec.KeyNull, err = jsonlBytes(j.Key, j.KeyEncoding, "key"); err != nil {
		return rec, err
	}
	if rec.Value, rec.ValueNull, err = jsonlBytes(j.Value, j.ValueEncoding, "value"); err != nil {
		return rec, err
	}
	for _, h := range j.Headers {
		v, _, err := jsonlBytes(&h.Value, h.Encoding, "header "+strconv.Quote(h.Key))
		if err != nil {
			return rec, err
		}
		rec.Headers = append(rec.Headers, kafka.Header{Key: h.Key, Value: v})
	}
	return rec, nil
}

func jsonlBytes(v *string, enc, what string) ([]byte, bool, error) {
	if v == nil {
		return nil, true, nil
	}
	switch enc {
	case "":
		return []byte(*v), false, nil
	case "base64":
		b, err := base64.StdEncoding.DecodeString(*v)
		if err != nil {
			return nil, false, fmt.Errorf("%s is not base64", what)
		}
		return b, false, nil
	}
	return nil, false, errors.New(what + ": unknown encoding " + strconv.Quote(enc))
}
