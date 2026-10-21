package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/rcmd"
	"github.com/shepherd-yun/datavim/internal/rdb"
)

// replyView is how one Redis console reply is shown: a table (Columns and
// Rows) or text Lines.
type replyView struct {
	Columns   []string
	Rows      [][]string // raw values (not escaped), for opening keys and copying
	Lines     []string   // the text when it is not a table, already escaped
	Truncated bool       // more than maxRows rows; only the first maxRows are kept
	Summary   string     // one line for the message log
}

// viewReply picks the display for r (Redis design §5.3): scalars as one
// value, scalar arrays and pair replies as tables, SCAN replies as a table
// of the keys, and anything else nested as redis-cli's indented text.
func viewReply(args []string, r rdb.Reply, maxRows int) replyView {
	cmd := ""
	if len(args) > 0 {
		cmd = strings.ToUpper(args[0])
	}
	switch r.Kind {
	case rdb.KindString:
		lines, _ := renderValue(r.Str, fmtAuto)
		return replyView{Lines: lines, Summary: clipText(firstLine(rcmd.Escape(r.Str)), 60)}
	case rdb.KindArray:
		if len(r.Elems) == 0 {
			return replyView{Lines: []string{"(empty array)"}, Summary: "(empty array)"}
		}
		if v, ok := scanView(cmd, r, maxRows); ok {
			return v
		}
		if pairs, ok := replyPairs(args, r); ok {
			return pairView(cmd, pairs, maxRows)
		}
		if allScalar(r.Elems) {
			cols := []string{"index", "value"}
			if cmd == "KEYS" {
				cols[1] = "key"
			}
			v := replyView{Columns: cols, Summary: fmt.Sprintf("%d items", len(r.Elems))}
			for i, e := range r.Elems {
				v.Rows = append(v.Rows, []string{strconv.Itoa(i + 1), replyCell(e)})
			}
			return v.clip(maxRows)
		}
		return replyView{Lines: treeLines(r), Summary: fmt.Sprintf("%d items", len(r.Elems))}
	case rdb.KindMap:
		if len(r.Pairs) == 0 {
			return replyView{Lines: []string{"(empty hash)"}, Summary: "(empty hash)"}
		}
		if allScalarPairs(r.Pairs) {
			return pairView(cmd, r.Pairs, maxRows)
		}
		return replyView{Lines: treeLines(r), Summary: fmt.Sprintf("%d fields", len(r.Pairs))}
	}
	s := scalarText(r)
	return replyView{Lines: []string{s}, Summary: s}
}

// replyPairs is r as pairs when the command is known to answer pairs:
// a flat even array (RESP2) or an array of two-element arrays (RESP3).
func replyPairs(args []string, r rdb.Reply) ([][2]rdb.Reply, bool) {
	if !rcmd.PairReply(args) {
		return nil, false
	}
	var out [][2]rdb.Reply
	if allScalar(r.Elems) {
		if len(r.Elems)%2 != 0 {
			return nil, false
		}
		for i := 0; i < len(r.Elems); i += 2 {
			out = append(out, [2]rdb.Reply{r.Elems[i], r.Elems[i+1]})
		}
		return out, true
	}
	for _, e := range r.Elems {
		if e.Kind != rdb.KindArray || len(e.Elems) != 2 || !allScalar(e.Elems) {
			return nil, false
		}
		out = append(out, [2]rdb.Reply{e.Elems[0], e.Elems[1]})
	}
	return out, true
}

func pairView(cmd string, pairs [][2]rdb.Reply, maxRows int) replyView {
	cols, noun := []string{"field", "value"}, "fields"
	if strings.HasPrefix(cmd, "Z") {
		cols, noun = []string{"member", "score"}, "members"
	}
	v := replyView{Columns: cols, Summary: fmt.Sprintf("%d %s", len(pairs), noun)}
	for _, p := range pairs {
		v.Rows = append(v.Rows, []string{replyCell(p[0]), replyCell(p[1])})
	}
	return v.clip(maxRows)
}

