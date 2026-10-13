package tunnel

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh/knownhosts"
)

func TestTunnelDirect(t *testing.T) {
	s, h, echo := direct(t)
	dial, err := h.pool("").Connect(context.Background(), "bastion")
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, dial, echo)
	if got := s.dialed(); !slices.Equal(got, []string{echo}) {
		t.Fatalf("server forwarded to %v", got)
	}
}

func TestTunnelConnReportsDialedAddress(t *testing.T) {
	_, h, echo := direct(t)
	dial, err := h.pool("").Connect(context.Background(), "bastion")
	if err != nil {
		t.Fatal(err)
	}
	c := roundTrip(t, dial, echo)
	if got := c.RemoteAddr(); got.Network() != "tcp" || got.String() != echo {
		t.Fatalf("RemoteAddr = %s %s; pgx sends cancel requests to it", got.Network(), got)
	}
}

func TestTunnelProxyJump(t *testing.T) {
	priv, pub := userKey(t)
	outer, inner := startServer(t, pub), startServer(t, pub)
	echo := startEcho(t)
	h := newHome(t)
	h.addKey("id_ed25519", priv, "")
	h.trust(outer, inner)
	h.write("config", hostBlock("bastion", outer)+hostBlock("db-host", inner, "ProxyJump bastion"))
	dial, err := h.pool("").Connect(context.Background(), "db-host")
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, dial, echo)
	if got := outer.dialed(); !slices.Equal(got, []string{inner.addr}) {
		t.Fatalf("bastion forwarded to %v, want the inner server", got)
	}
	if got := inner.dialed(); !slices.Equal(got, []string{echo}) {
		t.Fatalf("inner server forwarded to %v", got)
	}
}

func TestTunnelAgentKeyWinsOverLockedKey(t *testing.T) {
	agentKey, agentPub := userKey(t)
	fileKey, _ := userKey(t)
	s := startServer(t, agentPub)
	h := newHome(t)
	h.addKey("id_ed25519", fileKey, "secret") // encrypted, and not accepted anyway
	h.trust(s)
	h.write("config", hostBlock("bastion", s))
	dial, err := h.pool(startAgent(t, agentKey)).Connect(context.Background(), "bastion")
	if err != nil {
		t.Fatalf("the agent key logs in without asking for a passphrase: %v", err)
	}
	roundTrip(t, dial, startEcho(t))
}

func TestTunnelPassphrase(t *testing.T) {
	priv, pub := userKey(t)
	s := startServer(t, pub)
	h := newHome(t)
	h.addKey("work", priv, "secret")
	h.trust(s)
	h.write("config", hostBlock("bastion", s, "IdentityFile ~/.ssh/work"))
	p := h.pool("")
	ctx := context.Background()

	_, err := p.Connect(ctx, "bastion")
	var pe *PassphraseError
	if !errors.As(err, &pe) || pe.Path != h.path("work") || pe.Wrong {
		t.Fatalf("err = %v, want a PassphraseError for the work key", err)
	}
	p.SetPassphrase(h.path("work"), "nope")
	_, err = p.Connect(ctx, "bastion")
	if !errors.As(err, &pe) || !pe.Wrong || strings.Contains(err.Error(), "nope") {
		t.Fatalf("wrong passphrase: err = %v", err)
	}
	p.SetPassphrase(h.path("work"), "secret")
	dial, err := p.Connect(ctx, "bastion")
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, dial, startEcho(t))
}

func TestTunnelUnknownHostIsTrustedOnRequest(t *testing.T) {
	s, h, echo := direct(t)
	h.write("known_hosts", "")
	p := h.pool("")
	ctx := context.Background()
	_, err := p.Connect(ctx, "bastion")
	var ue *UnknownHostError
	if !errors.As(err, &ue) || ue.File != h.path("known_hosts") {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Equal(ue.Key.Marshal(), s.hostKey.PublicKey().Marshal()) {
		t.Fatal("the error carries the server's key")
	}
	if err := p.TrustHost(ue); err != nil {
		t.Fatal(err)
	}
	dial, err := p.Connect(ctx, "bastion")
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, dial, echo)
}

func TestTunnelRefusesChangedHostKey(t *testing.T) {
	s, h, _ := direct(t)
	h.write("known_hosts", knownhosts.Line([]string{s.addr}, newSigner(t).PublicKey())+"\n")
	_, err := h.pool("").Connect(context.Background(), "bastion")
	var ce *HostKeyChangedError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v", err)
	}
	data, _ := os.ReadFile(h.path("known_hosts"))
	if strings.Count(string(data), "\n") != 1 {
		t.Fatalf("known_hosts must not change:\n%s", data)
	}
}

func TestTunnelReconnectsAfterDrop(t *testing.T) {
	s, h, echo := direct(t)
	p := h.pool("")
	dial, err := p.Connect(context.Background(), "bastion")
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, dial, echo)
	s.dropClients()
	waitDead(t, p, "bastion")
	roundTrip(t, dial, echo) // the same dial function reconnects the tunnel
	if n := s.acceptedCount(); n != 2 {
		t.Fatalf("ssh connections = %d, want 2", n)
	}
}

func TestTunnelIsSharedPerAlias(t *testing.T) {
	s, h, _ := direct(t)
	p := h.pool("")
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Connect(context.Background(), "bastion")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := s.acceptedCount(); n != 1 {
		t.Fatalf("ssh connections = %d, want 1 shared tunnel", n)
	}
}

func TestTunnelRejectsProxyCommand(t *testing.T) {
	h := newHome(t)
	h.write("config", "Host legacy\n  ProxyCommand ssh -W %h:%p bastion\n")
	_, err := h.pool("").Connect(context.Background(), "legacy")
	if err == nil || !strings.Contains(err.Error(), "ProxyJump") {
		t.Fatalf("err = %v", err)
	}
}

func waitDead(t *testing.T, p *Pool, alias string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		p.mu.Lock()
		ch := p.chains[alias]
		p.mu.Unlock()
		if ch == nil || !ch.alive() {
			return
		}
	}
	t.Fatal("the tunnel still looks alive after the server dropped it")
}
