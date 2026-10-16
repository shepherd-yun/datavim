package importer

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// Kind groups column types by how a cell is converted for them.
type Kind int

const (
	KindString Kind = iota
	KindInteger
	KindDecimal // decimal, numeric: precision and scale are checked
	KindFloat
	KindBool
	KindDate
	KindDateTime
	KindTime
	KindOther // passed through for the server to check
)

// Target is a column rows are written to.
type Target struct {
	Name string
	Type string
	Kind Kind
	// Nullable: an empty cell is NULL. Default: otherwise it is DEFAULT
	// (the column has a default or is auto-increment).
	Nullable, Default bool
	Unsigned          bool
	Bits              int // integer width: 8, 16, 24, 32 or 64
	Precision, Scale  int // the type's arguments; decimal(p,s), 0 when absent
	Length            int // char and varchar: the limit in characters
	TZ                bool
	MySQL             bool // booleans are written as 1 and 0
}

var typeRe = regexp.MustCompile(`^([a-z][a-z0-9 ]*?)\s*(?:\(\s*(\d+)\s*(?:,\s*(\d+)\s*)?\))?(\s+unsigned)?(?:\s+zerofill)?$`)

// TargetOf describes column c of a table on driver ("mysql" or "postgres").
func TargetOf(driver string, c db.Column) Target {
	t := Target{
		Name: c.Name, Type: c.Type, Nullable: c.Nullable,
		Default: c.Default != nil || c.AutoIncrement, MySQL: driver == "mysql",
	}
	typ := strings.ToLower(strings.TrimSpace(c.Type))
	if strings.HasSuffix(typ, " with time zone") {
		t.TZ = true
	}
	typ = strings.NewReplacer(" without time zone", "", " with time zone", "").Replace(typ)
	if strings.HasPrefix(typ, "enum(") || strings.HasPrefix(typ, "set(") {
		return t // KindString
	}
	m := typeRe.FindStringSubmatch(typ)
	if m == nil {
		t.Kind = KindOther
		return t
	}
	base := m[1]
	t.Precision, _ = strconv.Atoi(m[2])
	t.Scale, _ = strconv.Atoi(m[3])
	t.Unsigned = m[4] != ""
	switch base {
	case "tinyint":
		if t.MySQL && m[2] == "1" {
			t.Kind, t.Precision = KindBool, 0
			return t
		}
		t.Kind, t.Bits = KindInteger, 8
	case "bool", "boolean":
		t.Kind = KindBool
	case "smallint", "int2", "smallserial", "serial2":
		t.Kind, t.Bits = KindInteger, 16
	case "mediumint":
		t.Kind, t.Bits = KindInteger, 24
	case "int", "integer", "int4", "serial", "serial4":
		t.Kind, t.Bits = KindInteger, 32
	case "bigint", "int8", "bigserial", "serial8":
		t.Kind, t.Bits = KindInteger, 64
	case "decimal", "numeric", "dec", "fixed":
		t.Kind = KindDecimal
	case "float", "double", "double precision", "real", "float4", "float8":
		t.Kind = KindFloat
	case "date":
		t.Kind = KindDate
	case "datetime", "timestamp", "timestamptz":
		t.Kind, t.TZ = KindDateTime, t.TZ || base == "timestamptz"
	case "time":
		t.Kind = KindTime
		if t.TZ {
			t.Kind, t.TZ = KindOther, false
		}
	case "char", "character", "varchar", "character varying", "nchar", "nvarchar", "bpchar":
		t.Kind, t.Length = KindString, t.Precision
	case "text", "tinytext", "mediumtext", "longtext", "citext":
		t.Kind = KindString
	default:
		t.Kind = KindOther
	}
	if t.Kind == KindInteger {
		t.Precision = 0 // int(11) is a display width
	}
	return t
}

// Cell is a converted value: text sent as a parameter, NULL or DEFAULT.
type Cell struct {
	Text    string
	Null    bool
	Default bool
}

const excelHint = "is not an integer (Excel keeps 15 digits and turns longer numbers into floats: format the column as text)"

// Convert turns the text of a cell into the value written to t; null is the
// text that stands for an empty cell. The error is the reason alone.
func Convert(t Target, cell, null string) (Cell, error) {
	text := cell
	if t.Kind != KindString && t.Kind != KindOther {
		text = strings.TrimSpace(cell)
	}
	if cell == null || text == null || text == "" && t.Kind != KindString {
		switch {
		case t.Nullable:
			return Cell{Null: true}, nil
		case t.Default:
			return Cell{Default: true}, nil
		}
		return Cell{}, errors.New("cannot be empty")
	}
	var out string
	var err error
	switch t.Kind {
	case KindInteger:
		out, err = convertInteger(t, text)
	case KindDecimal, KindFloat:
		out, err = convertNumber(t, text)
	case KindBool:
		out, err = convertBool(t, text)
	case KindDate:
		out, err = convertDate(text)
	case KindDateTime:
		out, err = convertDateTime(t, text)
	case KindTime:
		out, err = convertTime(text)
	case KindString:
		if n := utf8.RuneCountInString(text); t.Length > 0 && n > t.Length {
			err = fmt.Errorf("is %d characters, the column allows %d", n, t.Length)
		}
		out = text
	default:
		out = text
	}
	if err != nil {
		return Cell{}, err
	}
	return Cell{Text: out}, nil
}

var (
	thousandsRe = regexp.MustCompile(`^[+-]?\d{1,3}(,\d{3})+(\.\d*)?$`)
	numberRe    = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)
)

