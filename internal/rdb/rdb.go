// Package rdb is datavim's only link to Redis servers, what internal/db is
// for SQL ones. The UI never imports go-redis. Keys and values are Go
// strings used as byte strings: nothing here converts or escapes them.
package rdb

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/redit"
)

// Config is what Open needs.
type Config struct {
	Host string
	Port int
	// User is the ACL user (Redis 6+); empty authenticates with the
	// password alone.
	User, Password string
	DB             int
	TLS            bool
	TLSSkipVerify  bool
	// Dial opens the network connection (an SSH tunnel); nil dials directly.
	Dial db.DialFunc
}

// ErrAuthRequired means the server wants a password (NOAUTH).
var ErrAuthRequired = errors.New("redis: authentication required")

// DBInfo is one logical database and its key count.
type DBInfo struct {
	Index int
	Keys  int64
}

// KeyEntry is a scanned key and its TYPE ("string", "hash", or a module
// type such as "ReJSON-RL").
type KeyEntry struct{ Key, Type string }

// ScanPage is one SCAN reply; Cursor 0 means the scan is over.
type ScanPage struct {
	Keys   []KeyEntry
	Cursor uint64
}

// Client is a connection pool fixed to one db number, safe for concurrent
// use. Commands that change connection state (SELECT, MULTI) never run on it.
type Client interface {
	// Version is redis_version from INFO server.
	Version() string
	DBSize(ctx context.Context) (int64, error)
	// Databases lists the db numbers with their key counts.
	Databases(ctx context.Context) ([]DBInfo, error)
	// Scan sends one SCAN and returns the keys that still exist, with types.
	Scan(ctx context.Context, cursor uint64, match string, count int64) (ScanPage, error)

	KeyInfo(ctx context.Context, key string) (KeyInfo, error)
	GetString(ctx context.Context, key string, maxBytes int64) (string, error)
	HashPage(ctx context.Context, key string, cursor uint64, match string, count int64) ([][2]string, uint64, error)
	ListPage(ctx context.Context, key string, start, count int64) ([]string, error)
	SetPage(ctx context.Context, key string, cursor uint64, match string, count int64) ([]string, uint64, error)
	ZSetPage(ctx context.Context, key string, start, count int64) ([]ZMember, error)
	StreamPage(ctx context.Context, key, after string, count int64) ([]StreamEntry, error)

	// Apply runs p on a dedicated connection: WATCH p.Key, the checks (one
	// pipeline), MULTI, p.Cmds, EXEC. A check that does not hold or fails,
	// or an aborted EXEC, is *ChangedError and nothing is written. A
	// command failing inside EXEC is *ExecError.
	Apply(ctx context.Context, p redit.Plan) error
	// Expire sets the TTL (ttl < 0: PERSIST); false when the key is gone.
	Expire(ctx context.Context, key string, ttl time.Duration) (bool, error)
	// Rename is RENAMENX, or RENAME when overwrite; false when to exists
	// and !overwrite. A missing from is the server's error ("ERR no such key").
	Rename(ctx context.Context, from, to string, overwrite bool) (bool, error)
	Unlink(ctx context.Context, keys ...string) (int64, error)
	// ScanKeys is one SCAN without the TYPE lookups.
	ScanKeys(ctx context.Context, cursor uint64, match string, count int64) ([]string, uint64, error)

	// Info sends INFO with no section: the default sections (Redis 5 takes one section at most).
	Info(ctx context.Context) (Info, error)
	// SlowLog is SLOWLOG GET n, newest first.
	SlowLog(ctx context.Context, n int64) ([]SlowEntry, error)
	Clients(ctx context.Context) ([]ClientInfo, error)
	// KillClient is CLIENT KILL ID id; false when no such client.
	KillClient(ctx context.Context, id int64) (bool, error)
	// KeySizes reads every key's length in one pipeline, plus MEMORY USAGE
	// key SAMPLES 0 when memory. Keys gone meanwhile (or whose type changed)
	// are left out; a MEMORY error leaves Mem at -1 and is not an error.
	KeySizes(ctx context.Context, keys []KeyEntry, memory bool) ([]KeySize, error)

	Close() error
}

type client struct {
	rc      *redis.Client
	db      int
	version string
}

