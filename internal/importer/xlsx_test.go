package importer

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/xuri/excelize/v2"
)

// writeXLSX saves a workbook with sheets Data and Other. Data has a header,
// a date cell, a date-time cell, a formatted number, a gap row and a short row.
func writeXLSX(t *testing.T) string {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	if err := f.SetSheetName("Sheet1", "Data"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.NewSheet("Other"); err != nil {
		t.Fatal(err)
	}
	date, _ := f.NewStyle(&excelize.Style{NumFmt: 14})
	stamp, _ := f.NewStyle(&excelize.Style{NumFmt: 22})
	thousands, _ := f.NewStyle(&excelize.Style{NumFmt: 3})
	set := func(cell string, v any, style int) {
		t.Helper()
		if err := f.SetCellValue("Data", cell, v); err != nil {
			t.Fatal(err)
		}
		if style != 0 {
			if err := f.SetCellStyle("Data", cell, cell, style); err != nil {
				t.Fatal(err)
			}
		}
	}
	set("A1", "id", 0)
	set("B1", "born", 0)
	set("C1", "seen", 0)
	set("D1", "amount", 0)
	set("A2", 1, 0)
	set("B2", 46024, date)
	set("C2", 46024.5, stamp)
	set("D2", 1234567, thousands)
	set("A4", 2, 0)
	if err := f.SetCellValue("Other", "A1", "x"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "book.xlsx")
	if err := f.SaveAs(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpenXLSX(t *testing.T) {
	p := writeXLSX(t)
	got := readAll(t, p, DefaultOptions(p))
	want := readOut{
		Header: []string{"id", "born", "seen", "amount"},
		Rows:   [][]string{{"1", "2026-01-02", "2026-01-02 12:00", "1,234,567"}, {}, {"2"}},
		Lines:  []int{2, 3, 4},
	}
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	o := DefaultOptions(p)
	o.Sheet = "Other"
	if h := readAll(t, p, o).Header; !cmp.Equal(h, []string{"x"}) {
		t.Fatalf("Other header = %v", h)
	}
	o.Sheet = "Nope"
	if _, err := Open(p, o); err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("missing sheet: %v", err)
	}
}

func TestSheets(t *testing.T) {
	got, err := Sheets(writeXLSX(t))
	if err != nil {
		t.Fatal(err)
	}
	if !cmp.Equal(got, []string{"Data", "Other"}) {
		t.Fatalf("Sheets = %v", got)
	}
}
