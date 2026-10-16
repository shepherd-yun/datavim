package rdb

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

// go-redis logs pool and dial failures to stderr by default, which draws
// over the TUI. Send them to the sqlvim log instead; the error itself still
// reaches the caller.
func init() { redis.SetLogger(slogLogger{}) }

type slogLogger struct{}

func (slogLogger) Printf(ctx context.Context, format string, v ...any) {
	slog.WarnContext(ctx, "go-redis", "log", fmt.Sprintf(format, v...))
}
