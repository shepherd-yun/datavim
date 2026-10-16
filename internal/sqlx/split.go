package sqlx

import (
	"strconv"
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
// delimiter, and a T-SQL GO line ends a statement too. Statements that contain only comments are dropped.
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
		if d == SQLServer && lineStart(src, i) {
			if next, ok := goLine(src, i); ok {
				flush(i)
				i = next
				continue
			}
		}
		if d == Oracle && start < 0 {
			if end, next, ok := plsqlBlock(src, i); ok {
				start, hasCode = i, true
				flush(end)
				i = next
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

// plsqlBlock reads a PL/SQL block starting at i (BEGIN, DECLARE, CREATE
// PROCEDURE …), which holds semicolons: like SQL*Plus it runs to a line
// holding only "/", or to the end. end is where the block's text ends
// (after its END;), next where scanning goes on.
func plsqlBlock(src string, i int) (end, next int, ok bool) {
	var words []string
	for j := i; j < len(src) && len(words) < 6; {
		if isSpace(src[j]) {
			j++
			continue
		}
		t := lex(src, j, Oracle)
		if t.kind == tokComment {
			j = t.end
			continue
		}
		if t.kind != tokWord {
			break
		}
		words = append(words, strings.ToUpper(t.text))
		j = t.end
	}
	if !isBlockStart(words) {
		return 0, 0, false
	}
	for j := i; j < len(src); {
		if isSpace(src[j]) {
			if src[j] == '\n' {
				if k, ok := slashLine(src, j+1); ok {
					return j, k, true
				}
			}
			j++
			continue
		}
		j = lex(src, j, Oracle).end
	}
	return len(src), len(src), true
}

// isBlockStart reports the first words of a PL/SQL block.
func isBlockStart(w []string) bool {
	if len(w) == 0 {
		return false
	}
	switch w[0] {
	case "BEGIN", "DECLARE":
		return true
	case "CREATE":
	default:
		return false
	}
	w = w[1:]
	if len(w) >= 2 && w[0] == "OR" && w[1] == "REPLACE" {
		w = w[2:]
	}
	if len(w) > 0 && (w[0] == "EDITIONABLE" || w[0] == "NONEDITIONABLE") {
		w = w[1:]
	}
	return len(w) > 0 && (w[0] == "PROCEDURE" || w[0] == "FUNCTION" || w[0] == "PACKAGE" || w[0] == "TRIGGER" || w[0] == "TYPE")
}

// slashLine reports a line from i holding only "/" and returns where the
// next line starts.
func slashLine(src string, i int) (int, bool) {
	end := strings.IndexByte(src[i:], '\n')
	if end < 0 {
		end = len(src)
	} else {
		end += i
	}
	if strings.TrimSpace(src[i:end]) != "/" {
		return 0, false
	}
	return min(end+1, len(src)), true
}

// lineStart reports that only blanks precede i on its line.
func lineStart(src string, i int) bool {
	for j := i - 1; j >= 0 && src[j] != '\n'; j-- {
		if !isSpace(src[j]) {
			return false
		}
	}
	return true
}

// goLine parses a T-SQL batch separator line starting at i: GO, optionally
// followed by a repeat count, which is ignored.
func goLine(src string, i int) (next int, ok bool) {
	end := strings.IndexByte(src[i:], '\n')
	if end < 0 {
		end = len(src)
	} else {
		end += i
	}
	f := strings.Fields(src[i:end])
	if len(f) == 0 || len(f) > 2 || !strings.EqualFold(f[0], "GO") {
		return 0, false
	}
	if len(f) == 2 {
		if _, err := strconv.Atoi(f[1]); err != nil {
			return 0, false
		}
	}
	return end, true
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
