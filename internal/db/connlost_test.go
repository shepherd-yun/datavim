package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
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
		{fmt.Errorf("ping: %w", sql.ErrConnDone), true}, // the server ended the session (a kill)
		{&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true},
		{fmt.Errorf("write: %w", syscall.EPIPE), true},
		{context.Canceled, false},
		{fmt.Errorf("x: %w", context.DeadlineExceeded), false},
		{errors.New("ERROR: syntax error at or near \"selec\""), false},
		// Local file-system errors are not connection losses even though
		// syscall.Errno satisfies net.Error.
		{&fs.PathError{Op: "open", Path: "/tmp/users.xlsx", Err: syscall.EEXIST}, false},
		{&fs.PathError{Op: "open", Path: "/nope/users.csv", Err: syscall.ENOENT}, false},
		{&net.OpError{Op: "read", Net: "tcp", Err: &os.SyscallError{Syscall: "read", Err: syscall.ETIMEDOUT}}, true},
	}
	for _, c := range cases {
		if got := IsConnLost(c.err); got != c.want {
			t.Errorf("IsConnLost(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
