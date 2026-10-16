package ui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/edit"
	"github.com/shepherd-yun/sqlvim/internal/export"
	"github.com/shepherd-yun/sqlvim/internal/rcmd"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
)

// keyExportPage is how many items a whole-key export reads at a time.
const keyExportPage = 1000

// exportCell is v as written to a file: a binary value (not UTF-8, or with
// control characters) rcmd.Escape'd, reporting that it was.
func exportCell(v db.Value) (db.Value, bool) {
	if v.Null || utf8.ValidString(v.Text) && !hasControl(v.Text) {
		return v, false
	}
	return db.Value{Text: rcmd.Escape(v.Text)}, true
}

// exportCells copies rs with every binary cell rcmd.Escape'd; n is how many.
func exportCells(rs *db.ResultSet) (out *db.ResultSet, n int) {
	out = &db.ResultSet{Columns: rs.Columns, Rows: make([][]db.Value, len(rs.Rows))}
	for i, r := range rs.Rows {
		row := make([]db.Value, len(r))
		for j, v := range r {
			var esc bool
			if row[j], esc = exportCell(v); esc {
				n++
			}
		}
		out.Rows[i] = row
	}
	return out, n
}

// itemUnit is what a key of typ holds, plural.
func itemUnit(typ string) string {
	return map[string]string{"string": "bytes", "hash": "fields", "list": "elements", "set": "members", "zset": "members", "stream": "entries"}[typ]
}

// exportSource is the key's value as loaded (staged changes included, as
// the grid shows it), and, when there are more pages, the whole key read
// page by page.
func (t *keyTab) exportSource() exportSrc {
	if !t.loaded || !t.info.Exists {
		return exportSrc{}
	}
	src := exportSrc{name: t.key, conn: t.conn}
	var notes []string
	if t.isString() {
		rs := &db.ResultSet{Columns: []db.ColumnInfo{{Name: "value"}}, Rows: [][]db.Value{{{Text: t.value()}}}}
		src.rs, src.escaped = exportCells(rs)
		if t.truncated() {
			notes = append(notes, "only the first 1 MB was loaded")
		}
		if t.staged != nil {
			notes = append(notes, "includes unsaved changes")
		}
		src.note = joinNotes(notes)
		return src
	}
	if t.table == nil {
		return exportSrc{} // module types are not read
	}
	src.rs, src.escaped = exportCells(currentRows(t.table))
	if t.table.Changes() > 0 {
		src.note = joinNotes([]string{"includes unsaved changes"})
	}
	if t.more {
		src.wholeLabel = fmt.Sprintf("Whole key (%s %s)", groupDigits(t.info.Len), itemUnit(t.info.Type))
		src.stream = t.streamWhole()
	}
	return src
}

func joinNotes(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return " · " + strings.Join(notes, " · ")
}

// currentRows are the rows as they will be once the staged changes are
// submitted: deleted rows left out, new ones in.
func currentRows(tb *edit.Table) *db.ResultSet {
	rs := &db.ResultSet{Columns: tb.Columns()}
	for i := range tb.Len() {
		if r := tb.Row(i); r.State != edit.Deleted {
			rs.Rows = append(rs.Rows, r.Values)
		}
	}
	return rs
}

// streamWhole reads the key from the start, keyExportPage items at a time
// (pausing between pages on prod), and writes every item; hash fields and
// set members HSCAN / SSCAN return twice are written once.
func (t *keyTab) streamWhole() func(ctx context.Context, w export.Writer, rows, escaped *int) error {
	b, conn, n, key, info := t.backend, t.conn, t.db, t.key, t.info
	cfg, _ := b.Connection(conn)
	prod := cfg.Env == config.EnvProd
	dedup := info.Type == "hash" || info.Type == "set"
	return func(ctx context.Context, w export.Writer, rows, escaped *int) error {
		*rows, *escaped = 0, 0
		return withRedis(ctx, b, conn, n, func(c rdb.Client) error {
			*rows, *escaped = 0, 0
			seen := map[string]bool{}
			header := false
			var next uint64
			var start int64
			var after string
			for page := 0; ; page++ {
				if page > 0 && prod {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(prodScanPause):
					}
				}
				var msg keyLoadedMsg
				if err := readKeyPage(ctx, c, key, info, &msg, next, start, after, "", keyExportPage); err != nil {
					return err
				}
				if msg.rs == nil {
					return nil
				}
				if !header {
					if err := w.WriteHeader(msg.rs.Columns); err != nil {
						return err
					}
					header = true
				}
				for _, r := range msg.rs.Rows {
					if dedup {
						if seen[r[0].Text] {
							continue
						}
						seen[r[0].Text] = true
					}
					out := make([]db.Value, len(r))
					for i, v := range r {
						var esc bool
						if out[i], esc = exportCell(v); esc {
							*escaped++
						}
					}
					if err := w.WriteRow(out); err != nil {
						return err
					}
					*rows++
				}
				if !msg.more {
					return nil
				}
				next, start, after = msg.next, msg.nextStart, msg.nextAfter
			}
		})
	}
}
