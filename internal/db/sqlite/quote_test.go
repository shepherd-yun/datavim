package sqlite

import (
	"errors"
	"os"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
)

const marker = "DV_0123456789abcdef"

// frame parses input, which ends with the marker line, with no header line.
func frame(t *testing.T, input string) ([][]db.Value, string) {
	t.Helper()
	var rows [][]db.Value
	f := newFrameReader(strings.NewReader(input + marker + "\n"))
	errText, err := f.read(marker, nil, func(r []db.Value) error {
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		t.Fatalf("read(%q): %v", input, err)
	}
	return rows, errText
}

func TestParseRows(t *testing.T) {
	tests := []struct {
		name, input string
		want        []db.Value
	}{
		{"numbers and null", "1,-2.5,1e+20,NULL\n", []db.Value{{Text: "1"}, {Text: "-2.5"}, {Text: "1e+20"}, {Null: true}}},
		{"string", "'it''s'\n", []db.Value{{Text: "it's"}}},
		{"empty string", "''\n", []db.Value{{Text: ""}}},
		{"string NULL", "'NULL'\n", []db.Value{{Text: "NULL"}}},
		{"blob", "x'00ff'\n", []db.Value{{Text: "x'00FF'", Blob: true}}},
		{"upper-case blob", "X'00FF'\n", []db.Value{{Text: "x'00FF'", Blob: true}}},
		{"empty blob", "x''\n", []db.Value{{Text: "x''", Blob: true}}},
		{"text like a blob", "'x''00ff'''\n", []db.Value{{Text: "x'00ff'"}}},
		{"big integer", "9007199254740993\n", []db.Value{{Text: "9007199254740993"}}},
		{"infinity", "Inf,-Inf,1e999\n", []db.Value{{Text: "9.0e+999"}, {Text: "-9.0e+999"}, {Text: "1e999"}}},
		{"unistr newline", `unistr('a\u000ab')` + "\n", []db.Value{{Text: "a\nb"}}},
		{"unistr backslash", `unistr('\\')` + "\n", []db.Value{{Text: `\`}}},
		{"unistr quote", `unistr('it''s\u000a')` + "\n", []db.Value{{Text: "it's\n"}}},
		{"unistr long form", `unistr('\U0001F600')` + "\n", []db.Value{{Text: "😀"}}},
		{"unistr in a row", `1,unistr('\u000d'),2` + "\n", []db.Value{{Text: "1"}, {Text: "\r"}, {Text: "2"}}},
		{"physical newline", "'a\nb'\n", []db.Value{{Text: "a\nb"}}},
		{"physical crlf", "'a\r\nb'\n", []db.Value{{Text: "a\r\nb"}}},
		{"replace", `replace(replace('a\nb\rc','\n',char(10)),'\r',char(13))` + "\n", []db.Value{{Text: "a\nb\rc"}}},
		{"replace once", `replace('a\012b','\012',char(10)),1` + "\n", []db.Value{{Text: "a\nb"}, {Text: "1"}}},
		{"same names", "'a','a'\n", []db.Value{{Text: "a"}, {Text: "a"}}},
		{"comma and paren", "',',')'\n", []db.Value{{Text: ","}, {Text: ")"}}},
		{"crlf line end", "1,2\r\n", []db.Value{{Text: "1"}, {Text: "2"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, errText := frame(t, tt.input)
			if errText != "" {
				t.Fatalf("errText = %q", errText)
			}
			if len(rows) != 1 {
				t.Fatalf("rows = %v", rows)
			}
			if diff := cmp.Diff(tt.want, rows[0]); diff != "" {
				t.Fatalf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseHeader(t *testing.T) {
	var header []string
	var rows [][]db.Value
	f := newFrameReader(strings.NewReader("'a','a b'\n1,2\n3,4\n" + marker + "\n"))
	errText, err := f.read(marker, func(h []string) { header = h }, func(r []db.Value) error {
		rows = append(rows, r)
		return nil
	})
	if err != nil || errText != "" {
		t.Fatal(err, errText)
	}
	if !cmp.Equal(header, []string{"a", "a b"}) || len(rows) != 2 {
		t.Fatalf("header %v rows %v", header, rows)
	}
}

func TestParseGolden(t *testing.T) {
	want := [][]db.Value{
		{
			{Text: "1"},
			{Text: "-2.5"},
			{Text: "1.0e+20"},
			{Null: true},
			{Text: "it's"},
			{Text: "x'00FF'", Blob: true},
			{Text: "x''", Blob: true},
			{Text: "9007199254740993"},
			{Text: "a\nb"},
			{Text: "a\r\nb"},
			{Text: `back\slash\n`},
			{Text: ""},
			{Text: "b\\\n'q"},
			{Text: "x'00ff'"},
		},
		{
			{Text: "2"},
			{Text: "3.0"},
			{Text: "-9.0e+999"},
			{Text: "NULL"},
			{Text: "李雷"},
			{Text: "x''", Blob: true},
			{Text: "x"},
			{Text: "0"},
			{Text: ""},
			{Text: "\r"},
			{Text: "\n"},
			{Text: "1.0e-07"},
			{Text: ","},
			{Text: ")"},
		},
	}
	header := []string{"a", "a", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l m", "n", "o"}
	for _, v := range []string{"3.26", "3.37", "3.54"} {
		t.Run(v, func(t *testing.T) {
			data, err := os.ReadFile("testdata/quote-" + v + ".txt")
			if err != nil {
				t.Fatal(err)
			}
			f := newFrameReader(strings.NewReader(string(data) + marker + "\n"))
			var gotHeader []string
			var rows [][]db.Value
			errText, err := f.read(marker, func(h []string) { gotHeader = h }, func(r []db.Value) error {
				rows = append(rows, r)
				return nil
			})
			if err != nil || errText != "" {
				t.Fatalf("%v %q", err, errText)
			}
			if diff := cmp.Diff(header, gotHeader); diff != "" {
				t.Errorf("header (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(want, rows); diff != "" {
				t.Errorf("rows (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseLongAndBinary(t *testing.T) {
	long := strings.Repeat("x", 10<<20)
	rows, _ := frame(t, "'"+long+"'\n")
	if len(rows) != 1 || rows[0][0].Text != long {
		t.Fatal("10 MB value did not survive")
	}
	raw := "a\xff\xfe'b"
	rows, _ = frame(t, "'a\xff\xfe''b'\n")
	if rows[0][0].Text != raw {
		t.Fatalf("got %q", rows[0][0].Text)
	}
	rows, _ = frame(t, `unistr('a`+"\xff"+`\u000a')`+"\n")
	if rows[0][0].Text != "a\xff\n" {
		t.Fatalf("unistr got %q", rows[0][0].Text)
	}
	// a reader that returns one byte at a time changes nothing
	var got [][]db.Value
	f := newFrameReader(iotest.OneByteReader(strings.NewReader("'a''b',x'01',NULL\n" + marker + "\n")))
	if _, err := f.read(marker, nil, func(r []db.Value) error { got = append(got, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([][]db.Value{{{Text: "a'b"}, {Text: "x'01'", Blob: true}, {Null: true}}}, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestMarkerLookalike(t *testing.T) {
	rows, errText := frame(t, "'"+marker+"'\n'a\n"+marker+"\nb'\n")
	if errText != "" || len(rows) != 2 {
		t.Fatalf("rows %v err %q", rows, errText)
	}
	if rows[0][0].Text != marker || rows[1][0].Text != "a\n"+marker+"\nb" {
		t.Fatalf("rows %v", rows)
	}
}

func TestErrorText(t *testing.T) {
	for _, line := range []string{
		"Parse error near line 1: x",
		"Error near line 3: no such table: t",
		"Runtime error: database is locked (5)",
		"1 thing went wrong",
		"NULLABLE is wrong",
		"Infinite loop",
		"'abc' is not a function",
		"unistr is odd",
		"x'zz'",
	} {
		rows, errText := frame(t, line+"\n")
		if len(rows) != 0 || errText != line {
			t.Errorf("%q: rows %v errText %q", line, rows, errText)
		}
	}
	rows, errText := frame(t, "1,2\nParse error: a\n  SELECT\n   ^--- here\n")
	if len(rows) != 1 || errText != "Parse error: a\n  SELECT\n   ^--- here" {
		t.Errorf("rows %v errText %q", rows, errText)
	}
}

func TestReaderEOF(t *testing.T) {
	for _, in := range []string{"", "1,2\n", "'unterminated", "partial"} {
		f := newFrameReader(strings.NewReader(in))
		_, err := f.read(marker, nil, func([]db.Value) error { return nil })
		if err == nil {
			t.Errorf("%q: want an error at the end of the stream", in)
		}
	}
}

func TestRowCallbackError(t *testing.T) {
	stop := errors.New("stop")
	f := newFrameReader(strings.NewReader("1\n2\n" + marker + "\n"))
	n := 0
	_, err := f.read(marker, nil, func([]db.Value) error { n++; return stop })
	if !errors.Is(err, stop) || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	// the rest of the frame can still be read and discarded
	if _, err := f.read(marker, nil, func([]db.Value) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
