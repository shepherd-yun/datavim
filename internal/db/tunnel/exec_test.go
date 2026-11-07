package tunnel

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/shepherd-yun/datavim/internal/db"
)

func runToEnd(t *testing.T, p db.Process, stdin string) (string, error) {
	t.Helper()
	out := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(p.Stdout())
		out <- string(b)
	}()
	if _, err := io.WriteString(p.Stdin(), stdin); err != nil {
		t.Fatal(err)
	}
	if err := p.Stdin().Close(); err != nil {
		t.Fatal(err)
	}
	got := <-out
	return got, p.Wait()
}

func TestPoolExec(t *testing.T) {
	_, h, _ := direct(t)
	exec, err := h.pool("").Exec(context.Background(), "bastion")
	if err != nil {
		t.Fatal(err)
	}
	p, err := exec(context.Background(), "cat")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	got, err := runToEnd(t, p, "hello")
	if err != nil || got != "hello" {
		t.Fatalf("got %q, err %v", got, err)
	}
	p, err = exec(context.Background(), "exit 3")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	_, err = runToEnd(t, p, "")
	var ee *ssh.ExitError
	if !errors.As(err, &ee) || ee.ExitStatus() != 3 {
		t.Fatalf("Wait = %v", err)
	}
}

func TestPoolExecThroughJump(t *testing.T) {
	priv, pub := userKey(t)
	outer, inner := startServer(t, pub), startServer(t, pub)
	h := newHome(t)
	h.addKey("id_ed25519", priv, "")
	h.trust(outer, inner)
	h.write("config", hostBlock("bastion", outer)+hostBlock("db-host", inner, "ProxyJump bastion"))
	exec, err := h.pool("").Exec(context.Background(), "db-host")
	if err != nil {
		t.Fatal(err)
	}
	p, err := exec(context.Background(), "echo inner")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if got, err := runToEnd(t, p, ""); err != nil || got != "inner\n" {
		t.Fatalf("got %q, err %v", got, err)
	}
	if len(outer.commands) != 0 {
		t.Fatalf("the command ran on the jump host: %v", outer.commands)
	}
}

func TestPoolExecReconnects(t *testing.T) {
	s, h, _ := direct(t)
	exec, err := h.pool("").Exec(context.Background(), "bastion")
	if err != nil {
		t.Fatal(err)
	}
	p, err := exec(context.Background(), "cat")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.ReadAll(p.Stdout())
	}()
	s.dropClients()
	select {
	case <-done: // the process sees the end of its output
	case <-time.After(10 * time.Second):
		t.Fatal("stdout did not end when the connection died")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		p2, err := exec(context.Background(), "echo back")
		if err == nil {
			defer func() { _ = p2.Close() }()
			if got, err := runToEnd(t, p2, ""); err == nil && got == "back\n" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not reconnect: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := s.acceptedCount(); n != 2 {
		t.Fatalf("%d connections, want a reconnect", n)
	}
}

func TestPoolExecUnknownHost(t *testing.T) {
	priv, pub := userKey(t)
	s := startServer(t, pub)
	h := newHome(t)
	h.addKey("id_ed25519", priv, "")
	h.write("config", hostBlock("bastion", s))
	_, err := h.pool("").Exec(context.Background(), "bastion")
	var ue *UnknownHostError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v", err)
	}
}

func TestPoolExecRefusedKeepsTunnel(t *testing.T) {
	s, h, _ := direct(t)
	exec, err := h.pool("").Exec(context.Background(), "bastion")
	if err != nil {
		t.Fatal(err)
	}
	s.refuse(true)
	_, err = exec(context.Background(), "true")
	if err == nil || !strings.Contains(err.Error(), "too many sessions") {
		t.Fatalf("err = %v", err)
	}
	s.refuse(false)
	p, err := exec(context.Background(), "echo ok")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if got, err := runToEnd(t, p, ""); err != nil || got != "ok\n" {
		t.Fatalf("got %q, err %v", got, err)
	}
	if n := s.acceptedCount(); n != 1 {
		t.Fatalf("%d connections: a refused channel must not drop the tunnel", n)
	}
}

func TestPoolExecCancelledContext(t *testing.T) {
	_, h, _ := direct(t)
	exec, err := h.pool("").Exec(context.Background(), "bastion")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := exec(ctx, "true"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
