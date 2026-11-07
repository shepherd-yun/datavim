package sqlite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

const (
	maxPooled   = 4
	idleTimeout = 60 * time.Second
	// minMinor is the oldest sqlite3 3.x accepted (RHEL 8); safeMinor is the
	// first with -safe.
	minMinor  = 26
	safeMinor = 37
)

// cliEngine drives sqlite3 processes next to the file, through db.ExecFunc.
// Up to maxPooled processes serve Conn's statements, started on demand and
// closed after idleTimeout; each session has one of its own.
type cliEngine struct {
	execFn   db.ExecFunc
	host     string
	path     string
	readOnly bool
	safe     bool
	onStart  func(*cliProc) // for tests

	slots chan struct{}
	mu    sync.Mutex
	idle  []idleProc
	timer *time.Timer
	done  bool
}

type idleProc struct {
	p     *cliProc
	since time.Time
}

func openCLI(ctx context.Context, cfg db.ConnConfig) (engine, error) {
	e := &cliEngine{
		execFn: cfg.Exec, host: cfg.ExecHost, path: cfg.Database, readOnly: cfg.ReadOnly,
		slots: make(chan struct{}, maxPooled),
	}
	if e.host == "" {
		e.host = "the server"
	}
	minor, err := e.version(ctx)
	if err != nil {
		return nil, err
	}
	e.safe = cfg.ReadOnly && minor >= safeMinor
	return e, nil
}

func (e *cliEngine) notFound() error {
	return fmt.Errorf("sqlite3 not found on %s: install it on the server", e.host)
}

// version checks the server's sqlite3 and returns its minor version.
func (e *cliEngine) version(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	p, err := e.execFn(ctx, versionCommand())
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = p.Stdin().Close()
		_ = p.Close()
		go func() { _ = p.Wait() }()
	}()
	type read struct {
		out []byte
		err error
	}
	got := make(chan read, 1)
	go func() {
		out, err := io.ReadAll(io.LimitReader(p.Stdout(), 64<<10))
		got <- read{out, err}
	}()
	var r read
	select {
	case r = <-got:
	case <-ctx.Done():
		return 0, fmt.Errorf("reading the sqlite3 version on %s: %w", e.host, ctx.Err())
	}
	out := strings.TrimSpace(string(r.out))
	if strings.Contains(out, noSQLite3Marker) {
		return 0, e.notFound()
	}
	major, minor, ok := parseVersion(out)
	if !ok {
		if r.err != nil && out == "" {
			return 0, r.err
		}
		return 0, fmt.Errorf("unexpected output from sqlite3 on %s: %s", e.host, firstLine(out))
	}
	if major != 3 || minor < minMinor {
		return 0, fmt.Errorf("sqlite3 %s on %s is too old: datavim needs 3.%d or newer", strings.Fields(out)[0], e.host, minMinor)
	}
	return minor, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func (e *cliEngine) start(ctx context.Context) (*cliProc, error) {
	p, err := startProc(ctx, e)
	if err != nil {
		return nil, err
	}
	if e.onStart != nil {
		e.onStart(p)
	}
	return p, nil
}

