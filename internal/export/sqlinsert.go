package export

import (
	"bufio"
	"io"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

type insertWriter struct {
	w      *bufio.Writer
	driver string
	table  []string
	cols   []db.ColumnInfo
	head   string
}

func newInsert(w io.Writer, o Options) *insertWriter {
	table := o.Table
	if len(table) == 0 {
		table = []string{"query"}
	}
	return &insertWriter{w: bufio.NewWriter(w), driver: o.Driver, table: table}
}

func (i *insertWriter) ident(s string) string { return sqlx.QuoteIdent(sqlx.DialectOf(i.driver), s) }

func (i *insertWriter) WriteHeader(cols []db.ColumnInfo) error {
	i.cols = cols
	parts := make([]string, len(i.table))
	for k, p := range i.table {
		parts[k] = i.ident(p)
	}
	names := make([]string, len(cols))
	for k, c := range cols {
		names[k] = i.ident(c.Name)
	}
	i.head = "INSERT INTO " + strings.Join(parts, ".") + " (" + strings.Join(names, ", ") + ") VALUES ("
	return nil
}

func (i *insertWriter) WriteRow(row []db.Value) error {
	vals := make([]string, len(row))
	for k, v := range row {
		vals[k] = i.literal(i.cols[k], v)
	}
	_, err := i.w.WriteString(i.head + strings.Join(vals, ", ") + ");\n")
	return err
}

func (i *insertWriter) literal(c db.ColumnInfo, v db.Value) string {
	return sqlx.Literal(sqlx.DialectOf(i.driver), c.DatabaseType, v)
}

func (i *insertWriter) Close() error { return i.w.Flush() }
