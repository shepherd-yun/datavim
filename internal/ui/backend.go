// Package ui is the Bubble Tea application.
package ui

import (
	"context"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
)

// Backend is what the UI needs from the session layer.
type Backend interface {
	Connections() []config.Connection
	Conn(ctx context.Context, name, database string) (db.Conn, error)
	SetPassword(name, password string, save bool) error
}
