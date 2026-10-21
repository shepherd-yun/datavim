package kmsgx

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

const specDraft = `# topic: orders
key: ord-1
partition: 2
header: trace-id=abc123
header: source=sqlvim

{"id": 1, "amount": 99}
===
key: ord-2
{"id": 2, "amount": 12}
`

func TestParseDraft(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []DraftRecord
	}{
		{"spec example", specDraft, []DraftRecord{
			{
				Line: 2, EndLine: 7, Key: []byte("ord-1"), Partition: 2, Value: []byte(`{"id": 1, "amount": 99}`),
				Headers: []kafka.Header{{Key: "trace-id", Value: []byte("abc123")}, {Key: "source", Value: []byte("sqlvim")}},
			},
			{Line: 9, EndLine: 10, Key: []byte("ord-2"), Partition: -1, Value: []byte(`{"id": 2, "amount": 12}`)},
		}},
		{"body only", "{\"a\":1}\n\n\n", []DraftRecord{
			{Line: 1, EndLine: 1, KeyNull: true, Partition: -1, Value: []byte(`{"a":1}`)},
		}},
		{"null key and tombstone", "key: null\n\nnull", []DraftRecord{
			{Line: 1, EndLine: 3, KeyNull: true, ValueNull: true, Partition: -1},
		}},
		{"empty key", "key:\n\nv", []DraftRecord{
			{Line: 1, EndLine: 3, Key: []byte{}, Partition: -1, Value: []byte("v")},
		}},
		{"headers only", "key: k\nheader: a=\nheader: a=2", []DraftRecord{
			{
				Line: 1, EndLine: 3, Key: []byte("k"), Partition: -1, Value: []byte{},
				Headers: []kafka.Header{{Key: "a", Value: []byte{}}, {Key: "a", Value: []byte("2")}},
			},
		}},
		{"header-like line in the body", "key: k\n\nline one\nkey: x\n", []DraftRecord{
			{Line: 1, EndLine: 4, Key: []byte("k"), Partition: -1, Value: []byte("line one\nkey: x")},
		}},
		{"comment-only parts are skipped", "# nothing\n===\n\nhello\n===\n# end\n", []DraftRecord{
			{Line: 4, EndLine: 4, KeyNull: true, Partition: -1, Value: []byte("hello")},
		}},
		{"multi-line body keeps inner blank lines", "a\n\nb\n", []DraftRecord{
			{Line: 1, EndLine: 3, KeyNull: true, Partition: -1, Value: []byte("a\n\nb")},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDraft(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestParseDraftErrors(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"# x\npartition: x\n\nv", "line 2: partition must be a number ≥ 0"},
		{"key: a\npartition: -1\n\nv", "line 2: partition must be a number ≥ 0"},
		{"key: a\npartition: 1\nheader: novalue\n\nv", "line 3: header needs name=value"},
		{"key: a\nheader: =v\n\nv", "line 2: header needs name=value"},
		{"key: a\nkey: b\n\nv", "line 2: key is given twice"},
		{"v\n===\npartition: 1\npartition: 2", "line 4: partition is given twice"},
	} {
		got, err := ParseDraft(tc.text)
		if err == nil || err.Error() != tc.want || got != nil {
			t.Errorf("ParseDraft(%q) = %v, %v; want %q", tc.text, got, err, tc.want)
		}
		var de DraftError
		if err != nil && !asDraftError(err, &de) {
			t.Errorf("%T is not a DraftError", err)
		}
	}
}

func asDraftError(err error, de *DraftError) bool { return errors.As(err, de) }

func TestDraftAt(t *testing.T) {
	for line, key := range map[int]string{1: "ord-1", 7: "ord-1", 8: "ord-1", 9: "ord-2", 10: "ord-2", 11: "ord-2"} {
		r, err := DraftAt(specDraft, line)
		if err != nil || string(r.Key) != key {
			t.Errorf("line %d: key %q, err %v; want %q", line, r.Key, err, key)
		}
	}
	// another message's error does not stop this one
	r, err := DraftAt("partition: x\n\nv\n===\nkey: ok\n\nv", 6)
	if err != nil || string(r.Key) != "ok" {
		t.Fatalf("got %+v, %v", r, err)
	}
	if _, err := DraftAt("v\n===\n# only a comment\n", 3); err == nil || !strings.Contains(err.Error(), "no message") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseDraftK2(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want DraftRecord
	}{
		{"hex key", "key-encoding: hex\nkey: 00ff\n\nv", DraftRecord{Line: 1, EndLine: 4, Key: []byte{0, 0xff}, Partition: -1, Value: []byte("v")}},
		{"base64 value", "value-encoding: base64\n\nAP8=\n", DraftRecord{Line: 1, EndLine: 3, KeyNull: true, Partition: -1, Value: []byte{0, 0xff}}},
		{"hex value", "value-encoding: hex\n 00ff \n", DraftRecord{Line: 1, EndLine: 2, KeyNull: true, Partition: -1, Value: []byte{0, 0xff}}},
		{"base64 header", "header-base64: h=AP8=\nheader: t=x\nv", DraftRecord{
			Line: 1, EndLine: 3, KeyNull: true, Partition: -1, Value: []byte("v"),
			Headers: []kafka.Header{{Key: "h", Value: []byte{0, 0xff}}, {Key: "t", Value: []byte("x")}},
		}},
		{"value schema", "value-schema: orders-value@3#shop.Order\n{}", DraftRecord{
			Line: 1, EndLine: 2, KeyNull: true, Partition: -1, Value: []byte("{}"),
			ValueSchema: &SchemaSpec{Subject: "orders-value", Version: 3, Message: "shop.Order"},
		}},
		{"key schema, latest", "key-schema: orders-key\nkey: {\"id\":1}\n{}", DraftRecord{
			Line: 1, EndLine: 3, Key: []byte(`{"id":1}`), Partition: -1, Value: []byte("{}"),
			KeySchema: &SchemaSpec{Subject: "orders-key", Version: -1},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDraft(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || !reflect.DeepEqual(got[0], tc.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
	for _, tc := range []struct{ text, want string }{
		{"value-schema: s@x\n{}", "line 1: value-schema wants subject[@version][#message]"},
		{"value-schema: s#\n{}", "line 1: value-schema wants subject[@version][#message]"},
		{"value-schema: s\nvalue-encoding: hex\n00", "line 2: value-schema and value-encoding can't both be given"},
		{"key-encoding: hex\nkey-schema: s\nkey: 00\nv", "line 2: key-schema and key-encoding can't both be given"},
		{"value-encoding: rot13\nv", "line 1: value-encoding must be base64 or hex"},
		{"value-encoding: hex\nzz", "line 2: the body is not hex"},
		{"header-base64: h=!!\nv", "line 1: header-base64 needs name=<base64>"},
	} {
		_, err := ParseDraft(tc.text)
		var de DraftError
		if err == nil || err.Error() != tc.want || !errors.As(err, &de) {
			t.Errorf("ParseDraft(%q) err = %v, want %q", tc.text, err, tc.want)
		}
	}
}
