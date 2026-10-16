package rdb

import "context"

// scanTypeHook runs between SCAN and the TYPE lookups; tests set it.
var scanTypeHook func()

// Scan sends SCAN cursor [MATCH m] COUNT n, then looks up the batch's
// types in one pipeline. Keys gone in between (TYPE "none") are dropped.
// SCAN … TYPE is not used: it filters by one type and needs Redis 6.
func (c *client) Scan(ctx context.Context, cursor uint64, match string, count int64) (ScanPage, error) {
	keys, next, err := c.rc.Scan(ctx, cursor, match, count).Result()
	if err != nil {
		return ScanPage{}, wrap(ctx, err)
	}
	page := ScanPage{Cursor: next}
	if len(keys) == 0 {
		return page, nil
	}
	if scanTypeHook != nil {
		scanTypeHook()
	}
	types, err := c.types(ctx, keys)
	if err != nil {
		return ScanPage{}, err
	}
	page.Keys = make([]KeyEntry, 0, len(keys))
	for i, k := range keys {
		if types[i] != "none" {
			page.Keys = append(page.Keys, KeyEntry{Key: k, Type: types[i]})
		}
	}
	return page, nil
}

func (c *client) types(ctx context.Context, keys []string) ([]string, error) {
	p := c.rc.Pipeline()
	for _, k := range keys {
		p.Type(ctx, k)
	}
	cmds, err := p.Exec(ctx)
	if err != nil {
		return nil, wrap(ctx, err)
	}
	out := make([]string, len(cmds))
	for i, cmd := range cmds {
		out[i] = cmd.(interface{ Val() string }).Val()
	}
	return out, nil
}
