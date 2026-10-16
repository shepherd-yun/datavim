package sqlserver

import (
	"errors"
	"regexp"
	"strings"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// nearRE finds the token a syntax error names.
var nearRE = regexp.MustCompile(`near '(.+?)'\.?$`)

// locate marks a server error with the line it reports, at the token it
// is near when the message names one on that line.
func locate(err error, query string) error {
	var me mssql.Error
	if !errors.As(err, &me) || me.LineNo <= 0 {
		return err
	}
	start := 0
	for i := int32(1); i < me.LineNo; i++ {
		nl := strings.IndexByte(query[start:], '\n')
		if nl < 0 {
			return err
		}
		start += nl + 1
	}
	line := query[start:]
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}
	if m := nearRE.FindStringSubmatch(me.Message); m != nil {
		if k := strings.Index(line, m[1]); k >= 0 {
			return &db.PositionError{Err: err, Offset: start + k}
		}
	}
	return &db.PositionError{Err: err, Offset: start + len(line) - len(strings.TrimLeft(line, " \t"))}
}
