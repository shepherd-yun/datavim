package sqlserver

import (
	"context"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// server is the conn's db.ServerReader.
type server struct{ c *conn }

func (c *conn) Server() db.ServerReader { return server{c: c} }

var errNotImplemented = db.Unavailable("not implemented")

func (server) Info(context.Context) (db.ServerInfo, error) { return db.ServerInfo{}, errNotImplemented }

func (server) Sample(context.Context) (db.ServerSample, error) {
	return db.ServerSample{}, errNotImplemented
}

func (server) Sessions(context.Context, bool) (db.SessionList, error) {
	return db.SessionList{}, errNotImplemented
}

func (server) Locks(context.Context) (*db.ResultSet, error) { return nil, errNotImplemented }

func (server) TopStatements(context.Context, int) (*db.ResultSet, error) {
	return nil, errNotImplemented
}

func (server) Settings(context.Context) (*db.ResultSet, error) { return nil, errNotImplemented }

func (server) Replication(context.Context) (*db.ResultSet, error) { return nil, errNotImplemented }

func (server) CancelQuery(context.Context, db.SessionID) (bool, error) {
	return false, errNotImplemented
}

func (server) KillSession(context.Context, db.SessionID) (bool, error) {
	return false, errNotImplemented
}
