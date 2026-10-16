package rdb

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Info is the fields of an INFO reply ("# Section" and blank lines dropped).
// Keyspace lines keep their raw value: Info["db0"] == "keys=3,expires=0,avg_ttl=0".
type Info map[string]string

// ParseInfo reads an INFO reply.
func ParseInfo(s string) Info {
	out := Info{}
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			out[k] = v
		}
	}
	return out
}

// Int is an integer field; false when missing or not an integer.
func (i Info) Int(field string) (int64, bool) {
	n, err := strconv.ParseInt(i[field], 10, 64)
	return n, err == nil
}

// Float is a numeric field; false when missing or not a number.
func (i Info) Float(field string) (float64, bool) {
	f, err := strconv.ParseFloat(i[field], 64)
	return f, err == nil
}

// Keys is the keys= of every dbN line, summed.
func (i Info) Keys() int64 {
	var n int64
	for _, k := range i.keyspace() {
		n += k
	}
	return n
}

// keyspace is the keys= of each dbN line by db number.
func (i Info) keyspace() map[int]int64 {
	out := map[int]int64{}
	for name, v := range i {
		rest, ok := strings.CutPrefix(name, "db")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(rest)
		if err != nil {
			continue
		}
		for kv := range strings.SplitSeq(v, ",") {
			if s, ok := strings.CutPrefix(kv, "keys="); ok {
				out[n], _ = strconv.ParseInt(s, 10, 64)
			}
		}
	}
	return out
}

// SlowEntry is one SLOWLOG GET entry.
type SlowEntry struct {
	ID       int64
	Time     time.Time
	Duration time.Duration
	Args     []string // as the server truncated them
	Addr     string   // "" before Redis 4
	Name     string
}

// ClientInfo is one CLIENT LIST line: "id=7 addr=… name= age=3 …" as a map.
type ClientInfo map[string]string

func parseClientList(s string) []ClientInfo {
	var out []ClientInfo
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ci := ClientInfo{}
		for f := range strings.FieldsSeq(line) {
			if k, v, ok := strings.Cut(f, "="); ok {
				ci[k] = v
			}
		}
		out = append(out, ci)
	}
	return out
}

// KeySize is one key's length (STRLEN / HLEN / …; -1 for module types)
// and MEMORY USAGE (-1 when not asked for or not allowed).
type KeySize struct {
	Key, Type string
	Len, Mem  int64
}

// IsNoPerm reports whether err is the server's NOPERM (an ACL refusal).
func IsNoPerm(err error) bool {
	var re redis.Error
	return errors.As(err, &re) && strings.HasPrefix(re.Error(), "NOPERM")
}

func (c *client) Info(ctx context.Context) (Info, error) {
	s, err := c.rc.Info(ctx).Result()
	if err != nil {
		return nil, wrap(ctx, err)
	}
	return ParseInfo(s), nil
}

func (c *client) SlowLog(ctx context.Context, n int64) ([]SlowEntry, error) {
	v, err := c.rc.SlowLogGet(ctx, n).Result()
	if err != nil {
		return nil, wrap(ctx, err)
	}
	out := make([]SlowEntry, len(v))
	for i, e := range v {
		out[i] = SlowEntry{ID: e.ID, Time: e.Time, Duration: e.Duration, Args: e.Args, Addr: e.ClientAddr, Name: e.ClientName}
	}
	return out, nil
}

func (c *client) Clients(ctx context.Context) ([]ClientInfo, error) {
	s, err := c.rc.ClientList(ctx).Result()
	if err != nil {
		return nil, wrap(ctx, err)
	}
	return parseClientList(s), nil
}

func (c *client) KillClient(ctx context.Context, id int64) (bool, error) {
	n, err := c.rc.Do(ctx, "client", "kill", "id", id).Int64()
	if err != nil {
		return false, wrap(ctx, err)
	}
	return n > 0, nil
}

func (c *client) KeySizes(ctx context.Context, keys []KeyEntry, memory bool) ([]KeySize, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	type cmds struct {
		typ      *redis.StatusCmd
		size     *redis.Cmd
		mem      *redis.Cmd
		lenKnown bool
	}
	p := c.rc.Pipeline()
	sent := make([]cmds, len(keys))
	for i, k := range keys {
		sent[i].typ = p.Type(ctx, k.Key)
		if cmd, ok := lenCommand[k.Type]; ok {
			sent[i].size = p.Do(ctx, cmd, k.Key)
			sent[i].lenKnown = true
		}
		if memory {
			sent[i].mem = p.Do(ctx, "memory", "usage", k.Key, "samples", "0")
		}
	}
	if _, err := p.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) && !isServerErr(err) {
		return nil, wrap(ctx, err)
	}
	out := make([]KeySize, 0, len(keys))
	for i, k := range keys {
		s := sent[i]
		if s.typ.Err() != nil || s.typ.Val() != k.Type {
			continue
		}
		ks := KeySize{Key: k.Key, Type: k.Type, Len: -1, Mem: -1}
		if s.lenKnown {
			n, err := s.size.Int64()
			if err != nil {
				continue
			}
			ks.Len = n
		}
		if s.mem != nil {
			if n, err := s.mem.Int64(); err == nil {
				ks.Mem = n
			}
		}
		out = append(out, ks)
	}
	return out, nil
}
