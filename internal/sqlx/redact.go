package sqlx

import "strings"

// RedactSQL hides the password in a statement that sets one (CREATE or
// ALTER USER, ROLE or LOGIN, and SET PASSWORD): the secret after
// IDENTIFIED [WITH plugin] BY, PASSWORD [=] or SET PASSWORD … = becomes
// '***'. Other statements come back unchanged. It errs on the side of
// hiding: an unterminated literal is hidden too.
func RedactSQL(sql string) string {
	toks := codeTokens(sql, MySQL)
	if len(toks) < 2 {
		return sql
	}
	head, verb := strings.ToUpper(toks[0].text), strings.ToUpper(toks[1].text)
	setPassword := head == "SET" && verb == "PASSWORD"
	if !setPassword && (head != "CREATE" && head != "ALTER" || verb != "USER" && verb != "ROLE" && verb != "LOGIN") {
		return sql
	}
	isWord := func(i int, w string) bool {
		return i < len(toks) && toks[i].kind == tokWord && strings.EqualFold(toks[i].text, w)
	}
	isPunct := func(i int, p string) bool {
		return i < len(toks) && toks[i].kind == tokPunct && toks[i].text == p
	}
	// secret reports the span of the secret at token i: a string (with
	// SQL Server's N prefix), a quoted identifier, or a bare word when
	// bare is set (Oracle's IDENTIFIED BY tiger).
	secret := func(i int, bare bool) (start, end int, ok bool) {
		if i >= len(toks) {
			return 0, 0, false
		}
		t := toks[i]
		switch {
		case t.kind == tokWord && strings.EqualFold(t.text, "N") && i+1 < len(toks) &&
			toks[i+1].kind == tokString && toks[i+1].pos == t.end:
			return t.pos, toks[i+1].end, true
		case t.kind == tokString, t.kind == tokIdent:
			return t.pos, t.end, true
		case bare && t.kind == tokWord && !strings.EqualFold(t.text, "PASSWORD"):
			return t.pos, t.end, true
		}
		return 0, 0, false
	}
	type span struct{ start, end int }
	var hide []span
	add := func(start, end int, ok bool) {
		if ok {
			hide = append(hide, span{start, end})
		}
	}
	for i := 1; i < len(toks); i++ {
		switch {
		case isWord(i, "IDENTIFIED"):
			j := i + 1
			if isWord(j, "WITH") {
				j += 2
			}
			if isWord(j, "BY") {
				add(secret(j+1, true))
			}
		case isWord(i, "PASSWORD") && !(setPassword && i == 1):
			j := i + 1
			if isPunct(j, "=") {
				j++
			}
			add(secret(j, false))
		case setPassword && isPunct(i, "="):
			add(secret(i+1, false))
			i = len(toks)
		}
	}
	if len(hide) == 0 {
		return sql
	}
	var b strings.Builder
	last := 0
	for _, h := range hide {
		if h.start < last {
			continue
		}
		b.WriteString(sql[last:h.start])
		b.WriteString("'***'")
		last = h.end
	}
	b.WriteString(sql[last:])
	return b.String()
}
