// Package importer reads CSV, TSV and XLSX files and writes their rows into
// a table: cell conversion, type inference for a new table, batched INSERTs
// in one transaction. It does no terminal IO.
package importer

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// Format is a file format.
type Format string

const (
	CSV  Format = "csv"
	TSV  Format = "tsv"
	XLSX Format = "xlsx"
)

// Encoding is the text encoding of a CSV or TSV file.
type Encoding string

const (
	UTF8 Encoding = "UTF-8"
	GBK  Encoding = "GBK"
)

// Options say how to read a file.
type Options struct {
	Format    Format
	Delimiter rune     // CSV only; TSV always splits on tabs
	Encoding  Encoding // CSV and TSV
	HeaderRow int      // 1-based row holding the column names; 0: none
	Sheet     string   // XLSX; "" is the first sheet
}

// DefaultOptions guesses the options from the file name: comma, UTF-8,
// column names on the first row.
func DefaultOptions(path string) Options {
	o := Options{Format: CSV, Delimiter: ',', Encoding: UTF8, HeaderRow: 1}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tsv", ".tab":
		o.Format = TSV
	case ".xlsx":
		o.Format = XLSX
	}
	return o
}

// Source reads the data rows of a file.
type Source interface {
	// Header is the column names: the header row, or column_1, column_2…
	// (as wide as the first row) without one.
	Header() []string
	// Next returns the next data row, or io.EOF.
	Next() ([]string, error)
	// Line is the 1-based line (CSV, TSV) or row (XLSX) where the row Next
	// returned last starts.
	Line() int
	Close() error
}

// Open opens path for reading as o says.
func Open(path string, o Options) (Source, error) {
	var r rowReader
	var err error
	if o.Format == XLSX {
		r, err = openXLSX(path, o.Sheet)
	} else {
		r, err = openDelimited(path, o)
	}
	if err != nil {
		return nil, err
	}
	s, err := newSource(r, o.HeaderRow)
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	return s, nil
}

// rowReader is every row of a file, the header included.
type rowReader interface {
	Read() ([]string, error)
	Line() int
	Close() error
}

// source skips to the header and hands out the rows after it.
type source struct {
	r           rowReader
	header      []string
	pending     []string // the first data row when there is no header row
	pendingLine int
	line        int
}

func newSource(r rowReader, headerRow int) (*source, error) {
	s := &source{r: r}
	if headerRow == 0 {
		row, err := r.Read()
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		s.pending, s.pendingLine = row, r.Line()
		s.header = columnNames(make([]string, len(row)))
		return s, nil
	}
	for i := 1; i <= headerRow; i++ {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the file has fewer than %d rows", headerRow)
		}
		if err != nil {
			return nil, err
		}
		if i == headerRow {
			s.header = columnNames(row)
		}
	}
	return s, nil
}

// columnNames trims the header cells and names the blank ones column_N.
func columnNames(row []string) []string {
	out := make([]string, len(row))
	for i, h := range row {
		out[i] = strings.TrimSpace(h)
		if out[i] == "" {
			out[i] = "column_" + strconv.Itoa(i+1)
		}
	}
	return out
}

func (s *source) Header() []string { return s.header }
func (s *source) Line() int        { return s.line }
func (s *source) Close() error     { return s.r.Close() }

func (s *source) Next() ([]string, error) {
	if s.pending != nil {
		row := s.pending
		s.pending, s.line = nil, s.pendingLine
		return row, nil
	}
	row, err := s.r.Read()
	if err != nil {
		return nil, err
	}
	s.line = s.r.Line()
	return row, nil
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// delimited reads CSV or TSV.
type delimited struct {
	f    *os.File
	r    *csv.Reader
	line int
}

func openDelimited(path string, o Options) (*delimited, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(f)
	if b, _ := br.Peek(len(utf8BOM)); bytes.Equal(b, utf8BOM) {
		_, _ = br.Discard(len(utf8BOM))
	}
	var in io.Reader = br
	if o.Encoding == GBK {
		in = transform.NewReader(br, simplifiedchinese.GBK.NewDecoder())
	}
	r := csv.NewReader(in)
	r.Comma = o.Delimiter
	if o.Format == TSV {
		r.Comma = '\t'
	}
	if r.Comma == 0 {
		r.Comma = ','
	}
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	return &delimited{f: f, r: r}, nil
}

func (d *delimited) Read() ([]string, error) {
	row, err := d.r.Read()
	if err == nil {
		d.line, _ = d.r.FieldPos(0)
	}
	return row, err
}

func (d *delimited) Line() int    { return d.line }
func (d *delimited) Close() error { return d.f.Close() }
