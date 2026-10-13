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
	Connection(name string) (config.Connection, bool)
	Conn(ctx context.Context, name, database string) (db.Conn, error)
	SetPassword(name, password string, save bool) error
	// Reset drops the open connections of name so the next Conn reconnects.
	Reset(name string)
	// AddConnection saves a new connection to connections.toml (and its
	// password to the keychain when save is set).
	AddConnection(c config.Connection, password string, save bool) error
	// SetReadOnly switches a connection between read-only and read-write
	// for this run only.
	SetReadOnly(name string, ro bool) error
}
