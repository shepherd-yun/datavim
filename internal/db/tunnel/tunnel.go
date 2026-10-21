package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/shepherd-yun/datavim/internal/db"
)

// Options configures a Pool.
type Options struct {
	ConfigFile  string        // ssh_config, normally ~/.ssh/config
	Env         Env           // home directory and local user
	AgentSocket string        // $SSH_AUTH_SOCK; empty skips the agent
	DialTimeout time.Duration // per hop, TCP connect and handshake; default 15s
	Keepalive   time.Duration // keepalive interval; default 30s
}

// Pool keeps one SSH connection chain per ssh_config alias, shared by every
// database connection that uses it, and reconnects it after it dies.
// Safe for concurrent use.
type Pool struct {
	opts Options

	mu          sync.Mutex
	chains      map[string]*chain
	locks       map[string]*sync.Mutex
	passphrases map[string][]byte
}

// NewPool builds a Pool; nothing is dialed until Connect.
func NewPool(o Options) *Pool {
	if o.DialTimeout == 0 {
		o.DialTimeout = 15 * time.Second
	}
	if o.Keepalive == 0 {
		o.Keepalive = 30 * time.Second
	}
	return &Pool{
		opts:        o,
		chains:      map[string]*chain{},
		locks:       map[string]*sync.Mutex{},
		passphrases: map[string][]byte{},
	}
}

// Connect makes sure the tunnel for alias is up and returns a dial function
// that opens connections from its last hop. The dial function reconnects
// the tunnel if it has died in the meantime.
func (p *Pool) Connect(ctx context.Context, alias string) (db.DialFunc, error) {
	if _, err := p.live(ctx, alias); err != nil {
		return nil, err
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		ch, err := p.live(ctx, alias)
		if err != nil {
			return nil, err
		}
		conn, err := ch.last().DialContext(ctx, network, addr)
		if err != nil {
			var refused *ssh.OpenChannelError
			if !errors.As(err, &refused) && ctx.Err() == nil {
				p.drop(alias, ch) // the tunnel itself failed; reconnect next time
			}
			return nil, fmt.Errorf("ssh %s: open %s: %w", alias, addr, err)
		}
		return &tunnelConn{Conn: conn, remote: tunnelAddr{network: network, addr: addr}}, nil
	}, nil
}

// SetPassphrase remembers a key's passphrase for this run only.
func (p *Pool) SetPassphrase(path, passphrase string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.passphrases[path] = []byte(passphrase)
}

// TrustHost records an unknown host's key in known_hosts.
func (p *Pool) TrustHost(e *UnknownHostError) error { return Trust(e) }

// Close closes every tunnel.
func (p *Pool) Close() {
	p.mu.Lock()
	chains := p.chains
	p.chains = map[string]*chain{}
	p.mu.Unlock()
	for _, ch := range chains {
		ch.close()
	}
}

func (p *Pool) passphrase(path string) ([]byte, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pass, ok := p.passphrases[path]
	return pass, ok
}

func (p *Pool) forgetPassphrase(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.passphrases, path)
}

// lock serialises connecting per alias, so concurrent users share one tunnel.
func (p *Pool) lock(alias string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	l, ok := p.locks[alias]
	if !ok {
		l = &sync.Mutex{}
		p.locks[alias] = l
	}
	return l
}

// live returns the tunnel for alias, connecting it if there is none or the
// previous one died.
func (p *Pool) live(ctx context.Context, alias string) (*chain, error) {
	l := p.lock(alias)
	l.Lock()
	defer l.Unlock()
	p.mu.Lock()
	ch := p.chains[alias]
	p.mu.Unlock()
	if ch != nil && ch.alive() {
		return ch, nil
	}
	ch, err := p.connect(ctx, alias)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.chains[alias] = ch
	p.mu.Unlock()
	go p.keepalive(ch)
	return ch, nil
}

func (p *Pool) drop(alias string, ch *chain) {
	p.mu.Lock()
	if p.chains[alias] == ch {
		delete(p.chains, alias)
	}
	p.mu.Unlock()
	ch.close()
}

