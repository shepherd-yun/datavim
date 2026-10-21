package oracle

import (
	"errors"

	"github.com/sijms/go-ora/v2/network"

	"github.com/shepherd-yun/datavim/internal/db"
)

// locate marks a server error that carries a position (a character offset
// into the sent text, which begins at start in query).
func locate(err error, query string, start int) error {
	var oe *network.OracleError
	if !errors.As(err, &oe) || oe.ErrPos() <= 0 {
		return err
	}
	return &db.PositionError{Err: err, Offset: start + db.RuneOffset(query[start:], oe.ErrPos())}
}
