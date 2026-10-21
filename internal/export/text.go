package export

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
)

type delimited struct {
	cw       *csv.Writer
	noHeader bool
}

func newDelimited(w io.Writer, comma rune, o Options) *delimited {
	cw := csv.NewWriter(w)
	cw.Comma = comma
	return &delimited{cw: cw, noHeader: o.NoHeader}
}

func (d *delimited) WriteHeader(cols []db.ColumnInfo) error {
	if d.noHeader {
		return nil
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	return d.cw.Write(names)
}

func (d *delimited) WriteRow(row []db.Value) error {
	rec := make([]string, len(row))
	for i, v := range row {
		rec[i] = v.Text // NULL is an empty field
	}
	return d.cw.Write(rec)
}

func (d *delimited) Close() error {
	d.cw.Flush()
	return d.cw.Error()
}

type jsonWriter struct {
	w    *bufio.Writer
	cols []db.ColumnInfo
	keys []string
	n    int
}

func newJSON(w io.Writer) *jsonWriter { return &jsonWriter{w: bufio.NewWriter(w)} }

func (j *jsonWriter) WriteHeader(cols []db.ColumnInfo) error {
	j.cols = cols
	j.keys = make([]string, len(cols))
	for i, c := range cols {
		j.keys[i] = jsonString(c.Name)
	}
	return nil
}

func (j *jsonWriter) WriteRow(row []db.Value) error {
	if j.n == 0 {
		_, _ = j.w.WriteString("[\n  {")
	} else {
		_, _ = j.w.WriteString(",\n  {")
	}
	j.n++
	for i, v := range row {
		if i > 0 {
			_, _ = j.w.WriteString(", ")
		}
		_, _ = j.w.WriteString(j.keys[i])
		_, _ = j.w.WriteString(": ")
		_, _ = j.w.WriteString(jsonValue(j.cols[i], v))
	}
	_, err := j.w.WriteString("}")
	return err
}

func (j *jsonWriter) Close() error {
	if j.n == 0 {
		_, _ = j.w.WriteString("[]\n")
	} else {
		_, _ = j.w.WriteString("\n]\n")
	}
	return j.w.Flush()
}

func jsonValue(c db.ColumnInfo, v db.Value) string {
	switch {
	case v.Null:
		return "null"
	case isNumericType(c.DatabaseType) && isNumber(v.Text):
		return v.Text
	case isBoolType(c.DatabaseType) && (v.Text == "t" || v.Text == "f"):
		if v.Text == "t" {
			return "true"
		}
		return "false"
	case isJSONType(c.DatabaseType) && json.Valid([]byte(v.Text)):
		var b bytes.Buffer
		if err := json.Compact(&b, []byte(v.Text)); err == nil {
			return b.String()
		}
	}
	return jsonString(v.Text)
}

// jsonString encodes s without escaping <, > and &.
func jsonString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

type markdown struct{ w *bufio.Writer }

func newMarkdown(w io.Writer) *markdown { return &markdown{w: bufio.NewWriter(w)} }

func (m *markdown) line(cells []string) error {
	_, err := m.w.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	return err
}

func (m *markdown) WriteHeader(cols []db.ColumnInfo) error {
	names := make([]string, len(cols))
	rule := make([]string, len(cols))
	for i, c := range cols {
		names[i] = mdCell(c.Name)
		rule[i] = "---"
	}
	if err := m.line(names); err != nil {
		return err
	}
	return m.line(rule)
}

func (m *markdown) WriteRow(row []db.Value) error {
	cells := make([]string, len(row))
	for i, v := range row {
		if v.Null {
			cells[i] = "NULL"
		} else {
			cells[i] = mdCell(v.Text)
		}
	}
	return m.line(cells)
}

func (m *markdown) Close() error { return m.w.Flush() }

var mdEscaper = strings.NewReplacer("|", `\|`, "\r\n", "<br>", "\n", "<br>", "\r", "", "\t", " ")

func mdCell(s string) string { return mdEscaper.Replace(s) }

// jsonlWriter writes one object per line; a lone JSON-typed column
// holding an object is the line itself.
type jsonlWriter struct {
	w    *bufio.Writer
	cols []db.ColumnInfo
	keys []string
}

func newJSONL(w io.Writer) *jsonlWriter { return &jsonlWriter{w: bufio.NewWriter(w)} }

func (j *jsonlWriter) WriteHeader(cols []db.ColumnInfo) error {
	j.cols = cols
	j.keys = make([]string, len(cols))
	for i, c := range cols {
		j.keys[i] = jsonString(c.Name)
	}
	return nil
}

func (j *jsonlWriter) WriteRow(row []db.Value) error {
	if len(row) == 1 && isJSONType(j.cols[0].DatabaseType) && !row[0].Null && strings.HasPrefix(strings.TrimSpace(row[0].Text), "{") {
		if v := jsonValue(j.cols[0], row[0]); strings.HasPrefix(v, "{") {
			_, err := j.w.WriteString(v + "\n")
			return err
		}
	}
	_, _ = j.w.WriteString("{")
	for i, v := range row {
		if i > 0 {
			_, _ = j.w.WriteString(", ")
		}
		_, _ = j.w.WriteString(j.keys[i] + ": " + jsonValue(j.cols[i], v))
	}
	_, err := j.w.WriteString("}\n")
	return err
}

func (j *jsonlWriter) Close() error { return j.w.Flush() }