// acquire takes a free slot, then an idle process or a new one.
func (e *cliEngine) acquire(ctx context.Context) (*cliProc, error) {
	select {
	case e.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	e.mu.Lock()
	if n := len(e.idle); n > 0 {
		p := e.idle[n-1].p
		e.idle = e.idle[:n-1]
		e.mu.Unlock()
		return p, nil
	}
	e.mu.Unlock()
	p, err := e.start(ctx)
	if err != nil {
		<-e.slots
		return nil, err
	}
	return p, nil
}

// release gives a healthy process back.
func (e *cliEngine) release(p *cliProc) {
	e.mu.Lock()
	closed := e.done
	if !closed {
		e.idle = append(e.idle, idleProc{p: p, since: time.Now()})
		if e.timer == nil {
			e.timer = time.AfterFunc(idleTimeout, e.tick)
		}
	}
	e.mu.Unlock()
	if closed {
		p.close()
	}
	<-e.slots
}

// discardProc drops a process and frees its slot.
func (e *cliEngine) discardProc() { <-e.slots }

func (e *cliEngine) tick() {
	e.reap(time.Now())
	e.mu.Lock()
	defer e.mu.Unlock()
	e.timer = nil
	if len(e.idle) > 0 && !e.done {
		e.timer = time.AfterFunc(idleTimeout, e.tick)
	}
}

// reap closes the processes idle for idleTimeout at time now.
func (e *cliEngine) reap(now time.Time) {
	e.mu.Lock()
	var old []*cliProc
	kept := e.idle[:0]
	for _, ip := range e.idle {
		if now.Sub(ip.since) >= idleTimeout {
			old = append(old, ip.p)
		} else {
			kept = append(kept, ip)
		}
	}
	e.idle = kept
	e.mu.Unlock()
	for _, p := range old {
		p.close()
	}
}

// closeIdle closes every idle process: after a lost connection they are dead too.
func (e *cliEngine) closeIdle() {
	e.mu.Lock()
	idle := e.idle
	e.idle = nil
	e.mu.Unlock()
	for _, ip := range idle {
		ip.p.close()
	}
}

func (e *cliEngine) close() error {
	e.mu.Lock()
	e.done = true
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	e.mu.Unlock()
	e.closeIdle()
	return nil
}

// check returns the text to send for stmt, or why it must not be sent.
func (e *cliEngine) check(stmt string) (string, error) {
	if err := checkStmt(stmt, e.readOnly); err != nil {
		return "", err
	}
	return protectCRLF(stmt), nil
}

// finish decides what happens to a pooled process after a statement. A process
// that left a transaction open, or whose output nobody read to the end, is not
// reused.
func (e *cliEngine) finish(ctx context.Context, p *cliProc, stmt string, stopped bool, err error) error {
	switch {
	case ctx.Err() != nil:
		p.close()
		e.discardProc()
		return ctx.Err()
	case isStreamEnd(err):
		p.close()
		e.discardProc()
		e.closeIdle()
		return connLost(err, false)
	case stopped:
		p.terminate()
		e.discardProc()
		return err
	case err == nil && sqlx.TransactionEffect(stmt, sqlx.SQLite) == sqlx.TxBegin:
		p.close()
		e.discardProc()
		return nil
	}
	e.release(p)
	return err
}

func (e *cliEngine) exec(ctx context.Context, stmt string) (db.ExecResult, error) {
	stmt, err := e.check(stmt)
	if err != nil {
		return db.ExecResult{}, err
	}
	p, err := e.acquire(ctx)
	if err != nil {
		return db.ExecResult{}, err
	}
	res, err := p.runExec(ctx, stmt)
	if err = e.finish(ctx, p, stmt, false, err); err != nil {
		return db.ExecResult{}, err
	}
	return db.ExecResult{RowsAffected: res.rows}, nil
}

func (e *cliEngine) stream(ctx context.Context, stmt string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	stmt, err := e.check(stmt)
	if err != nil {
		return err
	}
	p, err := e.acquire(ctx)
	if err != nil {
		return err
	}
	res, err := p.runStream(ctx, stmt, false, onColumns, onRow)
	stopped := res.stopErr != nil
	if err == nil && stopped && !errors.Is(res.stopErr, db.ErrStop) {
		err = res.stopErr
	}
	return e.finish(ctx, p, stmt, stopped, err)
}

func (e *cliEngine) session(ctx context.Context) (engineSession, error) {
	p, err := e.start(ctx)
	if err != nil {
		return nil, err
	}
	return &cliSession{e: e, p: p}, nil
}

// cliSession is a process of its own. Cancelling costs the process: outside a
// transaction the next call starts another, losing temporary tables and
// PRAGMA settings; inside one the session stays lost, as a reconnect would
// silently drop the transaction.
type cliSession struct {
	e    *cliEngine
	mu   sync.Mutex
	p    *cliProc
	tx   sqlx.TxTracker
	inTx bool
	lost error
}

// proc is the session's process, started if the last one ended.
func (s *cliSession) proc(ctx context.Context) (*cliProc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lost != nil {
		return nil, s.lost
	}
	if s.p == nil {
		p, err := s.e.start(ctx)
		if err != nil {
			return nil, err
		}
		s.p = p
	}
	return s.p, nil
}

// txGone reports that the open transaction no longer exists, which BEGIN only
// succeeds on.
func txGone(ctx context.Context, p *cliProc) bool {
	if _, err := p.runExec(ctx, "BEGIN"); err != nil {
		return false
	}
	_, _ = p.runExec(ctx, "ROLLBACK")
	return true
}

// after deals with the outcome of a statement on p. A statement that failed
// may have taken the transaction along, as under OR ROLLBACK; the check runs
// unlocked, so that close can still reach a process that hangs.
func (s *cliSession) after(ctx context.Context, p *cliProc, stmt string, err error) error {
	rolledBack := err != nil && s.inTx && ctx.Err() == nil && !isStreamEnd(err) && txGone(ctx, p)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case rolledBack:
		s.inTx = false
		return fmt.Errorf("%w: %w", err, db.ErrTxRolledBack)
	case ctx.Err() != nil:
		p.close()
		s.p = nil
		if s.inTx {
			s.lost = fmt.Errorf("%w: %s", db.ErrConnLost, cancelledInTx)
			return s.lost
		}
		return ctx.Err()
	case isStreamEnd(err):
		p.close()
		s.p = nil
		lost := connLost(err, s.inTx)
		if s.inTx {
			s.lost = lost
		}
		return lost
	case err == nil:
		switch s.tx.Effect(stmt, sqlx.SQLite, s.inTx) {
		case sqlx.TxBegin:
			s.inTx = true
		case sqlx.TxEnd:
			s.inTx = false
		case sqlx.TxNone, sqlx.TxResume:
		}
	}
	return err
}

