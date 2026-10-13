package export

import (
	"fmt"
	"io"
	"strconv"

	"github.com/xuri/excelize/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

const (
	xlsxMaxRows      = 1048576
	xlsxMaxCellChars = 32767
	// Larger integers lose precision as float64, so they stay text.
	xlsxMaxDigits = 15
)

type xlsxWriter struct {
	out  io.Writer
	f    *excelize.File
	sw   *excelize.StreamWriter
	cols []db.ColumnInfo
	row  int
}

func newXLSX(w io.Writer) (*xlsxWriter, error) {
	f := excelize.NewFile()
	sw, err := f.NewStreamWriter("Sheet1")
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &xlsxWriter{out: w, f: f, sw: sw}, nil
}

func (x *xlsxWriter) setRow(vals []any) error {
	x.row++
	if x.row > xlsxMaxRows {
		return fmt.Errorf("xlsx holds at most %d rows", xlsxMaxRows)
	}
	cell, err := excelize.CoordinatesToCellName(1, x.row)
	if err != nil {
		return err
	}
	return x.sw.SetRow(cell, vals)
}

func (x *xlsxWriter) WriteHeader(cols []db.ColumnInfo) error {
	x.cols = cols
	vals := make([]any, len(cols))
	for i, c := range cols {
		vals[i] = c.Name
	}
	return x.setRow(vals)
}

func (x *xlsxWriter) WriteRow(row []db.Value) error {
	vals := make([]any, len(row))
	for i, v := range row {
		vals[i] = x.cell(x.cols[i], v)
	}
	return x.setRow(vals)
}

func (x *xlsxWriter) cell(c db.ColumnInfo, v db.Value) any {
	if v.Null {
		return nil
	}
	if isNumericType(c.DatabaseType) && isNumber(v.Text) && len(v.Text) <= xlsxMaxDigits {
		if f, err := strconv.ParseFloat(v.Text, 64); err == nil {
			return f
		}
	}
	if r := []rune(v.Text); len(r) > xlsxMaxCellChars {
		return string(r[:xlsxMaxCellChars])
	}
	return v.Text
}

func (x *xlsxWriter) Close() error {
	defer func() { _ = x.f.Close() }()
	if err := x.sw.Flush(); err != nil {
		return err
	}
	_, err := x.f.WriteTo(x.out)
	return err
}
