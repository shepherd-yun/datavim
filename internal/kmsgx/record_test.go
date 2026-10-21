package kmsgx

import (
	"testing"
	"time"

	"github.com/shepherd-yun/datavim/internal/kafka"
)

func TestJSON(t *testing.T) {
	ts := time.Date(2026, 10, 20, 1, 12, 1, 410_000_000, time.UTC)
	base := kafka.Record{Topic: "orders", Partition: 1, Offset: 7, Time: ts.In(time.FixedZone("x", 8*3600))}
	for _, tc := range []struct {
		name string
		edit func(*kafka.Record)
		want string
	}{
		{"text", func(r *kafka.Record) {
			r.Key, r.Value = []byte("k"), []byte(`{"a":1}`)
			r.Headers = []kafka.Header{{Key: "h", Value: []byte("v")}}
		}, `{"topic":"orders","partition":1,"offset":7,"timestamp":"2026-10-20T01:12:01.41Z","key":"k","headers":[{"key":"h","value":"v"}],"value":"{\"a\":1}"}`},
		{"binary value", func(r *kafka.Record) {
			r.Key, r.Value = []byte("k"), []byte("\x00\xff")
		}, `{"topic":"orders","partition":1,"offset":7,"timestamp":"2026-10-20T01:12:01.41Z","key":"k","headers":[],"value":"AP8=","value_encoding":"base64"}`},
		{"null key and value", func(r *kafka.Record) {
			r.KeyNull, r.ValueNull = true, true
		}, `{"topic":"orders","partition":1,"offset":7,"timestamp":"2026-10-20T01:12:01.41Z","key":null,"headers":[],"value":null}`},
		{"binary key and header", func(r *kafka.Record) {
			r.Key, r.Value = []byte{0xff}, []byte("")
			r.Headers = []kafka.Header{{Key: "b", Value: []byte{0xfe}}, {Key: "n"}}
		}, `{"topic":"orders","partition":1,"offset":7,"timestamp":"2026-10-20T01:12:01.41Z","key":"/w==","key_encoding":"base64","headers":[{"key":"b","value":"/g==","encoding":"base64"},{"key":"n","value":""}],"value":""}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.edit(&r)
			if got := string(JSON(r)); got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestPreview(t *testing.T) {
	for _, tc := range []struct {
		in    string
		null  bool
		width int
		want  string
	}{
		{"a\nb", false, 20, `a\nb`},
		{"", true, 20, "(null)"},
		{"", false, 20, "(empty)"},
		{`{"id":88121,"amount":99}`, false, 10, `{"id":881…`},
		{"\x00\xff", false, 20, `\x00\xFF`},
		{"short", false, 5, "short"},
	} {
		if got := Preview([]byte(tc.in), tc.null, tc.width); got != tc.want {
			t.Errorf("Preview(%q, %v, %d) = %q, want %q", tc.in, tc.null, tc.width, got, tc.want)
		}
	}
}
