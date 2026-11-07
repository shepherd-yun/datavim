package sqlite

import (
	"regexp"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// nearRe finds the token both engines name in a syntax error. SQLite writes
// it as is, so a " inside it is not doubled: the longest match to ": is taken.
var nearRe = regexp.MustCompile(`(?:near|unrecognized token:) "(.*?)"(?::|$|\s)`)

// locate adds the position of the token a syntax error names, when it is
// there. st is the statement that failed, inside the text the caller has.
func locate(err error, st sqlx.Statement) error {
	if err == nil {
		return nil
	}
	m := nearRe.FindStringSubmatch(err.Error())
	if m == nil {
		return err
	}
	off, ok := sqlx.FindToken(st.Text, sqlx.SQLite, m[1])
	if !ok {
		return err
	}
	return &db.PositionError{Err: err, Offset: st.Start + off}
}
