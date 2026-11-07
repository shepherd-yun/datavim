package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"

	"github.com/shepherd-yun/datavim/internal/db"
)

// Exec makes sure the tunnel for alias is up and returns a function that runs
// commands on its last hop, without a PTY. Like Connect's dial function it
// reconnects the tunnel if it has died in the meantime.
func (p *Pool) Exec(ctx context.Context, alias string) (db.ExecFunc, error) {
	if _, err := p.live(ctx, alias); err != nil {
		return nil, err
	}
	return func(ctx context.Context, command string) (db.Process, error) {
		ch, err := p.live(ctx, alias)
		if err != nil {
			return nil, err
		}
		proc, err := startSession(ctx, ch.last(), command)
		if err == nil {
			return proc, nil
		}
		// A refused channel (sshd's MaxSessions) or command (a server that only
		// forwards ports) says nothing about the tunnel, which others may be
		// using: only drop it if it no longer answers.
		var refused *ssh.OpenChannelError
		if !errors.As(err, &refused) && ctx.Err() == nil && ping(ch.last(), p.opts.DialTimeout) != nil {
			p.drop(alias, ch)
		}
		return nil, fmt.Errorf("ssh %s: %w", alias, err)
	}, nil
}

// startSession opens a session and starts command; neither call takes a
// context, so they run aside and are abandoned when ctx ends.
func startSession(ctx context.Context, c *ssh.Client, command string) (db.Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type started struct {
		p   *sshProcess
		err error
	}
	done := make(chan started, 1)
	go func() {
		p, err := start(c, command)
		done <- started{p, err}
	}()
	select {
	case r := <-done:
		return r.p, r.err
	case <-ctx.Done():
		go func() {
			if r := <-done; r.p != nil {
				_ = r.p.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

func start(c *ssh.Client, command string) (*sshProcess, error) {
	s, err := c.NewSession()
	if err != nil {
		return nil, err
	}
	stdin, err := s.StdinPipe()
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	stdout, err := s.StdoutPipe()
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := s.Start(command); err != nil {
		_ = s.Close()
		return nil, err
	}
	return &sshProcess{s: s, stdin: stdin, stdout: stdout}, nil
}

// sshProcess is a command on the server. Closing it closes the channel, which
// does not stop the command: it sees end-of-file on stdin.
type sshProcess struct {
	s      *ssh.Session
	stdin  io.WriteCloser
	stdout io.Reader
}

func (p *sshProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *sshProcess) Stdout() io.Reader     { return p.stdout }
func (p *sshProcess) Wait() error           { return p.s.Wait() }
func (p *sshProcess) Close() error          { return p.s.Close() }
