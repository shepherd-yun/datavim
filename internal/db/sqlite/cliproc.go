package sqlite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

const (
	startTimeout = 30 * time.Second
	killTimeout  = 5 * time.Second
	// lostMessage cannot say more: without a PTY the server's process gets no
	// SIGHUP, and kill cannot reach it over a dead connection.
	lostMessage   = "connection lost; the running statement may still finish on the server"
	lostInTx      = "; uncommitted changes will not be kept"
	cancelledInTx = "the statement was cancelled by ending its process: the transaction was rolled back"
)

// cliProc is one sqlite3 process next to the file. One statement runs at a time.
type cliProc struct {
	exec    db.ExecFunc
	p       db.Process
	in      io.WriteCloser
	out     *frameReader
	pid     string
	marker  string
	seq     int
	writing sync.WaitGroup
	busy    atomic.Bool
	once    sync.Once
}

func randomHex() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// startProc starts sqlite3 and prepares it for the protocol. An error that
// comes from the process (a missing file, say) is its own message.
func startProc(ctx context.Context, e *cliEngine) (*cliProc, error) {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	id, err := randomHex()
	if err != nil {
		return nil, err
	}
	p, err := e.execFn(ctx, startCommand(e.path, e.readOnly, e.safe))
	if err != nil {
		return nil, err
	}
	c := &cliProc{exec: e.execFn, p: p, in: p.Stdin(), out: newFrameReader(p.Stdout()), marker: "DV_" + id}
	done := make(chan error, 1)
	go func() { done <- c.handshake(e) }()
	select {
	case err = <-done:
	case <-ctx.Done():
		c.close()
		<-done
		c.kill()
		return nil, fmt.Errorf("starting sqlite3: %w", ctx.Err())
	}
	if err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

// handshake reads the PID line and sets the output mode. With a missing file
// sqlite3 prints its message and exits.
func (c *cliProc) handshake(e *cliEngine) error {
	line, err := c.out.r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == noSQLite3Marker {
		return e.notFound()
	}
	pid, ok := strings.CutPrefix(line, "PID=")
	if _, aerr := strconv.Atoi(pid); !ok || aerr != nil {
		if err != nil && line == "" {
			return fmt.Errorf("sqlite3 did not start: %w", err)
		}
		return fmt.Errorf("unexpected output from sqlite3: %s", line)
	}
	c.pid = pid
	c.send(".mode quote\n.headers on\n.timeout " + strconv.Itoa(busyTimeoutMillis) + "\nPRAGMA foreign_keys=ON;\n.print " + c.marker + "\n")
	errText, err := c.out.read(c.marker, nil, func([]db.Value) error { return nil })
	switch {
	case errText != "":
		return errors.New(cleanError(errText))
	case err != nil:
		return fmt.Errorf("sqlite3 exited: %w", err)
	}
	return nil
}

// send writes to the process's stdin without blocking the reader: a large
// statement and a large result would otherwise wait for each other.
func (c *cliProc) send(batch string) {
	c.writing.Wait()
	c.writing.Add(1)
	go func() {
		defer c.writing.Done()
		_, _ = io.WriteString(c.in, batch)
	}()
}

func (c *cliProc) nextMarker() string {
	c.seq++
	return c.marker + "_" + strconv.Itoa(c.seq)
}

// result is what one batch read back.
type result struct {
	rows    int64 // changes()
	stopErr error // what onRow returned, the rest of the result discarded
}

// readError is a read of the process's output that failed: the process is
// gone, unlike an error of its statement or of a row callback.
type readError struct{ error }

func (e readError) Unwrap() error { return e.error }

func isStreamEnd(err error) bool {
	var re readError
	return errors.As(err, &re)
}

// frame reads up to marker. A callback's error comes back as it is, the rest of
// the frame unread; res records it.
func (c *cliProc) frame(marker string, onHeader func([]string), onRow func([]db.Value) error) (string, error) {
	var stop error
	errText, err := c.out.read(marker, onHeader, func(r []db.Value) error {
		stop = onRow(r)
		return stop
	})
	switch {
	case err == nil:
		return errText, nil
	case stop != nil:
		return "", stop
	}
	if errText != "" { // what it said before it went away
		return errText, readError{fmt.Errorf("%s: %w", cleanError(errText), err)}
	}
	return "", readError{err}
}

func discard([]db.Value) error { return nil }

// runStream runs stmt and reads its rows. keep says what to do after a row
// callback's error: read and drop the rest of the output (a session must not
// lose its process), or return at once, leaving the process to the caller.
func (c *cliProc) runStream(ctx context.Context, stmt string, keep bool, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) (result, error) {
	typed := isSelect(stmt)
	var b strings.Builder
	var created, columns, dropped string
	if typed {
		view := "_dv_" + strconv.Itoa(c.seq)
		created, columns, dropped = c.nextMarker(), c.nextMarker(), c.nextMarker()
		b.WriteString("CREATE TEMP VIEW " + view + " AS " + stmt + "\n;\n.print " + created + "\n")
		b.WriteString("SELECT name, type FROM pragma_table_info('" + view + "');\n.print " + columns + "\n")
		b.WriteString("DROP VIEW IF EXISTS " + view + ";\n.print " + dropped + "\n")
	}
	done := c.nextMarker()
	b.WriteString(stmt + "\n;\n.print " + done + "\n")

	var res result
	err := c.run(ctx, b.String(), func() error {
		var viewCols []db.ColumnInfo
		if typed {
			if _, err := c.frame(created, func([]string) {}, discard); isStreamEnd(err) {
				return err
			}
			_, err := c.frame(columns, func([]string) {}, func(r []db.Value) error {
				if len(r) == 2 {
					viewCols = append(viewCols, db.ColumnInfo{Name: r[0].Text, DatabaseType: declType(r[1].Text)})
				}
				return nil
			})
			if err != nil {
				return err
			}
			if _, err := c.frame(dropped, func([]string) {}, discard); err != nil {
				return err
			}
		}
		sent := false
		var stop error
		errText, err := c.frame(done, func(names []string) {
			onColumns(mergeTypes(names, viewCols))
			sent = true
		}, func(r []db.Value) error {
			if ctx.Err() != nil {
				return nil
			}
			stop = onRow(r)
			return stop
		})
		if stop != nil {
			res.stopErr = stop
			if !keep {
				return nil
			}
			_, err := c.frame(done, nil, discard)
			if isStreamEnd(err) {
				return err
			}
			return nil
		}
		if err != nil {
			return err
		}
		if errText != "" {
			return errors.New(cleanError(errText))
		}
		if !sent {
			onColumns(viewCols)
		}
		return nil
	})
	return res, err
}

// mergeTypes names the result's columns; the types come from the view, which
// spells repeated names differently.
func mergeTypes(names []string, viewCols []db.ColumnInfo) []db.ColumnInfo {
	cols := make([]db.ColumnInfo, len(names))
	for i, n := range names {
		cols[i] = db.ColumnInfo{Name: n, DatabaseType: anyType}
		if len(viewCols) == len(names) {
			cols[i].DatabaseType = viewCols[i].DatabaseType
		}
	}
	return cols
}

// runExec runs stmt and reads changes().
func (c *cliProc) runExec(ctx context.Context, stmt string) (result, error) {
	done, count := c.nextMarker(), c.nextMarker()
	batch := stmt + "\n;\n.print " + done + "\nSELECT changes();\n.print " + count + "\n"
	var res result
	err := c.run(ctx, batch, func() error {
		errText, err := c.frame(done, func([]string) {}, discard)
		if err != nil {
			return err
		}
		_, err = c.frame(count, func([]string) {}, func(r []db.Value) error {
			if len(r) == 1 {
				res.rows, _ = strconv.ParseInt(r[0].Text, 10, 64)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if errText != "" {
			return errors.New(cleanError(errText))
		}
		return nil
	})
	return res, err
}

// isSelect reports the statements whose column types can be read through a
// temporary view. The view of WITH … INSERT fails, which is no error.
func isSelect(stmt string) bool {
	switch strings.ToUpper(sqlx.FirstWord(stmt, sqlx.SQLite)) {
	case "SELECT", "WITH", "VALUES":
		return true
	}
	return false
}

// run sends batch and reads it back with read. Cancelling ends the process:
// that is the only thing that stops a running statement without relying on
// sshd's signal support, and closing the channel would leave it running with
// its locks. The caller then treats the process as gone.
func (c *cliProc) run(ctx context.Context, batch string, read func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.busy.Store(true)
	defer c.busy.Store(false)
	c.send(batch)
	done := make(chan error, 1)
	go func() { done <- read() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		c.interrupt(done)
		return ctx.Err()
	}
}

// interrupt ends the process and waits for read to see the end of its output.
func (c *cliProc) interrupt(done <-chan error) {
	c.signal("INT")
	select {
	case <-done:
		return
	case <-time.After(killTimeout):
	}
	c.kill()
	select {
	case <-done:
		return
	case <-time.After(killTimeout):
	}
	c.close() // nothing more can be done from here; this ends the read
	<-done
}

// signal sends a signal through a channel of its own.
func (c *cliProc) signal(name string) {
	if c.pid == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), killTimeout)
	defer cancel()
	p, err := c.exec(ctx, "kill -"+name+" "+c.pid)
	if err != nil {
		return
	}
	waited := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, p.Stdout())
		_ = p.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-ctx.Done():
	}
	_ = p.Close()
}

func (c *cliProc) kill() { c.signal("KILL") }

// terminate ends a process nobody reads any more: its statement may be blocked
// writing output, so what is left is drained until it exits.
func (c *cliProc) terminate() {
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, c.out.r)
		done <- err
	}()
	c.interrupt(done)
	c.close()
}

// close ends the channel. With stdin closed sqlite3 exits as soon as it is
// idle, rolling back an open transaction.
func (c *cliProc) close() {
	c.once.Do(func() {
		_ = c.in.Close()
		_ = c.p.Close()
		go func() { _ = c.p.Wait() }()
	})
}

// errorPrefixRe is how the shell introduces a message.
var errorPrefixRe = regexp.MustCompile(`^(?:Parse error|Runtime error|Error:?)\s*(?:near line \d+:\s*)?`)

// cleanError reduces the shell's message to the server's: the line number
// counts the protocol's lines and the rest of the text repeats the statement.
func cleanError(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(errorPrefixRe.ReplaceAllString(line, ""))
}

// connLost is the error of a process gone without being asked to.
func connLost(err error, inTx bool) error {
	msg := lostMessage
	if inTx {
		msg += lostInTx
	}
	return fmt.Errorf("%w: %s (%w)", db.ErrConnLost, msg, err)
}
