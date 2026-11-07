package sqlite

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shepherd-yun/datavim/internal/db"
)

// frameReader reads the output of a sqlite3 process in .mode quote: rows of
// SQL literals, and anything else (error messages), up to a marker line. The
// marker is a bare word, so a text value equal to it, being quoted, cannot end
// a frame. There is no limit on the length of a value or line.
type frameReader struct {
	r   *bufio.Reader
	raw []byte // the current record so far, for when it turns out not to be one
}

func newFrameReader(r io.Reader) *frameReader {
	return &frameReader{r: bufio.NewReaderSize(r, 64<<10)}
}

// errNotLiteral means the text is not a row: it is a message.
var errNotLiteral = errors.New("not a literal")

// read reads up to marker. The first record goes to onHeader, if set, and the
// others to onRow; an error from onRow is returned at once, leaving the rest of
// the frame to read. Everything from the first line that is no row on is
// errText. err is the stream's error, io.EOF when the process is gone.
func (f *frameReader) read(marker string, onHeader func([]string), onRow func([]db.Value) error) (errText string, err error) {
	var msg []string
	for {
		if f.atMarker(marker) {
			if _, err := f.r.Discard(len(marker) + 1); err != nil {
				return "", err
			}
			return strings.Join(msg, "\n"), nil
		}
		f.raw = f.raw[:0]
		var vals []db.Value
		rerr := errNotLiteral
		if len(msg) == 0 {
			vals, rerr = f.record()
		}
		switch {
		case errors.Is(rerr, errNotLiteral):
			line, err := f.r.ReadBytes('\n')
			f.raw = append(f.raw, line...)
			if err != nil {
				return strings.Join(msg, "\n"), err
			}
			msg = append(msg, strings.TrimRight(string(f.raw), "\r\n"))
		case rerr != nil:
			return strings.Join(msg, "\n"), rerr
		case onHeader != nil:
			names := make([]string, len(vals))
			for i, v := range vals {
				names[i] = v.Text
			}
			onHeader(names)
			onHeader = nil
		default:
			if err := onRow(vals); err != nil {
				return "", err
			}
		}
	}
}

func (f *frameReader) atMarker(marker string) bool {
	b, err := f.r.Peek(len(marker) + 1)
	return err == nil && string(b[:len(marker)]) == marker && b[len(marker)] == '\n'
}

// record reads one line of literals.
func (f *frameReader) record() ([]db.Value, error) {
	var out []db.Value
	for {
		v, err := f.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		c, err := f.next()
		if err != nil {
			return nil, err
		}
		switch c {
		case ',':
		case '\n':
			return out, nil
		case '\r':
			if c, err = f.next(); err != nil {
				return nil, err
			}
			if c != '\n' {
				return nil, errNotLiteral
			}
			return out, nil
		default:
			return nil, errNotLiteral
		}
	}
}

func (f *frameReader) next() (byte, error) {
	c, err := f.r.ReadByte()
	if err == nil {
		f.raw = append(f.raw, c)
	}
	return c, err
}

// expect consumes word if it comes next.
func (f *frameReader) expect(word string) bool {
	b, err := f.r.Peek(len(word))
	if err != nil || string(b) != word {
		return false
	}
	f.raw = append(f.raw, b...)
	_, _ = f.r.Discard(len(word))
	return true
}

var numberRe = regexp.MustCompile(`^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$`)

// value reads one literal. The next byte stays unread.
func (f *frameReader) value() (db.Value, error) {
	b, err := f.r.Peek(1)
	if err != nil {
		return db.Value{}, err
	}
	switch c := b[0]; {
	case c == '\'', c == 'u', c == 'r':
		s, err := f.text()
		return db.Value{Text: s}, err
	case c == 'x', c == 'X':
		return f.blob()
	case c == 'N':
		if !f.expect("NULL") || !f.atDelimiter() {
			return db.Value{}, errNotLiteral
		}
		return db.Value{Null: true}, nil
	case c == 'I', c == '-', c == '+', c == '.', c >= '0' && c <= '9':
		return f.number()
	}
	return db.Value{}, errNotLiteral
}

