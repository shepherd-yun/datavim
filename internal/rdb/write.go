package rdb

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/shepherd-yun/datavim/internal/redit"
)

// ChangedError means the key is not as it was loaded: a check did not
// hold, or EXEC was aborted because the WATCHed key was written. Nothing
// was written.
type ChangedError struct{ What string }

func (e *ChangedError) Error() string {
	s := "key changed since loaded; refresh and retry"
	if e.What != "" {
		s += " · " + e.What
	}
	return s
}

// ExecError is a command failing inside EXEC: Redis does not roll back
// the others. Index is from 0.
type ExecError struct {
	Index, Total int
	Err          error
}

func (e *ExecError) Error() string {
	return fmt.Sprintf("command %d of %d failed: %v · the others were applied", e.Index+1, e.Total, e.Err)
}

func (e *ExecError) Unwrap() error { return e.Err }

// applyHook runs between the checks and MULTI; tests set it.
var applyHook func()

func anyArgs(args []string) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = a
	}
	return out
}

// isServerErr is a reply error from the server, not a network one.
func isServerErr(err error) bool {
	var re redis.Error
	return errors.As(err, &re) && !errors.Is(err, redis.Nil)
}

// Apply runs p on a dedicated connection: WATCH p.Key, the checks (one
// pipeline), MULTI, p.Cmds, EXEC. A check that does not hold or fails, or
// an aborted EXEC, is *ChangedError and nothing is written. A command
// failing inside EXEC is *ExecError.
func (c *client) Apply(ctx context.Context, p redit.Plan) error {
	err := c.rc.Watch(ctx, func(tx *redis.Tx) error {
		pttl := int64(0)
		if len(p.Checks) > 0 || p.RestoreTTL {
			var checks []*redis.Cmd
			var ttl *redis.Cmd
			_, err := tx.Pipelined(ctx, func(pipe redis.Pipeliner) error {
				for _, ch := range p.Checks {
					checks = append(checks, pipe.Do(ctx, anyArgs(ch.Args)...))
				}
				if p.RestoreTTL {
					ttl = pipe.Do(ctx, "PTTL", p.Key)
				}
				return nil
			})
			if err != nil && !isServerErr(err) && !errors.Is(err, redis.Nil) {
				return err
			}
			for i, ch := range p.Checks {
				v, err := checks[i].Result()
				switch {
				case errors.Is(err, redis.Nil):
					v = nil
				case err != nil:
					v = err
				}
				if !ch.Holds(v) {
					return &ChangedError{What: ch.What}
				}
			}
			if ttl != nil {
				pttl, _ = ttl.Int64()
			}
		}
		if applyHook != nil {
			applyHook()
		}
		cmds := p.Cmds
		if pttl > 0 {
			cmds = withPExpire(cmds, p.Key, pttl)
		}
		sent, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			for _, cmd := range cmds {
				pipe.Do(ctx, anyArgs(cmd)...)
			}
			return nil
		})
		if errors.Is(err, redis.TxFailedErr) || err == nil || redis.IsExecAbortError(err) {
			return err
		}
		for i, cmd := range sent {
			if e := cmd.Err(); isServerErr(e) {
				return &ExecError{Index: i, Total: len(sent), Err: e}
			}
		}
		return err
	}, p.Key)
	if errors.Is(err, redis.TxFailedErr) {
		return &ChangedError{}
	}
	var ce *ChangedError
	var ee *ExecError
	if errors.As(err, &ce) || errors.As(err, &ee) {
		return err
	}
	return wrap(ctx, err)
}

// withPExpire puts PEXPIRE key ms right after the SET of key.
func withPExpire(cmds [][]string, key string, ms int64) [][]string {
	out := make([][]string, 0, len(cmds)+1)
	for _, cmd := range cmds {
		out = append(out, cmd)
		if len(cmd) > 1 && strings.EqualFold(cmd[0], "SET") && cmd[1] == key {
			out = append(out, []string{"PEXPIRE", key, strconv.FormatInt(ms, 10)})
		}
	}
	return out
}

// Expire sets the TTL (ttl < 0: PERSIST); false when the key is gone.
func (c *client) Expire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if ttl >= 0 {
		ok, err := c.rc.Expire(ctx, key, ttl).Result()
		return ok, wrap(ctx, err)
	}
	ok, err := c.rc.Persist(ctx, key).Result()
	if err != nil || ok {
		return ok, wrap(ctx, err)
	}
	// PERSIST is 0 for a key without a TTL too.
	n, err := c.rc.Exists(ctx, key).Result()
	return n == 1, wrap(ctx, err)
}

// Rename is RENAMENX, or RENAME when overwrite; false when to exists and
// !overwrite. A missing from is the server's error ("ERR no such key").
func (c *client) Rename(ctx context.Context, from, to string, overwrite bool) (bool, error) {
	if overwrite {
		err := c.rc.Rename(ctx, from, to).Err()
		return err == nil, wrap(ctx, err)
	}
	ok, err := c.rc.RenameNX(ctx, from, to).Result()
	return ok, wrap(ctx, err)
}

func (c *client) Unlink(ctx context.Context, keys ...string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	n, err := c.rc.Unlink(ctx, keys...).Result()
	return n, wrap(ctx, err)
}

// ScanKeys is one SCAN without the TYPE lookups.
func (c *client) ScanKeys(ctx context.Context, cursor uint64, match string, count int64) ([]string, uint64, error) {
	keys, next, err := c.rc.Scan(ctx, cursor, match, count).Result()
	return keys, next, wrap(ctx, err)
}
