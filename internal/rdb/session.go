package rdb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// Session is the console's dedicated server connection: SELECT and MULTI
// stay in effect on it. Not safe for concurrent use.
type Session interface {
	// Do sends one command. A server error is Reply{Kind: KindError} with a
	// nil err; err is only a lost connection (db.ErrConnLost) or ctx's
	// error. A successful SELECT n updates DB().
	Do(ctx context.Context, args ...string) (Reply, error)
	DB() int
	Ping(ctx context.Context) error
	Close() error
}

// errRedial stops go-redis from quietly opening a second connection, which
// would start on the configured db outside any MULTI.
var errRedial = errors.New("redis: session connection was replaced")

type session struct {
	rc *redis.Client

	mu     sync.Mutex
	conn   net.Conn // the one connection; go-redis does not stop a read on ctx
	db     int
	closed bool
}

// OpenSession opens a dedicated connection: its own go-redis client with
// one connection, no read timeout (blocking commands end through ctx), no
// retries, and no quiet reconnects (a second dial fails). cfg.DB is the starting db.
func OpenSession(ctx context.Context, cfg Config) (Session, error) {
	opt := options(cfg)
	opt.PoolSize = 1
	opt.MinIdleConns = 0
	opt.ReadTimeout = -1
	opt.WriteTimeout = 5 * time.Second
	opt.ConnMaxIdleTime = -1
	s := &session{db: cfg.DB}
	dial := opt.Dialer
	if dial == nil {
		dial = redis.NewDialer(opt)
	}
	opt.Dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.conn != nil {
			return nil, errRedial
		}
		c, err := dial(ctx, network, addr)
		s.conn = c
		return c, err
	}
	s.rc = redis.NewClient(opt)
	if err := s.rc.Ping(ctx).Err(); err != nil {
		_ = s.rc.Close()
		return nil, wrap(ctx, err)
	}
	return s, nil
}

func (s *session) DB() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db
}

func (s *session) Do(ctx context.Context, args ...string) (Reply, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return Reply{}, fmt.Errorf("%w: the console connection was closed", db.ErrConnLost)
	}
	in := make([]any, len(args))
	for i, a := range args {
		in[i] = a
	}
	// go-redis only honours ctx deadlines, so a canceled blocking command
	// (BLPOP q 0) is stopped by closing the connection under it.
	stop := context.AfterFunc(ctx, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})
	r, err := FromAny(s.rc.Do(ctx, in...).Result())
	stop()
	if err != nil || ctx.Err() != nil {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		_ = s.rc.Close()
		if ctx.Err() != nil {
			return Reply{}, ctx.Err()
		}
		return Reply{}, err
	}
	if len(args) == 2 && strings.EqualFold(args[0], "SELECT") && r.Kind == KindString && r.Str == "OK" {
		if n, err := strconv.Atoi(args[1]); err == nil {
			s.mu.Lock()
			s.db = n
			s.mu.Unlock()
		}
	}
	return r, nil
}

func (s *session) Ping(ctx context.Context) error {
	r, err := s.Do(ctx, "PING")
	if err != nil {
		return err
	}
	if r.Kind == KindError {
		return errors.New(r.Str)
	}
	return nil
}

func (s *session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.rc.Close()
}
