package ui

import (
	"context"
	"log/slog"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// withConn runs a read-only fn on the named connection. If the connection
// turns out to be dead, the pool is reset and fn is retried once.
func withConn(ctx context.Context, b Backend, conn, database string, fn func(db.Conn) error) error {
	c, err := b.Conn(ctx, conn, database)
	if err == nil {
		err = fn(c)
	}
	if !db.IsConnLost(err) {
		return err
	}
	slog.Info("connection lost, reconnecting", "conn", conn, "err", err)
	b.Reset(conn)
	if c, err = b.Conn(ctx, conn, database); err != nil {
		return err
	}
	return fn(c)
}

// connFailure is implemented by results of work on a connection; the root
// model uses it to mark connections as disconnected (or healthy again).
type connFailure interface {
	failure() (conn string, err error)
}

func (m childrenLoadedMsg) failure() (string, error)  { return m.node.Conn, m.err }
func (m pageLoadedMsg) failure() (string, error)      { return m.conn, m.err }
func (m structureLoadedMsg) failure() (string, error) { return m.conn, m.err }
func (m exportDoneMsg) failure() (string, error)      { return m.conn, m.err }
func (m editsAppliedMsg) failure() (string, error)    { return m.conn, m.err }

func (m runDoneMsg) failure() (string, error) {
	if m.err != nil || len(m.results) == 0 {
		return m.conn, m.err
	}
	return m.conn, m.results[len(m.results)-1].Err
}
