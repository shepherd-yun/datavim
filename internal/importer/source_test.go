package importer

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func writeFile(t *testing.T, name, data string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

type readOut struct {
	Header []string
	Rows   [][]string
	Lines  []int
}

func readAll(t *testing.T, path string, o Options) readOut {
	t.Helper()
	s, err := Open(path, o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	out := readOut{Header: s.Header()}
	for {
		row, err := s.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out.Rows = append(out.Rows, row)
		out.Lines = append(out.Lines, s.Line())
	}
}

func TestDefaultOptions(t *testing.T) {
	for path, want := range map[string]Format{"a.csv": CSV, "a.TSV": TSV, "a.tab": TSV, "a.xlsx": XLSX, "a.txt": CSV, "a": CSV} {
		o := DefaultOptions(path)
		if o.Format != want || o.Delimiter != ',' || o.Encoding != UTF8 || o.HeaderRow != 1 {
			t.Errorf("DefaultOptions(%q) = %+v", path, o)
		}
	}
}

func TestOpenDelimited(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().String("名字,城市\n张三,北京\n")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		file string
		data string
		opts func(*Options)
		want readOut
	}{
		{
			"header", "a.csv", "id,name\n1,Alice\n2,Bob\n", nil,
			readOut{[]string{"id", "name"}, [][]string{{"1", "Alice"}, {"2", "Bob"}}, []int{2, 3}},
		},
		{
			"quotes and a newline in a field", "a.csv", "id,note\n1,\"a, \"\"b\"\"\nc\"\n2,x\n", nil,
			readOut{[]string{"id", "note"}, [][]string{{"1", "a, \"b\"\nc"}, {"2", "x"}}, []int{2, 4}},
		},
		{
			"utf-8 bom", "a.csv", "\ufeffid,name\n1,a\n", nil,
			readOut{[]string{"id", "name"}, [][]string{{"1", "a"}}, []int{2}},
		},
		{
			"semicolon", "a.csv", "id;name\n1;a\n", func(o *Options) { o.Delimiter = ';' },
			readOut{[]string{"id", "name"}, [][]string{{"1", "a"}}, []int{2}},
		},
		{
			"tsv", "a.tsv", "id\tname\n1\ta b\n", nil,
			readOut{[]string{"id", "name"}, [][]string{{"1", "a b"}}, []int{2}},
		},
		{
			"gbk", "a.csv", gbk, func(o *Options) { o.Encoding = GBK },
			readOut{[]string{"名字", "城市"}, [][]string{{"张三", "北京"}}, []int{2}},
		},
		{
			"no header", "a.csv", "1,a\n2,b\n", func(o *Options) { o.HeaderRow = 0 },
			readOut{[]string{"column_1", "column_2"}, [][]string{{"1", "a"}, {"2", "b"}}, []int{1, 2}},
		},
		{
			"header on row 2", "a.csv", "exported by x\nid,name\n1,a\n", func(o *Options) { o.HeaderRow = 2 },
			readOut{[]string{"id", "name"}, [][]string{{"1", "a"}}, []int{3}},
		},
		{
			"blank and padded header cells, ragged rows", "a.csv", " id ,,name\n1\n2,b,c,d\n", nil,
			readOut{[]string{"id", "column_2", "name"}, [][]string{{"1"}, {"2", "b", "c", "d"}}, []int{2, 3}},
		},
		{
			"empty file without a header", "a.csv", "", func(o *Options) { o.HeaderRow = 0 },
			readOut{Header: []string{}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := writeFile(t, tt.file, tt.data)
			o := DefaultOptions(p)
			if tt.opts != nil {
				tt.opts(&o)
			}
			if diff := cmp.Diff(tt.want, readAll(t, p, o), cmpopts.EquateEmpty()); diff != "" {
				t.Fatalf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing.csv"), DefaultOptions("x.csv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	p := writeFile(t, "a.csv", "id\n1\n")
	o := DefaultOptions(p)
	o.HeaderRow = 5
	if _, err := Open(p, o); err == nil || !strings.Contains(err.Error(), "fewer than 5 rows") {
		t.Fatalf("header row past the end: %v", err)
	}
}
