package sqlx

import "strings"

// RedactSQL hides passwords in SQL shown to the user, in any statement of
// the text (a batch, a DO block's body): the secret after IDENTIFIED
// [WITH plugin] BY, PASSWORD [=], OLD_PASSWORD [=], SET PASSWORD … =, and
// after a REPLACE that follows one of those, and every literal passed to
// sp_addlogin or sp_password, becomes '***'. It errs on the side of
// hiding: an unterminated literal is hidden too, and so is the value in
// password = '…' anywhere.
func RedactSQL(sql string, d Dialect) string {
	toks := codeTokens(sql, d)
	isWord := func(i int, w string) bool {
		return i < len(toks) && toks[i].kind == tokWord && strings.EqualFold(toks[i].text, w)
	}
	isPunct := func(i int, p string) bool {
		return i < len(toks) && toks[i].kind == tokPunct && toks[i].text == p
	}
	type span struct {
		start, end int
		with       string
	}
	var spans []span
	hidden := false // a secret was hidden in this statement
	// hide hides the secret at token i: a string (with SQL Server's N
	// prefix), a quoted identifier, or a bare word when bare is set
	// (Oracle's IDENTIFIED BY tiger).
	hide := func(i int, bare bool) {
		if i >= len(toks) {
			return
		}
		t := toks[i]
		switch {
		case t.kind == tokWord && strings.EqualFold(t.text, "N") && i+1 < len(toks) &&
			toks[i+1].kind == tokString && toks[i+1].pos == t.end:
			spans = append(spans, span{t.pos, toks[i+1].end, "'***'"})
		case t.kind == tokString, t.kind == tokIdent,
			bare && t.kind == tokWord && !strings.EqualFold(t.text, "PASSWORD"):
			spans = append(spans, span{t.pos, t.end, "'***'"})
		default:
			return
		}
		hidden = true
	}
	setPassword, procArgs := false, false
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch {
		case isPunct(i, ";"):
			hidden, setPassword, procArgs = false, false, false
		case procArgs:
			hide(i, false)
		case isWord(i, "SET") && isWord(i+1, "PASSWORD"):
			setPassword = true
			i++
		case setPassword && isPunct(i, "="):
			setPassword = false
			hide(i+1, false)
		case isWord(i, "IDENTIFIED"):
			j := i + 1
			if isWord(j, "WITH") {
				j += 2
			}
			if isWord(j, "BY") {
				hide(j+1, true)
			}
		case isWord(i, "PASSWORD"), isWord(i, "OLD_PASSWORD"):
			j := i + 1
			if isPunct(j, "=") {
				j++
			}
			hide(j, false)
		case hidden && isWord(i, "REPLACE") && !isPunct(i+1, "("):
			hide(i+1, true)
		case isWord(i, "sp_addlogin"), isWord(i, "sp_password"):
			procArgs = true
		case t.kind == tokString && strings.HasPrefix(t.text, "$"):
			// a dollar-quoted body, as in DO $$ … $$: redact inside it
			if tag := t.text[:strings.Index(t.text[1:], "$")+2]; len(t.text) >= 2*len(tag) && strings.HasSuffix(t.text, tag) {
				body := t.text[len(tag) : len(t.text)-len(tag)]
				if r := RedactSQL(body, d); r != body {
					spans = append(spans, span{t.pos, t.end, tag + r + tag})
				}
			}
		}
	}
	if len(spans) == 0 {
		return sql
	}
	var b strings.Builder
	last := 0
	for _, s := range spans {
		if s.start < last {
			continue
		}
		b.WriteString(sql[last:s.start])
		b.WriteString(s.with)
		last = s.end
	}
	b.WriteString(sql[last:])
	return b.String()
}
