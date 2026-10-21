package tunnel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/shepherd-yun/datavim/internal/db"
)

// testServer is an SSH server on 127.0.0.1 that accepts the given user keys
// and forwards direct-tcpip channels with net.Dial.
type testServer struct {
	addr    string
	hostKey ssh.Signer
	ln      net.Listener

	mu       sync.Mutex
	accepted int
	conns    []*ssh.ServerConn
	dials    []string
}

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func userKey(t *testing.T) (ed25519.PrivateKey, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func startServer(t *testing.T, authorized ...ssh.PublicKey) *testServer {
	t.Helper()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			for _, k := range authorized {
				if bytes.Equal(k.Marshal(), key.Marshal()) {
					return &ssh.Permissions{}, nil
				}
			}
			return nil, errors.New("unknown key")
		},
	}
	s := &testServer{hostKey: newSigner(t)}
	cfg.AddHostKey(s.hostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln, s.addr = ln, ln.Addr().String()
	t.Cleanup(func() { _ = ln.Close(); s.dropClients() })
	go s.serve(cfg)
	return s
}

func (s *testServer) serve(cfg *ssh.ServerConfig) {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		// Counted before the handshake can finish, so a client that has
		// connected is always counted.
		s.mu.Lock()
		s.accepted++
		s.mu.Unlock()
		go s.handle(nc, cfg)
	}
}

func (s *testServer) handle(nc net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		_ = nc.Close()
		return
	}
	s.mu.Lock()
	s.conns = append(s.conns, sc)
	s.mu.Unlock()
	go ssh.DiscardRequests(reqs) // answers keepalives with "not understood", like OpenSSH
	for nch := range chans {
		if nch.ChannelType() != "direct-tcpip" {
			_ = nch.Reject(ssh.UnknownChannelType, "only direct-tcpip")
			continue
		}
		var req struct {
			Host     string
			Port     uint32
			OrigHost string
			OrigPort uint32
		}
		if err := ssh.Unmarshal(nch.ExtraData(), &req); err != nil {
			_ = nch.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		target := net.JoinHostPort(req.Host, strconv.Itoa(int(req.Port)))
		s.mu.Lock()
		s.dials = append(s.dials, target)
		s.mu.Unlock()
		out, err := net.Dial("tcp", target)
		if err != nil {
			_ = nch.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			_ = out.Close()
			continue
		}
		go ssh.DiscardRequests(creqs)
		go func() { defer func() { _ = ch.Close() }(); _, _ = io.Copy(ch, out) }()
		go func() { defer func() { _ = out.Close() }(); _, _ = io.Copy(out, ch) }()
	}
}

// dropClients disconnects every client, as a restarted server would.
func (s *testServer) dropClients() {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (s *testServer) acceptedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

func (s *testServer) dialed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.dials)
}

// startEcho runs a TCP server that echoes everything back.
func startEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = c.Close() }(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

// startAgent serves an ssh-agent holding keys and returns its socket path.
func startAgent(t *testing.T, keys ...ed25519.PrivateKey) string {
	t.Helper()
	// Unix socket paths are limited to about 104 bytes on macOS, and
	// t.TempDir() paths there are long.
	dir, err := os.MkdirTemp("", "datavim-agent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	kr := agent.NewKeyring()
	for _, k := range keys {
		if err := kr.Add(agent.AddedKey{PrivateKey: k}); err != nil {
			t.Fatal(err)
		}
	}
	sock := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = c.Close() }(); _ = agent.ServeAgent(kr, c) }()
		}
	}()
	return sock
}

// home is a temporary home directory with an ~/.ssh directory.
type home struct {
	t   *testing.T
	dir string
}

func newHome(t *testing.T) *home {
	t.Helper()
	h := &home{t: t, dir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(h.dir, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *home) path(name string) string { return filepath.Join(h.dir, ".ssh", name) }

func (h *home) write(name, content string) {
	h.t.Helper()
	if err := os.WriteFile(h.path(name), []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// addKey writes an OpenSSH private key, encrypted when passphrase is set.
func (h *home) addKey(name string, priv ed25519.PrivateKey, passphrase string) {
	h.t.Helper()
	var block *pem.Block
	var err error
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		h.t.Fatal(err)
	}
	h.write(name, string(pem.EncodeToMemory(block)))
}

// trust writes known_hosts with the servers' host keys.
func (h *home) trust(servers ...*testServer) {
	var b strings.Builder
	for _, s := range servers {
		b.WriteString(knownhosts.Line([]string{s.addr}, s.hostKey.PublicKey()) + "\n")
	}
	h.write("known_hosts", b.String())
}

func (h *home) pool(agentSocket string) *Pool {
	p := NewPool(Options{
		ConfigFile:  h.path("config"),
		Env:         Env{Home: h.dir, User: "tester"},
		AgentSocket: agentSocket,
		DialTimeout: 5 * time.Second,
		Keepalive:   time.Hour,
	})
	h.t.Cleanup(p.Close)
	return p
}

// hostBlock is an ssh_config entry for s.
func hostBlock(alias string, s *testServer, extra ...string) string {
	host, port, _ := net.SplitHostPort(s.addr)
	b := fmt.Sprintf("Host %s\n  HostName %s\n  Port %s\n  User tester\n", alias, host, port)
	for _, e := range extra {
		b += "  " + e + "\n"
	}
	return b
}

// roundTrip sends a line through dial to the echo server at addr.
func roundTrip(t *testing.T, dial db.DialFunc, addr string) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dial(ctx, "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := io.WriteString(c, "ping\n"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping\n" {
		t.Fatalf("echo = %q, %v", buf, err)
	}
	return c
}

// direct sets up one server reachable as "bastion" with the default key.
func direct(t *testing.T) (*testServer, *home, string) {
	t.Helper()
	priv, pub := userKey(t)
	s := startServer(t, pub)
	h := newHome(t)
	h.addKey("id_ed25519", priv, "")
	h.trust(s)
	h.write("config", hostBlock("bastion", s))
	return s, h, startEcho(t)
}
