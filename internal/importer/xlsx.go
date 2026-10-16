package importer

import (
	"errors"
	"io"

	"github.com/xuri/excelize/v2"
)

// xlsxOptions make date cells read as ISO text (2026-01-02, 2026-01-02 12:00).
var xlsxOptions = excelize.Options{ShortDatePattern: "yyyy-mm-dd", LongTimePattern: "hh:mm:ss"}

// xlsxReader streams the formatted rows of one sheet. Rows missing from the
// sheet come back empty, so Line is the sheet's row number.
type xlsxReader struct {
	f    *excelize.File
	rows *excelize.Rows
	line int
}

func openXLSX(path, sheet string) (*xlsxReader, error) {
	f, err := excelize.OpenFile(path, xlsxOptions)
	if err != nil {
		return nil, err
	}
	if sheet == "" {
		list := f.GetSheetList()
		if len(list) == 0 {
			_ = f.Close()
			return nil, errors.New("the workbook has no sheets")
		}
		sheet = list[0]
	}
	rows, err := f.Rows(sheet)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &xlsxReader{f: f, rows: rows}, nil
}

func (x *xlsxReader) Read() ([]string, error) {
	if !x.rows.Next() {
		if err := x.rows.Error(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	x.line++
	return x.rows.Columns()
}

func (x *xlsxReader) Line() int { return x.line }

func (x *xlsxReader) Close() error {
	return errors.Join(x.rows.Close(), x.f.Close())
}

// Sheets lists the sheets of an XLSX file in workbook order.
func Sheets(path string) ([]string, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.GetSheetList(), nil
}
