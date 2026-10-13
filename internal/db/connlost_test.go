package db

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
)

func TestIsConnLost(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{fmt.Errorf("query: %w", ErrConnLost), true},
		{driver.ErrBadConn, true},
		{fmt.Errorf("read: %w", io.EOF), true},
		{io.ErrUnexpectedEOF, true},
		{&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true},
		{fmt.Errorf("write: %w", syscall.EPIPE), true},
		{context.Canceled, false},
		{fmt.Errorf("x: %w", context.DeadlineExceeded), false},
		{errors.New("ERROR: syntax error at or near \"selec\""), false},
	}
	for _, c := range cases {
		if got := IsConnLost(c.err); got != c.want {
			t.Errorf("IsConnLost(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
