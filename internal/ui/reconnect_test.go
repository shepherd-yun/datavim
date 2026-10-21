package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
)

func TestWithConnRetriesOnceAfterConnectionLoss(t *testing.T) {
	b := newFakeBackend()
	calls := 0
	err := withConn(context.Background(), b, "local", "app", func(db.Conn) error {
		calls++
		if calls == 1 {
			return fmt.Errorf("query: %w", db.ErrConnLost)
		}
		return nil
	})
	if err != nil || calls != 2 || !slices.Equal(b.resetList(), []string{"local"}) {
		t.Fatalf("err=%v calls=%d resets=%v", err, calls, b.resetList())
	}

	calls = 0
	boom := errors.New("syntax error")
	err = withConn(context.Background(), b, "local", "app", func(db.Conn) error { calls++; return boom })
	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("other errors are not retried: err=%v calls=%d", err, calls)
	}

	calls = 0
	err = withConn(context.Background(), b, "local", "app", func(db.Conn) error {
		calls++
		return db.ErrConnLost
	})
	if !db.IsConnLost(err) || calls != 2 {
		t.Fatalf("only one retry: err=%v calls=%d", err, calls)
	}
}

func TestTreeLoadReconnects(t *testing.T) {
	m, b := newTestModel(t)
	b.setLost(1)
	press(t, m, "l")
	if s := screen(m); !strings.Contains(s, "app") || strings.Contains(s, "disconnected") {
		t.Fatalf("one lost connection is retried:\n%s", s)
	}
	if !slices.Equal(b.resetList(), []string{"local"}) {
		t.Fatalf("resets = %v", b.resetList())
	}
}

func TestBrokenConnectionIsMarkedAndRetried(t *testing.T) {
	m, b := newTestModel(t)
	b.setLost(2)
	press(t, m, "l")
	s := screen(m)
	if !strings.Contains(s, "disconnected") || !strings.Contains(s, "connection local lost") {
		t.Fatalf("screen:\n%s", s)
	}
	press(t, m, "enter")
	if s := screen(m); strings.Contains(s, "disconnected") || !strings.Contains(s, "app") {
		t.Fatalf("enter retries:\n%s", s)
	}
}

func TestDataTabReconnectsOnRefresh(t *testing.T) {
	m, b := newTestModel(t)
	openUsers(t, m)
	b.setLost(1)
	press(t, m, "space", "r")
	if s := screen(m); !strings.Contains(s, "user1") || strings.Contains(s, "✗") {
		t.Fatalf("screen:\n%s", s)
	}
	if !slices.Contains(b.resetList(), "local") {
		t.Fatal("the pool was reset")
	}
}

func TestConsoleConnectionLossMarksTree(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setFail("select 1", fmt.Errorf("%w: broken pipe", db.ErrConnLost))
	press(t, m, "l", "j", "space", "c", "esc")
	m.activeTab().(*consoleTab).editor.SetValue("select 1")
	press(t, m, "enter")
	if !strings.Contains(screen(m), "disconnected") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	b.conn.setFail("select 1", nil)
	press(t, m, "enter")
	if strings.Contains(screen(m), "disconnected") {
		t.Fatal("a successful run clears the mark")
	}
}
