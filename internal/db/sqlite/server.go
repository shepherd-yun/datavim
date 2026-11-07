package sqlite

import (
	"context"

	"github.com/shepherd-yun/datavim/internal/db"
)

// server answers the server tab's questions: SQLite has no server. The UI
// hides the tab (sqlx.Features.ServerStatus).
type server struct{}

var errNoServer = db.Unavailable("SQLite has no server status")

func (server) Info(context.Context) (db.ServerInfo, error) { return db.ServerInfo{}, errNoServer }
func (server) Sample(context.Context) (db.ServerSample, error) {
	return db.ServerSample{}, errNoServer
}

func (server) Sessions(context.Context, bool) (db.SessionList, error) {
	return db.SessionList{}, errNoServer
}
func (server) Locks(context.Context) (*db.ResultSet, error)              { return nil, errNoServer }
func (server) TopStatements(context.Context, int) (*db.ResultSet, error) { return nil, errNoServer }
func (server) Settings(context.Context) (*db.ResultSet, error)           { return nil, errNoServer }
func (server) Replication(context.Context) (*db.ResultSet, error)        { return nil, errNoServer }
func (server) CancelQuery(context.Context, db.SessionID) (bool, error)   { return false, errNoServer }

func (server) KillSession(context.Context, db.SessionID) (bool, error) { return false, errNoServer }