// connect dials every hop of alias in turn, each through the previous one.
func (p *Pool) connect(ctx context.Context, alias string) (*chain, error) {
	cfg, err := LoadConfig(p.opts.ConfigFile)
	if err != nil {
		return nil, err
	}
	hops, err := Resolve(cfg, alias, p.opts.Env)
	if err != nil {
		return nil, err
	}
	ch := &chain{dead: make(chan struct{})}
	var prev *ssh.Client
	for _, h := range hops {
		c, err := p.dialHop(ctx, prev, h)
		if err != nil {
			ch.close()
			return nil, err
		}
		ch.clients = append(ch.clients, c)
		prev = c
	}
	for _, c := range ch.clients {
		go func() { _ = c.Wait(); ch.kill() }()
	}
	slog.Info("ssh tunnel up", "alias", alias, "hops", len(hops))
	return ch, nil
}

func (p *Pool) dialHop(ctx context.Context, via *ssh.Client, h Hop) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, p.opts.DialTimeout)
	defer cancel()
	addr := h.Addr()
	fail := func(err error) error { return fmt.Errorf("ssh %s (%s@%s): %w", h.Alias, h.User, addr, err) }

	known, algos, err := newHostKeys(h.KnownHosts).check(addr)
	if err != nil {
		return nil, fail(err)
	}
	k, err := p.loadKeys(h)
	if err != nil {
		return nil, err
	}
	defer k.close()

	var hostErr error
	cfg := &ssh.ClientConfig{
		User: h.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeysCallback(func() ([]ssh.Signer, error) { return k.signers, nil })},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			hostErr = known(hostname, remote, key)
			return hostErr
		},
		HostKeyAlgorithms: algos,
	}

	var conn net.Conn
	if via == nil {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = via.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fail(err)
	}
	// The handshake takes no context: bound it with a deadline and close the
	// connection if ctx ends first.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		switch {
		case hostErr != nil:
			return nil, fail(hostErr)
		case k.locked != "" && strings.Contains(err.Error(), "unable to authenticate"):
			return nil, &PassphraseError{Path: k.locked}
		}
		return nil, fail(err)
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// keepalive pings the tunnel and closes it when the server stops answering,
// so the next use reconnects instead of hanging on a dead TCP connection.
func (p *Pool) keepalive(ch *chain) {
	t := time.NewTicker(p.opts.Keepalive)
	defer t.Stop()
	for {
		select {
		case <-ch.dead:
			return
		case <-t.C:
		}
		if err := ping(ch.last(), p.opts.Keepalive); err != nil {
			slog.Info("ssh tunnel lost", "err", err)
			ch.close()
			return
		}
	}
}

func ping(c *ssh.Client, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() {
		_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return errors.New("keepalive timed out")
	}
}

// chain is the SSH clients of one tunnel, first hop first.
type chain struct {
	clients []*ssh.Client
	dead    chan struct{} // closed once any hop disconnects
	once    sync.Once
}

func (c *chain) last() *ssh.Client { return c.clients[len(c.clients)-1] }

func (c *chain) alive() bool {
	select {
	case <-c.dead:
		return false
	default:
		return true
	}
}

func (c *chain) kill() { c.once.Do(func() { close(c.dead) }) }

func (c *chain) close() {
	c.kill()
	for i := len(c.clients) - 1; i >= 0; i-- {
		_ = c.clients[i].Close()
	}
}

// tunnelConn reports the address it was dialed with as its remote address.
// SSH channels report 0.0.0.0:0, and pgx sends cancel requests to
// RemoteAddr(): without this, Ctrl-c could not reach PostgreSQL.
type tunnelConn struct {
	net.Conn
	remote net.Addr
}

func (c *tunnelConn) RemoteAddr() net.Addr { return c.remote }

type tunnelAddr struct{ network, addr string }

func (a tunnelAddr) Network() string { return a.network }
func (a tunnelAddr) String() string  { return a.addr }
