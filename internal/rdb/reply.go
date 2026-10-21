package rdb

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"

	"github.com/shepherd-yun/datavim/internal/db"
)

// ReplyKind is the RESP type of a Reply.
type ReplyKind int

const (
	KindString ReplyKind = iota
	KindInt
	KindDouble
	KindBool
	KindBigInt
	KindNil
	KindError
	KindArray
	KindMap
)

// Reply is one console reply with its RESP3 structure.
type Reply struct {
	Kind  ReplyKind
	Str   string // String, Error, BigInt (decimal)
	Int   int64
	Float float64
	Bool  bool
	Elems []Reply    // Array (a RESP3 set too)
	Pairs [][2]Reply // Map, sorted by the keys' text (go-redis maps have no order)
}

// FromAny turns what go-redis Do returns into a Reply. Only network and
// ctx errors are returned as errors; a server error is a KindError reply.
func FromAny(v any, err error) (Reply, error) {
	if err != nil {
		var re redis.Error
		switch {
		case errors.Is(err, redis.Nil):
			return Reply{Kind: KindNil}, nil
		case errors.As(err, &re):
			return Reply{Kind: KindError, Str: re.Error()}, nil
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return Reply{}, err
		default:
			return Reply{}, fmt.Errorf("%w: %w", db.ErrConnLost, err)
		}
	}
	return convert(v), nil
}

func convert(v any) Reply {
	switch v := v.(type) {
	case nil:
		return Reply{Kind: KindNil}
	case string:
		return Reply{Kind: KindString, Str: v}
	case int64:
		return Reply{Kind: KindInt, Int: v}
	case float64:
		return Reply{Kind: KindDouble, Float: v}
	case bool:
		return Reply{Kind: KindBool, Bool: v}
	case *big.Int:
		return Reply{Kind: KindBigInt, Str: v.String()}
	case redis.Error:
		return Reply{Kind: KindError, Str: v.Error()}
	case []any:
		r := Reply{Kind: KindArray, Elems: make([]Reply, len(v))}
		for i, e := range v {
			r.Elems[i] = convert(e)
		}
		return r
	case map[any]any:
		r := Reply{Kind: KindMap}
		for k, e := range v {
			r.Pairs = append(r.Pairs, [2]Reply{convert(k), convert(e)})
		}
		slices.SortFunc(r.Pairs, func(a, b [2]Reply) int { return strings.Compare(a[0].Text(), b[0].Text()) })
		return r
	default:
		return Reply{Kind: KindString, Str: fmt.Sprint(v)}
	}
}

// Text is a scalar reply as plain text (no type hint); "" for nil and
// containers.
func (r Reply) Text() string {
	switch r.Kind {
	case KindString, KindError, KindBigInt:
		return r.Str
	case KindInt:
		return strconv.FormatInt(r.Int, 10)
	case KindDouble:
		return strconv.FormatFloat(r.Float, 'g', -1, 64)
	case KindBool:
		return strconv.FormatBool(r.Bool)
	}
	return ""
}
