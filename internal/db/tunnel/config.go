// Package tunnel opens SSH tunnels described by ~/.ssh/config and gives the
// database drivers a dial function that connects through them.
package tunnel

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kevinburke/ssh_config"
)

// Hop is one SSH server on the way to the database.
type Hop struct {
	Alias      string // the name looked up in ssh_config
	Host       string // HostName, or the alias itself
	Port       int
	User       string
	Identities []string // IdentityFile values, expanded; empty means the defaults
	KnownHosts []string // UserKnownHostsFile values, expanded
}

// Addr is host:port, as dialed and as recorded in known_hosts.
func (h Hop) Addr() string { return net.JoinHostPort(h.Host, strconv.Itoa(h.Port)) }

// Env is what ssh_config tokens expand to.
type Env struct {
	Home string // ~ and %d
	User string // %u, and the default User
}

// maxHops bounds ProxyJump chains (and catches loops).
const maxHops = 8

// LoadConfig parses an ssh_config file; a missing file is an empty config.
func LoadConfig(path string) (*ssh_config.Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &ssh_config.Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	cfg, err := ssh_config.DecodeBytes(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Resolve returns the hops that reach alias: ProxyJump hosts first, alias last.
func Resolve(cfg *ssh_config.Config, alias string, env Env) ([]Hop, error) {
	hops, err := resolve(cfg, alias, env, 0)
	if err != nil {
		return nil, err
	}
	if len(hops) > maxHops {
		return nil, fmt.Errorf("ssh host %q: more than %d ProxyJump hops", alias, maxHops)
	}
	return hops, nil
}

func resolve(cfg *ssh_config.Config, alias string, env Env, depth int) ([]Hop, error) {
	if depth > maxHops {
		return nil, fmt.Errorf("ssh host %q: ProxyJump loop or more than %d hops", alias, maxHops)
	}
	h, jumps, err := lookup(cfg, alias, env)
	if err != nil {
		return nil, err
	}
	if len(jumps) == 0 {
		return []Hop{h}, nil
	}
	// Like ssh -J a,b: a is reached with its own ProxyJump, b through a.
	chain, err := resolve(cfg, jumps[0].host, env, depth+1)
	if err != nil {
		return nil, err
	}
	jumps[0].apply(&chain[len(chain)-1])
	for _, j := range jumps[1:] {
		next, _, err := lookup(cfg, j.host, env)
		if err != nil {
			return nil, err
		}
		j.apply(&next)
		chain = append(chain, next)
	}
	return append(chain, h), nil
}

func lookup(cfg *ssh_config.Config, alias string, env Env) (Hop, []jump, error) {
	get := func(key string) string {
		v, _ := cfg.Get(alias, key)
		return strings.TrimSpace(v)
	}
	if pc := get("ProxyCommand"); pc != "" && !strings.EqualFold(pc, "none") {
		return Hop{}, nil, fmt.Errorf("ssh host %q uses ProxyCommand, which sqlvim does not support; use ProxyJump instead", alias)
	}
	h := Hop{Alias: alias, Host: alias, Port: 22, User: env.User}
	if v := get("HostName"); v != "" {
		h.Host = strings.ReplaceAll(v, "%h", alias)
	}
	if v := get("Port"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return Hop{}, nil, fmt.Errorf("ssh host %q: bad Port %q", alias, v)
		}
		h.Port = p
	}
	if v := get("User"); v != "" {
		h.User = v
	}
	ids, _ := cfg.GetAll(alias, "IdentityFile")
	for _, id := range ids {
		h.Identities = append(h.Identities, expand(strings.TrimSpace(id), env, h))
	}
	known := get("UserKnownHostsFile")
	if known == "" {
		known = "~/.ssh/known_hosts"
	}
	for _, f := range strings.Fields(known) {
		h.KnownHosts = append(h.KnownHosts, expand(f, env, h))
	}
	jumps, err := parseJumps(get("ProxyJump"))
	if err != nil {
		return Hop{}, nil, fmt.Errorf("ssh host %q: %w", alias, err)
	}
	return h, jumps, nil
}

// jump is one entry of ProxyJump: [user@]host[:port].
type jump struct {
	user, host string
	port       int
}

func (j jump) apply(h *Hop) {
	if j.user != "" {
		h.User = j.user
	}
	if j.port != 0 {
		h.Port = j.port
	}
}

func parseJumps(v string) ([]jump, error) {
	if v == "" || strings.EqualFold(v, "none") {
		return nil, nil
	}
	var out []jump
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimPrefix(strings.TrimSpace(part), "ssh://")
		var j jump
		if at := strings.LastIndex(part, "@"); at >= 0 {
			j.user, part = part[:at], part[at+1:]
		}
		j.host = part
		if host, port, err := net.SplitHostPort(part); err == nil {
			p, err := strconv.Atoi(port)
			if err != nil || p < 1 || p > 65535 {
				return nil, fmt.Errorf("bad ProxyJump port in %q", part)
			}
			j.host, j.port = host, p
		}
		if j.host == "" {
			return nil, fmt.Errorf("bad ProxyJump %q", v)
		}
		out = append(out, j)
	}
	return out, nil
}

// expand handles ~ and the %d %u %h %r %% tokens.
func expand(s string, env Env, h Hop) string {
	if s == "~" {
		return env.Home
	}
	if rest, ok := strings.CutPrefix(s, "~/"); ok {
		s = filepath.Join(env.Home, rest)
	}
	return strings.NewReplacer("%%", "%", "%d", env.Home, "%u", env.User, "%h", h.Host, "%r", h.User).Replace(s)
}
