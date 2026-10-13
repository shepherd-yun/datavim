package tunnel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kevinburke/ssh_config"
)

const testSSHConfig = `
Host bastion
  HostName bastion.example.com
  User ops
  Port 2222
  IdentityFile ~/.ssh/ops_ed25519
  IdentityFile %d/.ssh/backup_%r

Host db-private
  HostName 10.0.1.5
  ProxyJump bastion

Host chain
  HostName 10.9.9.9
  ProxyJump admin@hop1:2200,hop2

Host hop1
  ProxyJump bastion

Host loop-a
  ProxyJump loop-b
Host loop-b
  ProxyJump loop-a

Host legacy
  ProxyCommand ssh -W %h:%p bastion

Host custom-known
  UserKnownHostsFile ~/kh1 /etc/kh2

Host bad-port
  Port ssh

Host *
  User fallback
`

var testEnv = Env{Home: "/home/me", User: "me"}

func decode(t *testing.T, src string) *ssh_config.Config {
	t.Helper()
	cfg, err := ssh_config.Decode(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestResolve(t *testing.T) {
	cfg := decode(t, testSSHConfig)
	kh := []string{"/home/me/.ssh/known_hosts"}
	bastion := Hop{
		Alias: "bastion", Host: "bastion.example.com", Port: 2222, User: "ops",
		Identities: []string{"/home/me/.ssh/ops_ed25519", "/home/me/.ssh/backup_ops"},
		KnownHosts: kh,
	}
	cases := map[string][]Hop{
		"bastion": {bastion},
		"db-private": {
			bastion,
			{Alias: "db-private", Host: "10.0.1.5", Port: 22, User: "fallback", KnownHosts: kh},
		},
		// hop1's own ProxyJump is honoured; user and port come from the jump spec.
		"chain": {
			bastion,
			{Alias: "hop1", Host: "hop1", Port: 2200, User: "admin", KnownHosts: kh},
			{Alias: "hop2", Host: "hop2", Port: 22, User: "fallback", KnownHosts: kh},
			{Alias: "chain", Host: "10.9.9.9", Port: 22, User: "fallback", KnownHosts: kh},
		},
		"not-in-config": {{Alias: "not-in-config", Host: "not-in-config", Port: 22, User: "fallback", KnownHosts: kh}},
		"custom-known": {{
			Alias: "custom-known", Host: "custom-known", Port: 22, User: "fallback",
			KnownHosts: []string{"/home/me/kh1", "/etc/kh2"},
		}},
	}
	for alias, want := range cases {
		got, err := Resolve(cfg, alias, testEnv)
		if err != nil {
			t.Errorf("Resolve(%q): %v", alias, err)
			continue
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("Resolve(%q) (-want +got):\n%s", alias, diff)
		}
	}
}

func TestResolveErrors(t *testing.T) {
	cfg := decode(t, testSSHConfig)
	for alias, want := range map[string]string{
		"loop-a":   "ProxyJump",
		"legacy":   "ProxyCommand",
		"bad-port": "Port",
	} {
		_, err := Resolve(cfg, alias, testEnv)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve(%q) err = %v, want it to mention %s", alias, err, want)
		}
	}
}

func TestParseJumps(t *testing.T) {
	cases := map[string][]jump{
		"":                {},
		"none":            {},
		"h":               {{host: "h"}},
		"u@h:2200":        {{user: "u", host: "h", port: 2200}},
		"ssh://u@h:22, k": {{user: "u", host: "h", port: 22}, {host: "k"}},
		"[::1]:2222":      {{host: "::1", port: 2222}},
	}
	for in, want := range cases {
		got, err := parseJumps(in)
		if err != nil {
			t.Errorf("parseJumps(%q): %v", in, err)
			continue
		}
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if diff := cmp.Diff(want, got, cmp.AllowUnexported(jump{})); diff != "" {
			t.Errorf("parseJumps(%q) (-want +got):\n%s", in, diff)
		}
	}
	for _, bad := range []string{"h:0", "h:x", "u@"} {
		if _, err := parseJumps(bad); err == nil {
			t.Errorf("parseJumps(%q) should fail", bad)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(filepath.Join(dir, "missing"))
	if err != nil {
		t.Fatalf("a missing file is an empty config: %v", err)
	}
	hops, err := Resolve(cfg, "box", testEnv)
	if err != nil || len(hops) != 1 || hops[0].User != "me" || hops[0].Port != 22 {
		t.Fatalf("hops = %+v, err = %v", hops, err)
	}
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("Host box\n  HostName 1.2.3.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if hops, _ := Resolve(cfg, "box", testEnv); hops[0].Host != "1.2.3.4" {
		t.Fatalf("hops = %+v", hops)
	}
}