// stripThousands drops the commas of 1,234,567.5.
func stripThousands(s string) string {
	if thousandsRe.MatchString(s) {
		return strings.ReplaceAll(s, ",", "")
	}
	return s
}

func convertInteger(t Target, s string) (string, error) {
	s = stripThousands(s)
	if i := strings.IndexByte(s, '.'); i > 0 && strings.Trim(s[i+1:], "0") == "" {
		s = s[:i]
	}
	if t.Unsigned {
		if strings.HasPrefix(s, "-") && numberRe.MatchString(s) {
			return "", fmt.Errorf("is out of range for %s", t.Type)
		}
		v, err := strconv.ParseUint(strings.TrimPrefix(s, "+"), 10, t.Bits)
		if err != nil {
			return "", integerError(t, s, err)
		}
		return strconv.FormatUint(v, 10), nil
	}
	v, err := strconv.ParseInt(s, 10, t.Bits)
	if err != nil {
		return "", integerError(t, s, err)
	}
	return strconv.FormatInt(v, 10), nil
}

func integerError(t Target, s string, err error) error {
	switch {
	case errors.Is(err, strconv.ErrRange):
		return fmt.Errorf("is out of range for %s", t.Type)
	case strings.ContainsAny(s, "eE") && numberRe.MatchString(s):
		return errors.New(excelHint)
	}
	return errors.New("is not an integer")
}

func convertNumber(t Target, s string) (string, error) {
	s = stripThousands(s)
	if !numberRe.MatchString(s) {
		return "", errors.New("is not a number")
	}
	if t.Kind == KindFloat || t.Precision == 0 {
		return s, nil
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return "", errors.New("is not a number")
	}
	scale := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(t.Scale)), nil))
	if !new(big.Rat).Mul(r, scale).IsInt() {
		return "", fmt.Errorf("has more than %d decimal places", t.Scale)
	}
	out := r.FloatString(t.Scale)
	whole, _, _ := strings.Cut(strings.TrimPrefix(out, "-"), ".")
	if digits := len(strings.TrimLeft(whole, "0")); digits > t.Precision-t.Scale {
		return "", fmt.Errorf("has more than %d digits before the point", t.Precision-t.Scale)
	}
	return out, nil
}

func convertBool(t Target, s string) (string, error) {
	var v bool
	switch strings.ToLower(s) {
	case "true", "t", "yes", "y", "1":
		v = true
	case "false", "f", "no", "n", "0":
	default:
		return "", errors.New("is not a boolean")
	}
	switch {
	case t.MySQL && v:
		return "1", nil
	case t.MySQL:
		return "0", nil
	case v:
		return "true", nil
	}
	return "false", nil
}

const (
	dateFormat     = "2006-01-02"
	dateTimeFormat = "2006-01-02 15:04:05.999999"
	timeFormat     = "15:04:05.999999"
)

var (
	dateLayouts  = []string{"2006-1-2", "2006/1/2"}
	clockLayouts = []string{"15:4", "15:4:5"} // fractional seconds parse after :5
	zoneLayouts  = []string{"Z07:00", " Z07:00", "Z0700", "Z07"}
)

func parseDate(s string) (time.Time, bool) {
	for _, l := range dateLayouts {
		if v, err := time.Parse(l, s); err == nil {
			return v, true
		}
	}
	return time.Time{}, false
}

// parseDateTime parses a date, a space or T, and a time of day; with zone
// set it also accepts a trailing zone and reports whether there was one.
func parseDateTime(s string, zone bool) (v time.Time, zoned, ok bool) {
	for _, d := range dateLayouts {
		for _, sep := range []string{" ", "T"} {
			for _, c := range clockLayouts {
				l := d + sep + c
				if v, err := time.Parse(l, s); err == nil {
					return v, false, true
				}
				if !zone {
					continue
				}
				for _, z := range zoneLayouts {
					if v, err := time.Parse(l+z, s); err == nil {
						return v, true, true
					}
				}
			}
		}
	}
	return time.Time{}, false, false
}

func convertDate(s string) (string, error) {
	if v, ok := parseDate(s); ok {
		return v.Format(dateFormat), nil
	}
	if v, _, ok := parseDateTime(s, false); ok && v.Hour() == 0 && v.Minute() == 0 && v.Second() == 0 && v.Nanosecond() == 0 {
		return v.Format(dateFormat), nil
	}
	return "", errors.New("is not a date")
}

func convertDateTime(t Target, s string) (string, error) {
	if v, ok := parseDate(s); ok {
		return v.Format(dateTimeFormat), nil
	}
	v, zoned, ok := parseDateTime(s, t.TZ)
	switch {
	case !ok:
		return "", errors.New("is not a date and time")
	case zoned:
		return v.Format(dateTimeFormat + "-07:00"), nil
	}
	return v.Format(dateTimeFormat), nil
}

func convertTime(s string) (string, error) {
	for _, l := range clockLayouts {
		if v, err := time.Parse(l, s); err == nil {
			return v.Format(timeFormat), nil
		}
	}
	return "", errors.New("is not a time")
}

// RowError is a file row that cannot be written.
type RowError struct {
	Line   int
	Column string // the file's name for the column; "" for the whole row
	Value  string
	Reason string
}

func (e *RowError) Error() string {
	if e.Column == "" {
		return fmt.Sprintf("line %d: %s", e.Line, e.Reason)
	}
	v := e.Value
	if r := []rune(v); len(r) > 40 {
		v = string(r[:40]) + "…"
	}
	return fmt.Sprintf("line %d, %s: %q %s", e.Line, e.Column, v, e.Reason)
}
