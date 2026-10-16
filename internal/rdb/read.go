package rdb

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// KeyInfo is the header of a key's detail view.
type KeyInfo struct {
	Exists bool
	Type   string
	// TTL is -1 when the key never expires.
	TTL time.Duration
	// Len is STRLEN / HLEN / LLEN / SCARD / ZCARD / XLEN; -1 for other types.
	Len int64
	// Encoding is OBJECT ENCODING, "" when it failed.
	Encoding string
	// Memory is MEMORY USAGE in bytes, -1 when it failed.
	Memory int64
}

// ZMember is a sorted set member and its score.
type ZMember struct {
	Member string
	Score  float64
}

// StreamEntry is one stream entry; Fields keep their order.
type StreamEntry struct {
	ID     string
	Fields [][2]string
}

var lenCommand = map[string]string{
	"string": "strlen", "hash": "hlen", "list": "llen", "set": "scard", "zset": "zcard", "stream": "xlen",
}

// KeyInfo reads TYPE, PTTL, OBJECT ENCODING and MEMORY USAGE in one
// pipeline, then the length command for the type. A missing key is
// Exists == false and no error.
func (c *client) KeyInfo(ctx context.Context, key string) (KeyInfo, error) {
	p := c.rc.Pipeline()
	typ := p.Type(ctx, key)
	pttl := p.Do(ctx, "pttl", key)
	enc := p.ObjectEncoding(ctx, key)
	mem := p.Do(ctx, "memory", "usage", key)
	if _, err := p.Exec(ctx); err != nil && (typ.Err() != nil || pttl.Err() != nil) {
		return KeyInfo{}, wrap(ctx, err)
	}
	info := KeyInfo{Type: typ.Val(), Len: -1, Memory: -1}
	if info.Type == "none" {
		return KeyInfo{}, nil
	}
	info.Exists = true
	if ms, err := pttl.Int64(); err == nil && ms >= 0 {
		info.TTL = time.Duration(ms) * time.Millisecond
	} else {
		info.TTL = -1
	}
	if enc.Err() == nil {
		info.Encoding = enc.Val()
	}
	if n, err := mem.Int64(); err == nil {
		info.Memory = n
	}
	if cmd, ok := lenCommand[info.Type]; ok {
		n, err := c.rc.Do(ctx, cmd, key).Int64()
		if err != nil {
			return KeyInfo{}, wrap(ctx, err)
		}
		info.Len = n
	}
	return info, nil
}

// GetString reads the first max bytes of a string (GETRANGE key 0 max-1).
func (c *client) GetString(ctx context.Context, key string, maxBytes int64) (string, error) {
	v, err := c.rc.GetRange(ctx, key, 0, maxBytes-1).Result()
	return v, wrap(ctx, err)
}

// HashPage is one HSCAN; the next cursor is 0 at the end.
func (c *client) HashPage(ctx context.Context, key string, cursor uint64, match string, count int64) ([][2]string, uint64, error) {
	kv, next, err := c.rc.HScan(ctx, key, cursor, match, count).Result()
	if err != nil {
		return nil, 0, wrap(ctx, err)
	}
	out := make([][2]string, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, [2]string{kv[i], kv[i+1]})
	}
	return out, next, nil
}

// ListPage is LRANGE key start start+count-1.
func (c *client) ListPage(ctx context.Context, key string, start, count int64) ([]string, error) {
	v, err := c.rc.LRange(ctx, key, start, start+count-1).Result()
	return v, wrap(ctx, err)
}

// SetPage is one SSCAN; the next cursor is 0 at the end.
func (c *client) SetPage(ctx context.Context, key string, cursor uint64, match string, count int64) ([]string, uint64, error) {
	v, next, err := c.rc.SScan(ctx, key, cursor, match, count).Result()
	return v, next, wrap(ctx, err)
}

// ZSetPage is ZRANGE key start start+count-1 WITHSCORES, in rank order.
func (c *client) ZSetPage(ctx context.Context, key string, start, count int64) ([]ZMember, error) {
	zs, err := c.rc.ZRangeWithScores(ctx, key, start, start+count-1).Result()
	if err != nil {
		return nil, wrap(ctx, err)
	}
	out := make([]ZMember, len(zs))
	for i, z := range zs {
		m, _ := z.Member.(string)
		out[i] = ZMember{Member: m, Score: z.Score}
	}
	return out, nil
}

// StreamPage reads count entries after the ID after ("" starts at the
// beginning). It asks for one more and drops after itself, so it does not
// need the exclusive "(" range of Redis 6.2.
func (c *client) StreamPage(ctx context.Context, key, after string, count int64) ([]StreamEntry, error) {
	start, n := "-", count
	if after != "" {
		start, n = after, count+1
	}
	v, err := c.rc.Do(ctx, "xrange", key, start, "+", "count", strconv.FormatInt(n, 10)).Slice()
	if err != nil {
		return nil, wrap(ctx, err)
	}
	out := make([]StreamEntry, 0, len(v))
	for _, item := range v {
		e, ok := streamEntry(item)
		if !ok {
			return nil, errors.New("redis: unexpected XRANGE reply")
		}
		if e.ID == after {
			continue
		}
		out = append(out, e)
	}
	if int64(len(out)) > count {
		out = out[:count]
	}
	return out, nil
}

// streamEntry reads [id, [k, v, …]] from a raw reply (RESP2 or RESP3).
func streamEntry(item any) (StreamEntry, bool) {
	pair, ok := item.([]any)
	if !ok || len(pair) != 2 {
		return StreamEntry{}, false
	}
	id, ok := pair[0].(string)
	if !ok {
		return StreamEntry{}, false
	}
	e := StreamEntry{ID: id}
	switch fv := pair[1].(type) {
	case []any:
		for i := 0; i+1 < len(fv); i += 2 {
			k, _ := fv[i].(string)
			v, _ := fv[i+1].(string)
			e.Fields = append(e.Fields, [2]string{k, v})
		}
	case nil:
	default:
		return StreamEntry{}, false
	}
	return e, true
}
