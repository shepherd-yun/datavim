package kmsgx

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

func TestWire(t *testing.T) {
	for _, tc := range []struct {
		in      []byte
		id      int
		payload []byte
		ok      bool
	}{
		{[]byte{0, 0, 0, 0, 7, 0xAA}, 7, []byte{0xAA}, true},
		{[]byte{0, 0, 0, 1, 0}, 256, []byte{}, true},
		{[]byte{1, 0, 0, 0, 7, 0xAA}, 0, nil, false},
		{[]byte{0, 0, 0, 7}, 0, nil, false},
		{nil, 0, nil, false},
	} {
		id, payload, ok := Wire(tc.in)
		if id != tc.id || ok != tc.ok || !bytes.Equal(payload, tc.payload) {
			t.Errorf("Wire(% x) = %d, % x, %v", tc.in, id, payload, ok)
		}
	}
}

// sameJSON compares two JSON texts as values.
func sameJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("decoded is not JSON: %s", got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}

// roundTrip encodes body, checks the header and decodes it back.
func roundTrip(t *testing.T, s kafka.Schema, refs map[string]kafka.Schema, message, body string) []byte {
	t.Helper()
	return roundTripAs(t, s, refs, message, body, body)
}

// roundTripAs is roundTrip for a body that decodes to want.
func roundTripAs(t *testing.T, s kafka.Schema, refs map[string]kafka.Schema, message, body, want string) []byte {
	t.Helper()
	wire, err := Encode(s, refs, message, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	id, payload, ok := Wire(wire)
	if !ok || id != s.ID {
		t.Fatalf("header: id %d ok %v (% x)", id, ok, wire)
	}
	got, err := Decode(s, refs, payload)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, got, want)
	return payload
}

const orderSchema = `{"type":"record","name":"Order","fields":[{"name":"id","type":"long"},{"name":"note","type":["null","string"],"default":null}]}`

func TestAvroRoundTrip(t *testing.T) {
	s := kafka.Schema{ID: 12, Type: "AVRO", Text: orderSchema}
	// hamba/avro decodes a nullable union to the bare value; both shapes encode
	roundTripAs(t, s, nil, "", `{"id":88124,"note":{"string":"x"}}`, `{"id":88124,"note":"x"}`)
	roundTrip(t, s, nil, "", `{"id":88124,"note":"x"}`)
	roundTrip(t, s, nil, "", `{"id":1,"note":null}`)
}

func TestAvroTypes(t *testing.T) {
	s := kafka.Schema{ID: 3, Type: "AVRO", Text: `{"type":"record","name":"All","namespace":"shop","fields":[
		{"name":"b","type":"boolean"},{"name":"i","type":"int"},{"name":"f","type":"float"},{"name":"d","type":"double"},
		{"name":"by","type":"bytes"},{"name":"e","type":{"type":"enum","name":"Color","symbols":["RED","BLUE"]}},
		{"name":"a","type":{"type":"array","items":"string"}},{"name":"m","type":{"type":"map","values":"long"}},
		{"name":"u","type":["null","int",{"type":"record","name":"Inner","fields":[{"name":"x","type":"int"}]}]}]}`}
	roundTrip(t, s, nil, "", `{"b":true,"i":-3,"f":1.5,"d":2.25,"by":"AP8=","e":"BLUE","a":["x","y"],"m":{"k":9007199254740993},"u":{"shop.Inner":{"x":4}}}`)
}

func TestAvroWithReference(t *testing.T) {
	addr := kafka.Schema{ID: 1, Subject: "address-value", Version: 1, Type: "AVRO",
		Text: `{"type":"record","name":"Address","namespace":"shop","fields":[{"name":"city","type":"string"}]}`}
	s := kafka.Schema{ID: 2, Type: "AVRO",
		Text:       `{"type":"record","name":"Order","namespace":"shop","fields":[{"name":"id","type":"long"},{"name":"ship","type":"Address"}]}`,
		References: []kafka.SchemaRef{{Name: "shop.Address", Subject: "address-value", Version: 1}}}
	roundTrip(t, s, map[string]kafka.Schema{"shop.Address": addr}, "", `{"id":5,"ship":{"city":"Oslo"}}`)
}

const shopProto = `syntax = "proto3";
package shop;
message Order { int64 id = 1; string note = 2; }
message Refund { int64 id = 1; }
`

func TestProtoRoundTrip(t *testing.T) {
	s := kafka.Schema{ID: 9, Type: "PROTOBUF", Text: shopProto}
	payload := roundTrip(t, s, nil, "shop.Refund", `{"id":"7"}`)
	// message indexes [1]: count 1 and index 1, both zigzag
	if !bytes.HasPrefix(payload, []byte{2, 2}) {
		t.Fatalf("refund indexes: % x", payload)
	}
	payload = roundTrip(t, s, nil, "", `{"id":"1","note":"n"}`)
	if payload[0] != 0 {
		t.Fatalf("the first message's indexes are one 0: % x", payload)
	}
	if _, err := Encode(s, nil, "shop.Nope", []byte(`{}`)); err == nil || err.Error() != "no message shop.Nope in the schema" {
		t.Fatalf("unknown message err = %v", err)
	}
}

func TestProtoImport(t *testing.T) {
	common := kafka.Schema{ID: 1, Type: "PROTOBUF", Text: `syntax = "proto3"; package common; message Money { int64 cents = 1; string currency = 2; }`}
	s := kafka.Schema{ID: 2, Type: "PROTOBUF",
		Text:       `syntax = "proto3"; package shop; import "common.proto"; message Order { int64 id = 1; common.Money total = 2; }`,
		References: []kafka.SchemaRef{{Name: "common.proto", Subject: "common", Version: 1}}}
	roundTrip(t, s, map[string]kafka.Schema{"common.proto": common}, "", `{"id":"3","total":{"cents":"250","currency":"EUR"}}`)
}

func TestJSONSchema(t *testing.T) {
	s := kafka.Schema{ID: 4, Type: "JSON", Text: `{"type":"object"}`}
	roundTrip(t, s, nil, "", `{"a":1}`)
	if _, err := Decode(s, nil, []byte("{nope")); err == nil {
		t.Fatal("bad JSON decoded")
	}
}

func TestDecodeErrors(t *testing.T) {
	s := kafka.Schema{ID: 12, Type: "AVRO", Text: orderSchema}
	wire, err := Encode(s, nil, "", []byte(`{"id":88124,"note":{"string":"a long note"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(s, nil, wire[5:len(wire)-4]); err == nil {
		t.Fatal("truncated Avro decoded")
	}
	if _, err := Decode(kafka.Schema{Type: "XML"}, nil, nil); err == nil || err.Error() != `unsupported schema type "XML"` {
		t.Fatalf("err = %v", err)
	}
	if _, err := Encode(s, nil, "", []byte(`{"note":null}`)); err == nil {
		t.Fatal("a missing field encoded")
	}
	if _, err := Decode(kafka.Schema{Type: "PROTOBUF", Text: shopProto}, nil, []byte{0x08}); err == nil || !strings.Contains(err.Error(), "message index") {
		t.Logf("err = %v", err)
	}
}