// scanView shows a SCAN-family reply [cursor, [items]] as a table of the
// items, with the cursor in the summary.
func scanView(cmd string, r rdb.Reply, maxRows int) (replyView, bool) {
	if cmd != "SCAN" && cmd != "SSCAN" && cmd != "HSCAN" && cmd != "ZSCAN" {
		return replyView{}, false
	}
	if len(r.Elems) != 2 || r.Elems[0].Kind != rdb.KindString || r.Elems[1].Kind != rdb.KindArray || !allScalar(r.Elems[1].Elems) {
		return replyView{}, false
	}
	cursor, items := r.Elems[0].Str, r.Elems[1].Elems
	var v replyView
	switch cmd {
	case "HSCAN", "ZSCAN":
		if len(items)%2 != 0 {
			return replyView{}, false
		}
		var pairs [][2]rdb.Reply
		for i := 0; i < len(items); i += 2 {
			pairs = append(pairs, [2]rdb.Reply{items[i], items[i+1]})
		}
		v = pairView(cmd, pairs, maxRows)
		v.Summary = fmt.Sprintf("cursor %s · %d %s", cursor, len(pairs), map[string]string{"HSCAN": "fields", "ZSCAN": "members"}[cmd])
		return v, true
	}
	col, noun := "key", "keys"
	if cmd == "SSCAN" {
		col, noun = "member", "members"
	}
	v = replyView{Columns: []string{"index", col}, Summary: fmt.Sprintf("cursor %s · %d %s", cursor, len(items), noun)}
	for i, e := range items {
		v.Rows = append(v.Rows, []string{strconv.Itoa(i + 1), replyCell(e)})
	}
	return v.clip(maxRows), true
}

func (v replyView) clip(maxRows int) replyView {
	if maxRows > 0 && len(v.Rows) > maxRows {
		v.Rows, v.Truncated = v.Rows[:maxRows], true
	}
	return v
}

// resultSet is the table for the grid; cells are escaped for display.
func (v replyView) resultSet() *db.ResultSet {
	rs := &db.ResultSet{Columns: make([]db.ColumnInfo, len(v.Columns))}
	for i, c := range v.Columns {
		rs.Columns[i] = db.ColumnInfo{Name: c}
	}
	for _, row := range v.Rows {
		out := make([]db.Value, len(row))
		for i, c := range row {
			out[i] = db.Value{Text: rcmd.Escape(c)}
		}
		rs.Rows = append(rs.Rows, out)
	}
	return rs
}

func isScalar(r rdb.Reply) bool { return r.Kind != rdb.KindArray && r.Kind != rdb.KindMap }

func allScalar(rs []rdb.Reply) bool {
	for _, r := range rs {
		if !isScalar(r) {
			return false
		}
	}
	return true
}

func allScalarPairs(ps [][2]rdb.Reply) bool {
	for _, p := range ps {
		if !isScalar(p[0]) || !isScalar(p[1]) {
			return false
		}
	}
	return true
}

// replyCell is a scalar as a raw table cell.
func replyCell(r rdb.Reply) string {
	switch r.Kind {
	case rdb.KindNil:
		return "(nil)"
	case rdb.KindError:
		return "(error) " + r.Str
	}
	return r.Text()
}

// scalarText is a scalar with redis-cli's type hint.
func scalarText(r rdb.Reply) string {
	switch r.Kind {
	case rdb.KindInt:
		return "(integer) " + r.Text()
	case rdb.KindDouble:
		return "(double) " + r.Text()
	case rdb.KindBool:
		return "(boolean) " + r.Text()
	case rdb.KindBigInt:
		return "(big number) " + r.Str
	case rdb.KindNil:
		return "(nil)"
	case rdb.KindError:
		return "(error) " + r.Str
	case rdb.KindString:
		// Status replies cannot be told from bulk strings after go-redis,
		// so the common ones are shown bare like redis-cli does.
		switch r.Str {
		case "OK", "QUEUED", "PONG":
			return r.Str
		}
		return `"` + strings.ReplaceAll(rcmd.Escape(r.Str), `"`, `\"`) + `"`
	}
	return ""
}

// treeLines is redis-cli's indented form: "1) …" for arrays, "1# k => v"
// for maps, nested replies indented under their number.
func treeLines(r rdb.Reply) []string {
	switch r.Kind {
	case rdb.KindArray:
		if len(r.Elems) == 0 {
			return []string{"(empty array)"}
		}
		var out []string
		w := len(strconv.Itoa(len(r.Elems)))
		for i, e := range r.Elems {
			out = appendNested(out, fmt.Sprintf("%*d) ", w, i+1), treeLines(e))
		}
		return out
	case rdb.KindMap:
		if len(r.Pairs) == 0 {
			return []string{"(empty hash)"}
		}
		var out []string
		w := len(strconv.Itoa(len(r.Pairs)))
		for i, p := range r.Pairs {
			prefix := fmt.Sprintf("%*d# ", w, i+1)
			key := strings.Join(treeLines(p[0]), " ")
			sub := treeLines(p[1])
			sub[0] = key + " => " + sub[0]
			out = appendNested(out, prefix, sub)
		}
		return out
	}
	return []string{scalarText(r)}
}

func appendNested(out []string, prefix string, sub []string) []string {
	pad := strings.Repeat(" ", len(prefix))
	for j, l := range sub {
		if j == 0 {
			out = append(out, prefix+l)
		} else {
			out = append(out, pad+l)
		}
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
