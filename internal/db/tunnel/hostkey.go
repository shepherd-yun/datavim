package tunnel

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// UnknownHostError means the server's key is not in known_hosts yet. The UI
// shows the fingerprint and calls Trust once the user accepts it.
type UnknownHostError struct {
	Host string // known_hosts form: host, or [host]:port
	Key  ssh.PublicKey
	File string // the known_hosts file Trust appends to
}

func (e *UnknownHostError) Error() string {
	return fmt.Sprintf("ssh: the authenticity of host %s can't be established (%s key fingerprint %s)",
		e.Host, e.Key.Type(), e.Fingerprint())
}

// Fingerprint is the key's SHA256 fingerprint as OpenSSH prints it.
func (e *UnknownHostError) Fingerprint() string { return ssh.FingerprintSHA256(e.Key) }

// HostKeyChangedError means known_hosts holds a different key for the host,
// which may be a man-in-the-middle attack. datavim never offers to overwrite it.
type HostKeyChangedError struct {
	Host  string
	Key   ssh.PublicKey
	Known []knownhosts.KnownKey
}

func (e *HostKeyChangedError) Error() string {
	k := e.Known[0]
	return fmt.Sprintf("ssh: REMOTE HOST IDENTIFICATION HAS CHANGED for %s: the server sent %s key %s but %s:%d records another key; if the change is expected, remove that line",
		e.Host, e.Key.Type(), ssh.FingerprintSHA256(e.Key), k.Filename, k.Line)
}

// hostKeys checks server keys against a hop's known_hosts files.
type hostKeys struct {
	files []string // configured files, in order; the first receives new hosts
}

func newHostKeys(files []string) hostKeys { return hostKeys{files: files} }

// check returns the host key callback for addr and the key algorithms to ask
// for. Asking only for the types already recorded keeps a host that has, say,
// an ed25519 entry from being reported as changed because it offered ecdsa.
// The files are read on every call so hosts trusted a moment ago are known.
func (k hostKeys) check(addr string) (ssh.HostKeyCallback, []string, error) {
	var existing []string
	for _, f := range k.files {
		if _, err := os.Stat(f); err == nil {
			existing = append(existing, f)
		}
	}
	known, err := knownhosts.New(existing...)
	if err != nil {
		return nil, nil, err
	}
	write := ""
	if len(k.files) > 0 {
		write = k.files[0]
	}
	host := knownhosts.Normalize(addr)
	cb := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := known(hostname, remote, key)
		var ke *knownhosts.KeyError
		switch {
		case errors.As(err, &ke) && len(ke.Want) == 0:
			return &UnknownHostError{Host: host, Key: key, File: write}
		case errors.As(err, &ke):
			return &HostKeyChangedError{Host: host, Key: key, Known: ke.Want}
		}
		return err
	}
	return cb, knownAlgorithms(known, addr), nil
}

// probeKey never matches a recorded key, so checking it lists them all.
var probeKey = func() ssh.PublicKey {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		panic(err)
	}
	return k
}()

func knownAlgorithms(known ssh.HostKeyCallback, addr string) []string {
	var ke *knownhosts.KeyError
	if err := known(addr, &net.TCPAddr{IP: net.IPv4zero}, probeKey); !errors.As(err, &ke) {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(algos ...string) {
		for _, a := range algos {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	for _, w := range ke.Want {
		if t := w.Key.Type(); t == ssh.KeyAlgoRSA {
			add(ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
		} else {
			add(t)
		}
	}
	return out
}

// Trust appends the host's key to its known_hosts file, creating the file
// (0600) and its directory (0700) when needed.
func Trust(e *UnknownHostError) error {
	if e.File == "" {
		return errors.New("ssh: no known_hosts file configured")
	}
	if err := os.MkdirAll(filepath.Dir(e.File), 0o700); err != nil {
		return err
	}
	line := knownhosts.Line([]string{e.Host}, e.Key) + "\n"
	if data, err := os.ReadFile(e.File); err == nil && len(data) > 0 && data[len(data)-1] != '\n' {
		line = "\n" + line
	}
	f, err := os.OpenFile(e.File, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
