package db

import (
	"context"
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
	var ne net.Error
	return errors.Is(err, ErrConnLost) || errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) || errors.As(err, &ne)
}
