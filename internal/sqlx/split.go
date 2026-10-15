package sqlx

import (
	"strings"
	"unicode"
)

// Statement is one SQL statement inside a larger text.
type Statement struct {
	Text       string // source text without the trailing delimiter
	Start, End int    // byte offsets of Text in the source; End is exclusive
}

// Split cuts src into statements. Delimiters inside strings, quoted
// identifiers and comments are ignored; MySQL DELIMITER lines change the
// delimiter. Statements that contain only comments are dropped.
func Split(src string, d Dialect) []Statement {
	var out []Statement
	delim := ";"
	start := -1      // start of the current statement; -1 between statements
	hasCode := false // the current statement has a non-comment token
	flush := func(end int) {
		if start >= 0 && hasCode {
			text := strings.TrimRightFunc(src[start:end], unicode.IsSpace)
			out = append(out, Statement{Text: text, Start: start, End: start + len(text)})
		}
		start, hasCode = -1, false
	}
	for i := 0; i < len(src); {
		if isSpace(src[i]) {
			i++
			continue
		}
		if d == MySQL && start < 0 {
			if nd, next, ok := delimiterDirective(src, i); ok {
				delim, i = nd, next
				continue
			}
		}
		if strings.HasPrefix(src[i:], delim) {
			flush(i)
			i += len(delim)
			continue
		}
		t := lex(src, i, d)
		if start < 0 {
			start = i
		}
		if t.kind != tokComment {
			hasCode = true
		}
		i = t.end
	}
	flush(len(src))
	return out
}

// delimiterDirective parses a "DELIMITER x" line starting at i.
func delimiterDirective(src string, i int) (delim string, next int, ok bool) {
	end := strings.IndexByte(src[i:], '\n')
	if end < 0 {
		end = len(src)
	} else {
		end += i
	}
	f := strings.Fields(src[i:end])
	if len(f) != 2 || !strings.EqualFold(f[0], "DELIMITER") {
		return "", 0, false
	}
	return f[1], end, true
}

// At returns the statement under offset: the last one starting at or before
// it, or the first one when offset precedes them all.
func At(stmts []Statement, offset int) (Statement, bool) {
	if len(stmts) == 0 {
		return Statement{}, false
	}
	best := stmts[0]
	for _, s := range stmts {
		if s.Start <= offset {
			best = s
		}
	}
	return best, true
}

// From returns the statements from the one under offset to the end; an
// offset between two statements starts at the next one.
func From(stmts []Statement, offset int) []Statement {
	for i, s := range stmts {
		if offset <= s.End {
			return stmts[i:]
		}
	}
	return nil
}
