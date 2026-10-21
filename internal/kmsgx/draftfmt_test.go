package kmsgx

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

func TestFormatDraftRoundTrip(t *testing.T) {
	recs := []kafka.Record{
		{Key: []byte("ord-1"), Value: []byte(`{"id": 1}`), Headers: []kafka.Header{{Key: "trace", Value: []byte("abc")}}},
		{Key: []byte("k"), Value: []byte("null")},
		{Key: []byte("k"), Value: []byte(" null\n")},
		{Key: []byte("k"), Value: []byte("a\n===\nb")},
		{Key: []byte("k"), Value: []byte("a\n === \nb")},
		{Key: []byte("\x00\xff"), Value: []byte("v")},
		{Key: []byte("a\nb"), Value: []byte("v")},
		{Key: []byte("null"), Value: []byte("v")},
		{Key: []byte(" padded "), Value: []byte("v")},
		{Key: []byte("k"), Value: []byte("v"), Headers: []kafka.Header{{Key: "h", Value: []byte("x\ny")}, {Key: "b", Value: []byte{0xff}}, {Key: "s", Value: []byte(" sp ")}}},
		{Key: []byte("k"), Value: []byte{0, 0, 0, 0, 7, 2, 'a'}},
		{KeyNull: true, Value: []byte("no key")},
		{Key: []byte("k"), ValueNull: true},
		{Key: []byte("k"), Value: []byte("  spaced  ")},
		{Key: []byte("k"), Value: []byte{}},
		{KeyNull: true, Value: []byte{}},
		{KeyNull: true, Value: []byte("key: looks like a header")},
		{KeyNull: true, Value: []byte("# looks like a comment")},
		{KeyNull: true, Value: []byte("\xc3\x28")},
		{KeyNull: true, Value: []byte("v"), Headers: []kafka.Header{
			{Key: "a=b", Value: []byte("c")}, {Key: "x=y", Value: []byte{0xff}}, {Key: "", Value: []byte("e")},
			{Key: " sp", Value: []byte("v")}, {Key: "n\nl", Value: []byte{}}, {Key: "\xff", Value: []byte("v")},
		}},
	}
	for i := range recs {
		recs[i].Topic, recs[i].Partition, recs[i].Offset = "orders", 1, int64(i)
	}
	text := FormatDraft(recs)
	got, err := ParseDraft(text)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if len(got) != len(recs) {
		t.Fatalf("%d messages back, want %d:\n%s", len(got), len(recs), text)
	}
	for i, r := range recs {
		g := got[i]
		if g.KeyNull != r.KeyNull || !bytes.Equal(g.Key, r.Key) || g.ValueNull != r.ValueNull || !bytes.Equal(g.Value, r.Value) || g.Partition != -1 {
			t.Errorf("message %d: got key %q (null %v) value %q (null %v) p%d; want key %q value %q", i, g.Key, g.KeyNull, g.Value, g.ValueNull, g.Partition, r.Key, r.Value)
		}
		if len(g.Headers) != len(r.Headers) {
			t.Errorf("message %d headers: %+v", i, g.Headers)
			continue
		}
		for j, h := range r.Headers {
			if g.Headers[j].Key != h.Key || !bytes.Equal(g.Headers[j].Value, h.Value) {
				t.Errorf("message %d header %d: %+v", i, j, g.Headers[j])
			}
		}
	}
	if !strings.HasPrefix(text, "# from orders p1:0\n") {
		t.Fatalf("text starts %q", text[:min(len(text), 40)])
	}
}

func TestParseJSONL(t *testing.T) {
	recs := []kafka.Record{
		{
			Topic: "orders", Partition: 2, Offset: 7, Time: time.Unix(0, 0), Key: []byte("k1"), Value: []byte(`{"a":1}`),
			Headers: []kafka.Header{{Key: "h", Value: []byte{0xff}}},
		},
		{Topic: "orders", KeyNull: true, Value: []byte{0, 0xff, 1}},
		{Topic: "orders", Key: []byte("k3"), ValueNull: true},
	}
	var b strings.Builder
	for i, r := range recs {
		b.Write(JSON(r))
		b.WriteString("\n")
		if i == 0 {
			b.WriteString("\n")
		}
	}
	got, err := ParseJSONL(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d records", len(got))
	}
	for i, r := range recs {
		g := got[i]
		if g.KeyNull != r.KeyNull || !bytes.Equal(g.Key, r.Key) || g.ValueNull != r.ValueNull || !bytes.Equal(g.Value, r.Value) || g.Partition != -1 {
			t.Errorf("record %d = %+v", i, g)
		}
	}
	if h := got[0].Headers; len(h) != 1 || h[0].Key != "h" || !bytes.Equal(h[0].Value, []byte{0xff}) {
		t.Errorf("headers = %+v", h)
	}
	if got[2].Line != 4 {
		t.Errorf("line of the third = %d", got[2].Line)
	}
	_, err = ParseJSONL(strings.NewReader(`{"key":"a","value":"b"}` + "\n{bad\n"))
	if err == nil || !strings.HasPrefix(err.Error(), "line 2: ") {
		t.Fatalf("err = %v", err)
	}
}
