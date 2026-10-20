// Package kmsgx is the terminal-free logic behind the Kafka views: the JSON
// form of a message, start positions, filters, producer drafts and offset
// reset plans. It does no IO.
package kmsgx

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/rcmd"
)

// JSON returns one message as a JSON object, the form JSONL export and
// copying a whole message share. Keys are in a fixed order: topic,
// partition, offset, timestamp (RFC 3339, UTC), key, headers, value. A null
// key or value is JSON null; valid UTF-8 is a string; anything else is
// base64 with "key_encoding" / "value_encoding" ("encoding" in a header)
// set to "base64".
func JSON(r kafka.Record) []byte {
	var b bytes.Buffer
	b.WriteString(`{"topic":`)
	writeString(&b, r.Topic)
	b.WriteString(`,"partition":`)
	b.WriteString(itoa(int64(r.Partition)))
	b.WriteString(`,"offset":`)
	b.WriteString(itoa(r.Offset))
	b.WriteString(`,"timestamp":`)
	writeString(&b, r.Time.UTC().Format(time.RFC3339Nano))
	writeBytes(&b, "key", "key_encoding", r.Key, r.KeyNull)
	b.WriteString(`,"headers":[`)
	for i, h := range r.Headers {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"key":`)
		writeString(&b, h.Key)
		writeBytes(&b, "value", "encoding", h.Value, false)
		b.WriteByte('}')
	}
	b.WriteByte(']')
	writeBytes(&b, "value", "value_encoding", r.Value, r.ValueNull)
	b.WriteByte('}')
	return b.Bytes()
}

// writeBytes writes ,"name":<v> and, for non-UTF-8 v, ,"enc":"base64".
func writeBytes(b *bytes.Buffer, name, enc string, v []byte, null bool) {
	b.WriteString(`,"` + name + `":`)
	switch {
	case null:
		b.WriteString("null")
	case utf8.Valid(v):
		writeString(b, string(v))
	default:
		writeString(b, base64.StdEncoding.EncodeToString(v))
		b.WriteString(`,"` + enc + `":"base64"`)
	}
}

func writeString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)       // a string always encodes
	b.Truncate(b.Len() - 1) // Encode's newline
}

func itoa(n int64) string {
	out, _ := json.Marshal(n)
	return string(out)
}

// Preview is the one-line form of a key or value in the message table:
// "(null)", "(empty)", or the bytes escaped with rcmd.Escape and cut to
// width cells, ending in … when cut.
func Preview(v []byte, null bool, width int) string {
	switch {
	case null:
		return "(null)"
	case len(v) == 0:
		return "(empty)"
	}
	// escaping a huge value only to cut it is wasted work
	if limit := 4 * (width + 1); len(v) > limit {
		v = v[:limit]
	}
	return ansi.Truncate(rcmd.Escape(string(v)), width, "…")
}
