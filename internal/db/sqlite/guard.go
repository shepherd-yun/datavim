package sqlite

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// Functions refused on remote connections, before anything is sent.
var (
	// remoteBanned can run programs on the server (edit() really does).
	remoteBanned = []string{"edit", "load_extension"}
	// readOnlyBanned touch the server's files, which -readonly does not stop.
	readOnlyBanned = []string{"writefile", "readfile", "fsdir"}
)

// checkStmt refuses what must not reach a remote sqlite3 shell: its dot
// commands, which would run programs and read files; text that leaves the
// shell waiting for more input, which would swallow the marker and hang the
// connection; and functions that reach outside the database. stmt may hold
// several statements.
func checkStmt(stmt string, readOnly bool) error {
	const d = sqlx.SQLite
	if slices.Contains([]byte(stmt), 0) {
		return errors.New("NUL bytes are not supported in a statement")
	}
	stmts := sqlx.Split(stmt, d)
	for _, st := range stmts {
		switch sqlx.FirstWord(st.Text, d) {
		case ".":
			return errors.New("dot commands are not supported")
		case "#": // the shell skips such a line, and the next one starts afresh
			return errors.New("a statement cannot start with #")
		}
	}
	if !sqlx.IsComplete(stmt, d) {
		return errors.New("incomplete statement: a string, comment or trigger is not closed")
	}
	for _, st := range stmts {
		if sqlx.TerminatorLine(st.Text, d) {
			return errors.New(`a line holding only / or go is not supported`)
		}
		if name, ok := sqlx.CallsFunction(st.Text, d, remoteBanned...); ok {
			return fmt.Errorf("%s() is not allowed on a remote connection: it could run programs on the server", name)
		}
		if !readOnly {
			continue
		}
		if name, ok := sqlx.CallsFunction(st.Text, d, readOnlyBanned...); ok {
			return fmt.Errorf("%s() is not allowed on a read-only remote connection: it touches the server's files", name)
		}
		words := sqlx.LeadingWords(st.Text, d, 2)
		switch {
		case len(words) > 0 && words[0] == "ATTACH":
			return errors.New("ATTACH is not allowed on a read-only remote connection")
		case len(words) > 0 && words[0] == "VACUUM":
			return errors.New("VACUUM is not allowed on a read-only remote connection: VACUUM INTO writes a file on the server")
		case len(words) > 1 && words[0] == "CREATE" && words[1] == "VIRTUAL":
			return errors.New("CREATE virtual tables is not allowed on a read-only remote connection: a module such as zipfile writes files on the server")
		}
	}
	return nil
}

// protectCRLF keeps \r\n in string literals intact: the shell reads its input
// by lines and drops a \r before a line's end, which would silently change the
// text. Such a string is sent as an expression instead.
func protectCRLF(stmt string) string {
	if !strings.Contains(stmt, "\r\n") {
		return stmt
	}
	return sqlx.RewriteStrings(stmt, sqlx.SQLite, func(raw string) string {
		if !strings.Contains(raw, "\r\n") {
			return raw
		}
		return "(" + strings.ReplaceAll(raw, "\r\n", "'||char(13)||char(10)||'") + ")"
	})
}
