package importer

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// Inferrer guesses the column types of a new table from the file's rows.
type Inferrer struct {
	mysql bool
	null  string
	rows  int
	cols  []colStats
}

// colStats is what one column's values have in common.
type colStats struct {
	values    int
	notInt    bool
	notNum    bool
	exp       bool
	notDate   bool
	notStamp  bool // neither a date nor a date and time
	fracSec   bool
	intDigits int
	scale     int
	maxLen    int
}

// NewInferrer counts rows for a table on driver; null is the text of an
// empty cell.
func NewInferrer(driver, null string) *Inferrer {
	return &Inferrer{mysql: driver == "mysql", null: null}
}

// Add counts one data row; blank rows are skipped, as the import skips them.
func (in *Inferrer) Add(cells []string) {
	if blank(cells) {
		return
	}
	in.rows++
	for len(in.cols) < len(cells) {
		in.cols = append(in.cols, colStats{})
	}
	for i, c := range cells {
		in.cols[i].add(c, in.null)
	}
}

func (c *colStats) add(raw, null string) {
	s := strings.TrimSpace(raw)
	if s == "" || raw == null || s == null {
		return
	}
	c.values++
	c.maxLen = max(c.maxLen, utf8.RuneCountInString(raw))
	n := stripThousands(s)
	switch {
	case !numberRe.MatchString(n):
		c.notInt, c.notNum = true, true
	case strings.ContainsAny(n, "eE"):
		c.notInt, c.exp = true, true
	default:
		whole, frac, _ := strings.Cut(strings.TrimLeft(n, "+-"), ".")
		if len(whole) > 1 && whole[0] == '0' {
			c.notInt, c.notNum = true, true // 007: an identifier, not a number
			break
		}
		frac = strings.TrimRight(frac, "0")
		digits := len(strings.TrimLeft(whole, "0"))
		if frac != "" || digits > 18 {
			c.notInt = true
		}
		c.intDigits, c.scale = max(c.intDigits, digits), max(c.scale, len(frac))
	}
	_, isDate := parseDate(s)
	_, _, isStamp := parseDateTime(s, false)
	if !isDate {
		c.notDate = true
	}
	if !isDate && !isStamp {
		c.notStamp = true
	}
	if isStamp && strings.Contains(s, ".") {
		c.fracSec = true
	}
}

// Schema is a table called ref with one column per name, typed from the
// rows added. A column is NOT NULL only when every row had a value for it.
func (in *Inferrer) Schema(ref db.TableRef, names []string) db.TableSchema {
	s := db.TableSchema{Ref: ref}
	for i, name := range names {
		var c colStats
		if i < len(in.cols) {
			c = in.cols[i]
		}
		s.Columns = append(s.Columns, db.Column{
			Name: name, Type: in.typeOf(c), Nullable: c.values == 0 || c.values < in.rows, Position: i + 1,
		})
	}
	return s
}

func (in *Inferrer) typeOf(c colStats) string {
	maxPrecision, maxScale, numeric, double, stamp, text := 1000, 1000, "numeric", "double precision", "timestamp", "text"
	if in.mysql {
		maxPrecision, maxScale, numeric, double, stamp, text = 65, 30, "decimal", "double", "datetime", "varchar(255)"
		if c.fracSec {
			stamp = "datetime(6)"
		}
	}
	switch {
	case c.values == 0:
		return text
	case !c.notInt:
		return "bigint"
	case !c.notNum && !c.exp && c.intDigits+c.scale <= maxPrecision && c.scale <= maxScale:
		return fmt.Sprintf("%s(%d,%d)", numeric, max(c.intDigits+c.scale, 1), c.scale)
	case !c.notNum:
		return double
	case !c.notDate:
		return "date"
	case !c.notStamp:
		return stamp
	case c.maxLen <= 255:
		return fmt.Sprintf("varchar(%d)", min(255, max(16, (c.maxLen+15)/16*16)))
	}
	return "text"
}
