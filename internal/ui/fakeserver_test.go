package ui

import (
	"context"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// fakeServer is a db.ServerReader for the server tab tests.
type fakeServer struct{}

var errFakeServer = db.Unavailable("fake")

func (*fakeServer) Info(context.Context) (db.ServerInfo, error) {
	return db.ServerInfo{}, errFakeServer
}

func (*fakeServer) Sample(context.Context) (db.ServerSample, error) {
	return db.ServerSample{}, errFakeServer
}

func (*fakeServer) Sessions(context.Context, bool) (db.SessionList, error) {
	return db.SessionList{}, errFakeServer
}

func (*fakeServer) Locks(context.Context) (*db.ResultSet, error) { return nil, errFakeServer }

func (*fakeServer) TopStatements(context.Context, int) (*db.ResultSet, error) {
	return nil, errFakeServer
}

func (*fakeServer) Settings(context.Context) (*db.ResultSet, error) { return nil, errFakeServer }

func (*fakeServer) Replication(context.Context) (*db.ResultSet, error) { return nil, errFakeServer }

func (*fakeServer) CancelQuery(context.Context, db.SessionID) (bool, error) {
	return false, errFakeServer
}

func (*fakeServer) KillSession(context.Context, db.SessionID) (bool, error) {
	return false, errFakeServer
}