// Open builds the pool and checks it with PING and INFO server. go-redis
// falls back to RESP2 by itself when the server rejects HELLO (Redis 5).
// An ACL user may not be allowed PING or INFO (+@read has neither): a
// NOPERM reply still means the connection and the login work, and the
// version is then "".
func Open(ctx context.Context, cfg Config) (Client, error) {
	opt := options(cfg)
	opt.PoolSize = 4
	c := &client{rc: redis.NewClient(opt), db: cfg.DB}
	if err := c.rc.Ping(ctx).Err(); err != nil && !IsNoPerm(err) {
		_ = c.rc.Close()
		return nil, wrap(ctx, err)
	}
	info, err := c.rc.Info(ctx, "server").Result()
	switch {
	case IsNoPerm(err):
	case err != nil:
		_ = c.rc.Close()
		return nil, wrap(ctx, err)
	default:
		c.version = infoValue(info, "redis_version")
	}
	return c, nil
}

// options are the go-redis settings Open and OpenSession share: RESP3
// first, ctx deadlines, no retries.
func options(cfg Config) *redis.Options {
	opt := &redis.Options{
		Addr:                     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Username:                 cfg.User,
		Password:                 cfg.Password,
		DB:                       cfg.DB,
		Protocol:                 3,
		ContextTimeoutEnabled:    true,
		DisableIdentity:          true,
		MaxRetries:               -1,
		MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled},
	}
	if cfg.Dial != nil {
		opt.Dialer = cfg.Dial
	}
	if cfg.TLS {
		opt.TLSConfig = &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: cfg.TLSSkipVerify, MinVersion: tls.VersionTLS12} //nolint:gosec // opt-in via tls_skip_verify
	}
	return opt
}

func (c *client) Version() string { return c.version }
func (c *client) Close() error    { return c.rc.Close() }

func (c *client) DBSize(ctx context.Context) (int64, error) {
	n, err := c.rc.DBSize(ctx).Result()
	return n, wrap(ctx, err)
}

// Databases asks CONFIG GET databases for the count; when CONFIG is not
// allowed it lists the dbs INFO keyspace has keys in, plus the configured one.
// An ACL user allowed neither (+@read) sees the configured db, counted
// with DBSIZE.
func (c *client) Databases(ctx context.Context) ([]DBInfo, error) {
	ks, err := c.rc.Info(ctx, "keyspace").Result()
	if err != nil && !IsNoPerm(err) {
		return nil, wrap(ctx, err)
	}
	keys := parseKeyspace(ks)
	if err != nil {
		n, err := c.rc.DBSize(ctx).Result()
		if err != nil {
			return nil, wrap(ctx, err)
		}
		keys[c.db] = n
	}
	n := 0
	if m, err := c.rc.ConfigGet(ctx, "databases").Result(); err == nil {
		n, _ = strconv.Atoi(m["databases"])
	} else if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var out []DBInfo
	if n > 0 {
		for i := range n {
			out = append(out, DBInfo{Index: i, Keys: keys[i]})
		}
		return out, nil
	}
	if _, ok := keys[c.db]; !ok {
		keys[c.db] = 0
	}
	for i := range keys {
		out = append(out, DBInfo{Index: i, Keys: keys[i]})
	}
	sortDBs(out)
	return out, nil
}

func sortDBs(dbs []DBInfo) {
	for i := 1; i < len(dbs); i++ {
		for j := i; j > 0 && dbs[j].Index < dbs[j-1].Index; j-- {
			dbs[j], dbs[j-1] = dbs[j-1], dbs[j]
		}
	}
}

// parseKeyspace reads "db0:keys=3,expires=0,avg_ttl=0" lines.
func parseKeyspace(info string) map[int]int64 {
	return ParseInfo(info).keyspace()
}

// infoValue is one field of an INFO reply.
func infoValue(info, field string) string {
	for line := range strings.SplitSeq(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), field+":"); ok {
			return v
		}
	}
	return ""
}

// wrap turns go-redis errors into datavim's: NOAUTH is ErrAuthRequired,
// server replies stay as they are, cancellation is ctx.Err() and
// everything else (network, closed pool) is db.ErrConnLost.
func wrap(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, redis.Nil) {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var re redis.Error
	if errors.As(err, &re) {
		if strings.HasPrefix(re.Error(), "NOAUTH") {
			return fmt.Errorf("%w: %s", ErrAuthRequired, re.Error())
		}
		return err
	}
	return fmt.Errorf("%w: %w", db.ErrConnLost, err)
}