func (s *cliSession) exec(ctx context.Context, stmt string) (db.ExecResult, error) {
	stmt, err := s.e.check(stmt)
	if err != nil {
		return db.ExecResult{}, err
	}
	p, err := s.proc(ctx)
	if err != nil {
		return db.ExecResult{}, err
	}
	res, err := p.runExec(ctx, stmt)
	if err = s.after(ctx, p, stmt, err); err != nil {
		return db.ExecResult{}, err
	}
	return db.ExecResult{RowsAffected: res.rows}, nil
}

func (s *cliSession) stream(ctx context.Context, stmt string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	stmt, err := s.e.check(stmt)
	if err != nil {
		return err
	}
	p, err := s.proc(ctx)
	if err != nil {
		return err
	}
	res, err := p.runStream(ctx, stmt, true, onColumns, onRow)
	if err == nil && res.stopErr != nil && !errors.Is(res.stopErr, db.ErrStop) {
		err = res.stopErr
	}
	return s.after(ctx, p, stmt, err)
}

func (s *cliSession) ping(ctx context.Context) error {
	_, err := s.exec(ctx, "SELECT 1")
	return err
}

// close ends the process; with stdin closed an open transaction rolls back.
// A statement still running would go on and keep its locks, so it gets a signal.
func (s *cliSession) close() error {
	s.mu.Lock()
	p := s.p
	s.p = nil
	if s.lost == nil {
		s.lost = fmt.Errorf("%w: the session is closed", db.ErrConnLost)
	}
	s.mu.Unlock()
	if p != nil {
		if p.busy.Load() {
			p.signal("INT")
		}
		p.close()
	}
	return nil
}
