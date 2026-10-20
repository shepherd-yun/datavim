package kmsgx

import (
	"strings"
	"testing"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

func TestFilterMatch(t *testing.T) {
	now := time.Date(2026, 10, 20, 14, 0, 0, 0, time.Local)
	rec := func(key, value string) kafka.Record {
		return kafka.Record{
			Key: []byte(key), Value: []byte(value), Size: len(key) + len(value),
			Time: time.Date(2026, 10, 20, 9, 30, 0, 0, time.Local),
		}
	}
	big := rec("k", strings.Repeat("x", 2000))
	big.Partition = 1
	hdr := rec("k", "v")
	hdr.Headers = []kafka.Header{{Key: "source", Value: []byte("checkout")}}
	for _, tc := range []struct {
		expr string
		r    kafka.Record
		want bool
	}{
		{"", rec("a", "b"), true},
		{"key = ord-1", rec("ord-1", ""), true},
		{"key = ord-1", rec("ord-2", ""), false},
		{"key != ord-1", rec("ord-2", ""), true},
		{`value ~ "SKU"`, rec("", `{"sku":1}`), true},
		{"value.user.id = 42", rec("", `{"user":{"id":42}}`), true},
		{"value.user.id = 42", rec("", `{"user":{"id":"42x"}}`), false},
		{"value.user.id = 42", rec("", `{"user":{"id":42.0}}`), true},
		{"value.items.0.sku = A-1", rec("", `{"items":[{"sku":"A-1"}]}`), true},
		{"value.id = 1", rec("", `not json`), false},
		{"value.id = 1", rec("", `[1]`), false},
		{"partition = 1 and size > 1000", big, true},
		{"partition = 1 and size > 1000", rec("k", "v"), false},
		{"key = a or key = b", rec("b", ""), true},
		{"key = a or key = b", rec("c", ""), false},
		{"key = a or key = b and value = x", rec("a", "y"), true},
		{"(key = a or key = b) and value = x", rec("a", "y"), false},
		{"header.source = checkout", hdr, true},
		{"header.missing != x", hdr, false},
		{"ts > 09:12", rec("", ""), true},
		{"ts < 09:12", rec("", ""), false},
		{`ts > "2026-10-20 09:00"`, rec("", ""), true},
		{"offset < 10", rec("", ""), true},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			f, err := ParseFilter(tc.expr, now)
			if err != nil {
				t.Fatal(err)
			}
			if got := f.Match(tc.r); got != tc.want {
				t.Fatalf("Match = %v, want %v", got, tc.want)
			}
		})
	}
	var zero Filter
	if !zero.Match(rec("a", "b")) {
		t.Fatal("the zero Filter matches everything")
	}
}

func TestParseFilterErrors(t *testing.T) {
	for _, tc := range []struct{ expr, want string }{
		{"foo = 1", "unknown field foo"},
		{"key ==", "column 6: expected a value"},
		{"key", "column 4: expected an operator"},
		{"key = a and", "column 12: expected a field"},
		{"(key = a", "column 9: expected )"},
		{`key = "open`, "unterminated quote"},
		{"ts > soon", `bad time "soon"`},
		{"partition = x", `partition needs a number, got "x"`},
		{"key = a b", `column 9: expected and / or, got "b"`},
	} {
		_, err := ParseFilter(tc.expr, time.Now())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParseFilter(%q) err = %v, want %q", tc.expr, err, tc.want)
		}
	}
}

func TestFilterString(t *testing.T) {
	f, err := ParseFilter("  key = a  ", time.Now())
	if err != nil || f.String() != "key = a" {
		t.Fatalf("String = %q, %v", f.String(), err)
	}
}

func TestCellFilter(t *testing.T) {
	for _, tc := range []struct{ existing, col, op, value, want string }{
		{"", "key", "=", "a b", `key = "a b"`},
		{"partition = 1", "value", "~", "x", "partition = 1 and value ~ x"},
		{"", "KEY", "!=", `say "hi"`, `key != "say \"hi\""`},
		{"", "offset", "=", "7", "offset = 7"},
		{"", "value", "=", "", `value = ""`},
		{"", "value", "=", "or", `value = "or"`},
	} {
		got := CellFilter(tc.existing, tc.col, tc.op, tc.value)
		if got != tc.want {
			t.Errorf("CellFilter = %q, want %q", got, tc.want)
		}
		if _, err := ParseFilter(got, time.Now()); err != nil {
			t.Errorf("CellFilter output %q does not parse: %v", got, err)
		}
	}
}
