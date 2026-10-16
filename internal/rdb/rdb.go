// Package rdb is sqlvim's only link to Redis servers, what internal/db is
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

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"

	"github.com/shepherd-yun/sqlvim/internal/db"
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
	Close() error
}

type client struct {
	rc      *redis.Client
	db      int
	version string
}

// Open builds the pool and checks it with PING and INFO server. go-redis
// falls back to RESP2 by itself when the server rejects HELLO (Redis 5).
func Open(ctx context.Context, cfg Config) (Client, error) {
	opt := &redis.Options{
		Addr:                  net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Username:              cfg.User,
		Password:              cfg.Password,
		DB:                    cfg.DB,
		Protocol:              3,
		PoolSize:              4,
		ContextTimeoutEnabled: true,
		DisableIdentity:       true,
		MaxRetries:            -1,
		MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled},
	}
	if cfg.Dial != nil {
		opt.Dialer = cfg.Dial
	}
	if cfg.TLS {
		opt.TLSConfig = &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: cfg.TLSSkipVerify, MinVersion: tls.VersionTLS12} //nolint:gosec // opt-in via tls_skip_verify
	}
	c := &client{rc: redis.NewClient(opt), db: cfg.DB}
	if err := c.rc.Ping(ctx).Err(); err != nil {
		_ = c.rc.Close()
		return nil, wrap(ctx, err)
	}
	info, err := c.rc.Info(ctx, "server").Result()
	if err != nil {
		_ = c.rc.Close()
		return nil, wrap(ctx, err)
	}
	c.version = infoValue(info, "redis_version")
	return c, nil
}

func (c *client) Version() string { return c.version }
func (c *client) Close() error    { return c.rc.Close() }

func (c *client) DBSize(ctx context.Context) (int64, error) {
	n, err := c.rc.DBSize(ctx).Result()
	return n, wrap(ctx, err)
}

// Databases asks CONFIG GET databases for the count; when CONFIG is not
// allowed it lists the dbs INFO keyspace has keys in, plus the configured one.
func (c *client) Databases(ctx context.Context) ([]DBInfo, error) {
	ks, err := c.rc.Info(ctx, "keyspace").Result()
	if err != nil {
		return nil, wrap(ctx, err)
	}
	keys := parseKeyspace(ks)
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
	out := map[int]int64{}
	for line := range strings.SplitSeq(info, "\n") {
		name, rest, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || !strings.HasPrefix(name, "db") {
			continue
		}
		i, err := strconv.Atoi(name[2:])
		if err != nil {
			continue
		}
		for kv := range strings.SplitSeq(rest, ",") {
			if v, ok := strings.CutPrefix(kv, "keys="); ok {
				out[i], _ = strconv.ParseInt(v, 10, 64)
			}
		}
	}
	return out
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

// wrap turns go-redis errors into sqlvim's: NOAUTH is ErrAuthRequired,
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
	return fmt.Errorf("%w: %v", db.ErrConnLost, err)
}
