package export

import (
	"bytes"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/xuri/excelize/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func TestXLSX(t *testing.T) {
	rs := &db.ResultSet{
		Columns: []db.ColumnInfo{{Name: "id", DatabaseType: "INT"}, {Name: "name", DatabaseType: "VARCHAR"}},
		Rows: [][]db.Value{
			{{Text: "1"}, {Text: "李雷"}},
			{{Text: "12345678901234567890"}, {Null: true}},
		},
	}
	var buf bytes.Buffer
	w, err := New(XLSX, &buf, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteResult(w, rs); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	rows, err := f.GetRows("Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"id", "name"}, {"1", "李雷"}, {"12345678901234567890"}}
	if diff := cmp.Diff(want, rows); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}