// atDelimiter reports that a value may end here.
func (f *frameReader) atDelimiter() bool {
	b, err := f.r.Peek(1)
	return err == nil && (b[0] == ',' || b[0] == '\n' || b[0] == '\r')
}

func (f *frameReader) number() (db.Value, error) {
	var tok []byte
	for !f.atDelimiter() {
		c, err := f.next()
		if err != nil {
			return db.Value{}, err
		}
		tok = append(tok, c)
		if len(tok) > 64 {
			return db.Value{}, errNotLiteral
		}
	}
	switch s := string(tok); {
	case s == "Inf": // not SQL: written so that it can go back to the server
		return db.Value{Text: realInf}, nil
	case s == "-Inf":
		return db.Value{Text: "-" + realInf}, nil
	case numberRe.MatchString(s):
		return db.Value{Text: s}, nil
	}
	return db.Value{}, errNotLiteral
}

func (f *frameReader) blob() (db.Value, error) {
	if !f.expect("x'") && !f.expect("X'") {
		return db.Value{}, errNotLiteral
	}
	var digits []byte
	for {
		c, err := f.next()
		if err != nil {
			return db.Value{}, err
		}
		if c == '\'' {
			break
		}
		if !isHex(c) {
			return db.Value{}, errNotLiteral
		}
		digits = append(digits, c)
	}
	if len(digits)%2 != 0 {
		return db.Value{}, errNotLiteral
	}
	return db.Value{Text: "x'" + strings.ToUpper(string(digits)) + "'", Blob: true}, nil
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// text reads a string in any of the forms sqlite3 versions write:
// 'a', unistr('a\u000a') and replace(replace('a\n','\n',char(10)),…).
func (f *frameReader) text() (string, error) {
	switch {
	case f.expect("unistr("):
		s, err := f.quoted()
		if err != nil {
			return "", err
		}
		if !f.expect(")") {
			return "", errNotLiteral
		}
		return unescapeUnistr(s), nil
	case f.expect("replace("):
		s, err := f.text()
		if err != nil {
			return "", err
		}
		if !f.expect(",") {
			return "", errNotLiteral
		}
		from, err := f.quoted()
		if err != nil {
			return "", err
		}
		if !f.expect(",char(") {
			return "", errNotLiteral
		}
		var digits []byte
		for {
			c, err := f.next()
			if err != nil {
				return "", err
			}
			if c == ')' {
				break
			}
			if c < '0' || c > '9' || len(digits) > 6 {
				return "", errNotLiteral
			}
			digits = append(digits, c)
		}
		code, err := strconv.Atoi(string(digits))
		if err != nil || !f.expect(")") {
			return "", errNotLiteral
		}
		return strings.ReplaceAll(s, from, string(rune(code))), nil
	}
	return f.quoted()
}

// quoted reads a '…' string, which may hold newlines and any bytes.
func (f *frameReader) quoted() (string, error) {
	c, err := f.next()
	if err != nil {
		return "", err
	}
	if c != '\'' {
		return "", errNotLiteral
	}
	var b bytes.Buffer
	for {
		chunk, err := f.r.ReadSlice('\'')
		f.raw = append(f.raw, chunk...)
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			b.Write(chunk)
			continue
		case err != nil:
			return "", err
		}
		b.Write(chunk[:len(chunk)-1])
		if !f.expect("'") { // '' is a quote; a lone one ends the string
			return b.String(), nil
		}
		b.WriteByte('\'')
	}
}

// unescapeUnistr decodes \\, \uXXXX and \UXXXXXXXX. Other bytes stay as they
// are, valid UTF-8 or not.
func unescapeUnistr(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		digits := 0
		switch s[i+1] {
		case '\\':
			b.WriteByte('\\')
			i++
			continue
		case 'u':
			digits = 4
		case 'U':
			digits = 8
		}
		if digits == 0 || i+2+digits > len(s) {
			b.WriteByte(s[i])
			continue
		}
		code, err := strconv.ParseUint(s[i+2:i+2+digits], 16, 32)
		if err != nil || code > utf8.MaxRune {
			b.WriteByte(s[i])
			continue
		}
		b.WriteRune(rune(code))
		i += 1 + digits
	}
	return b.String()
}
