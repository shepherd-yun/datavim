// Package export writes result sets as TSV, CSV, JSON, Markdown, SQL or XLSX.
package export

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
)

// Format is an output format; its value is also the file extension.
type Format string

const (
	TSV      Format = "tsv"
	CSV      Format = "csv"
	JSON     Format = "json"
	Markdown Format = "md"
	SQL      Format = "sql"
	XLSX     Format = "xlsx"
	// JSONL is one JSON object per line. A row whose only column is a
	// JSON-typed object is written as that object (a Kafka message).
	JSONL Format = "jsonl"
)

// Label is the human name shown in messages.
func (f Format) Label() string {
	switch f {
	case TSV:
		return "TSV"
	case CSV:
		return "CSV"
	case JSON:
		return "JSON"
	case Markdown:
		return "Markdown"
	case SQL:
		return "SQL INSERT"
	case XLSX:
		return "Excel"
	case JSONL:
		return "JSON Lines"
	}
	return string(f)
}

// Options tunes a writer.
type Options struct {
	NoHeader bool     // TSV and CSV: leave out the header line
	Driver   string   // SQL: the connection's driver name, selects quoting and literal rules
	Table    []string // SQL: target table name parts, e.g. {"app", "users"}
}

// Writer writes one result set. Close flushes but never closes the io.Writer.
type Writer interface {
	WriteHeader(cols []db.ColumnInfo) error
	WriteRow(row []db.Value) error
	Close() error
}

// New returns a writer for f.
func New(f Format, w io.Writer, o Options) (Writer, error) {
	switch f {
	case TSV:
		return newDelimited(w, '\t', o), nil
	case CSV:
		return newDelimited(w, ',', o), nil
	case JSON:
		return newJSON(w), nil
	case Markdown:
		return newMarkdown(w), nil
	case SQL:
		return newInsert(w, o), nil
	case XLSX:
		return newXLSX(w)
	case JSONL:
		return newJSONL(w), nil
	}
	return nil, fmt.Errorf("unknown export format %q", f)
}

// WriteRows writes rs through w without closing it (for ToFile's fill).
func WriteRows(w Writer, rs *db.ResultSet) error {
	if err := w.WriteHeader(rs.Columns); err != nil {
		return err
	}
	for _, r := range rs.Rows {
		if err := w.WriteRow(r); err != nil {
			return err
		}
	}
	return nil
}

// WriteResult writes rs through w and closes w.
func WriteResult(w Writer, rs *db.ResultSet) error {
	if err := WriteRows(w, rs); err != nil {
		return err
	}
	return w.Close()
}

// Encode renders rs as text in any format except XLSX.
func Encode(f Format, rs *db.ResultSet, o Options) (string, error) {
	if f == XLSX {
		return "", errors.New("xlsx can only be exported to a file")
	}
	var b strings.Builder
	w, err := New(f, &b, o)
	if err != nil {
		return "", err
	}
	if err := WriteResult(w, rs); err != nil {
		return "", err
	}
	return b.String(), nil
}
