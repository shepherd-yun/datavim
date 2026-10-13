package mysql

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// nearRE matches the tail of a MySQL syntax error message. The quoted text
// is the rest of the statement from the error on (possibly cut short).
var nearRE = regexp.MustCompile(`(?s)near '(.*)' at line (\d+)$`)

// locate marks syntax errors with where they occur in query.
func locate(err error, query string) error {
	var me *gomysql.MySQLError
	if !errors.As(err, &me) {
		return err
	}
	m := nearRE.FindStringSubmatch(me.Message)
	if m == nil {
		return err
	}
	near := m[1]
	if near == "" {
		return &db.PositionError{Err: err, Offset: len(query)}
	}
	line, _ := strconv.Atoi(m[2])
	start := 0
	for i := 1; i < line; i++ {
		nl := strings.IndexByte(query[start:], '\n')
		if nl < 0 {
			return err
		}
		start += nl + 1
	}
	if k := strings.Index(query[start:], near); k >= 0 {
		return &db.PositionError{Err: err, Offset: start + k}
	}
	return &db.PositionError{Err: err, Offset: start}
}
