package rdb

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestSlogLoggerWritesToSlog(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	slogLogger{}.Printf(context.Background(), "failed to dial after %d attempts", 5)

	if got := buf.String(); !strings.Contains(got, "failed to dial after 5 attempts") || !strings.Contains(got, "go-redis") {
		t.Fatalf("slog output = %q", got)
	}
}
