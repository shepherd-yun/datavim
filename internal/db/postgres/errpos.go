package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/shepherd-yun/datavim/internal/db"
)

// locate marks server errors that carry a position (1-based, in characters).
func locate(err error, sql string) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Position > 0 {
		return &db.PositionError{Err: err, Offset: db.RuneOffset(sql, int(pe.Position)-1)}
	}
	return err
}
