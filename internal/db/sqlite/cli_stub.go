package sqlite

import (
	"context"
	"errors"

	"github.com/shepherd-yun/datavim/internal/db"
)

func openCLI(context.Context, db.ConnConfig) (engine, error) {
	return nil, errors.New("sqlite: remote files are not supported yet")
}
