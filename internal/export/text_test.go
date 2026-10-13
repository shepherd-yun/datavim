package export

import (
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func tricky() *db.ResultSet {
	return &db.ResultSet{
		Columns: []db.ColumnInfo{
			{Name: "id", DatabaseType: "int4"},
			{Name: "name", DatabaseType: "text"},
			{Name: "note", DatabaseType: "text"},
			{Name: "data", DatabaseType: "jsonb"},
			{Name: "ok", DatabaseType: "bool"},
		},
		Rows: [][]db.Value{
			{{Text: "1"}, {Text: "a\tb"}, {Text: "line1\nline2"}, {Text: `{"k": 1}`}, {Text: "t"}},
			{{Text: "2"}, {Text: `say "hi"|x<y>`}, {Text: ""}, {Null: true}, {Null: true}},
			{{Text: "3"}, {Text: "李雷"}, {Text: `back\slash`}, {Text: "not json"}, {Text: "f"}},
		},
	}
}

func encode(t *testing.T, f Format, rs *db.ResultSet, o Options) string {
	t.Helper()
	s, err := Encode(f, rs, o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDelimitedRoundTrip(t *testing.T) {
	want := [][]string{
		{"id", "name", "note", "data", "ok"},
		{"1", "a\tb", "line1\nline2", `{"k": 1}`, "t"},
		{"2", `say "hi"|x<y>`, "", "", ""},
		{"3", "李雷", `back\slash`, "not json", "f"},
	}
	for f, comma := range map[Format]rune{TSV: '\t', CSV: ','} {
		r := csv.NewReader(strings.NewReader(encode(t, f, tricky(), Options{})))
		r.Comma = comma
		got, err := r.ReadAll()
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", f, diff)
		}
	}
}

func TestTSVNoHeader(t *testing.T) {
	rs := &db.ResultSet{Columns: []db.ColumnInfo{{Name: "a"}, {Name: "b"}}, Rows: [][]db.Value{{{Text: "1"}, {Null: true}}}}
	if got := encode(t, TSV, rs, Options{NoHeader: true}); got != "1\t\n" {
		t.Fatalf("got %q", got)
	}
}

func TestJSON(t *testing.T) {
	out := encode(t, JSON, tricky(), Options{})
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	want := []map[string]any{
		{"id": 1.0, "name": "a\tb", "note": "line1\nline2", "data": map[string]any{"k": 1.0}, "ok": true},
		{"id": 2.0, "name": `say "hi"|x<y>`, "note": "", "data": nil, "ok": nil},
		{"id": 3.0, "name": "李雷", "note": `back\slash`, "data": "not json", "ok": false},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if strings.Index(out, `"id"`) > strings.Index(out, `"name"`) {
		t.Error("keys keep column order")
	}
	if !strings.Contains(out, "<y>") {
		t.Error("HTML characters are not escaped")
	}
	empty := encode(t, JSON, &db.ResultSet{Columns: []db.ColumnInfo{{Name: "a"}}}, Options{})
	if empty != "[]\n" {
		t.Fatalf("empty = %q", empty)
	}
}

func TestMarkdown(t *testing.T) {
	want := "| id | name | note | data | ok |\n" +
		"| --- | --- | --- | --- | --- |\n" +
		"| 1 | a b | line1<br>line2 | {\"k\": 1} | t |\n" +
		"| 2 | say \"hi\"\\|x<y> |  | NULL | NULL |\n" +
		"| 3 | 李雷 | back\\slash | not json | f |\n"
	if got := encode(t, Markdown, tricky(), Options{}); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEncodeRejectsXLSXAndUnknown(t *testing.T) {
	if _, err := Encode(XLSX, tricky(), Options{}); err == nil {
		t.Fatal("xlsx is a file format")
	}
	if _, err := Encode("yaml", tricky(), Options{}); err == nil {
		t.Fatal("unknown format")
	}
}

func TestTypeHelpers(t *testing.T) {
	for _, typ := range []string{"INT", "UNSIGNED BIGINT", "DECIMAL", "int8", "numeric", "float8", "DOUBLE"} {
		if !isNumericType(typ) {
			t.Errorf("%s is numeric", typ)
		}
	}
	for _, typ := range []string{"interval", "VARCHAR", "text", "point"} {
		if isNumericType(typ) {
			t.Errorf("%s is not numeric", typ)
		}
	}
	for s, want := range map[string]bool{"1": true, "-2.5": true, "1e10": true, "NaN": false, "Infinity": false, "1.": false, "": false} {
		if isNumber(s) != want {
			t.Errorf("isNumber(%q) != %v", s, want)
		}
	}
	if !isBinaryType("VARBINARY") || !isBinaryType("LONGBLOB") || isBinaryType("bytea") {
		t.Error("isBinaryType")
	}
}
