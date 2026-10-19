package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"syscall"
)

// ErrConnLost marks errors caused by a connection that is no longer usable.
var ErrConnLost = errors.New("connection lost")

// IsConnLost reports whether err means the server connection is gone, so
// reconnecting may help. Cancellation and timeouts of the caller's own
// context are not connection losses.
func IsConnLost(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// Only errors from the network layer count: matching any net.Error would
	// also match syscall.Errno, i.e. every local file-system error.
	var oe *net.OpError
	return errors.Is(err, ErrConnLost) || errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) ||
		// database/sql's answer on a dedicated connection the driver
		// found dead, e.g. after the server killed the session
		errors.Is(err, sql.ErrConnDone) ||
		errors.Is(err, syscall.EPIPE) || errors.As(err, &oe)
}
