package tunnel

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func pubKey(t *testing.T, kind string) ssh.PublicKey {
	t.Helper()
	var k any
	var err error
	switch kind {
	case "ed25519":
		k, _, err = ed25519.GenerateKey(rand.Reader)
	case "ecdsa":
		var p *ecdsa.PrivateKey
		p, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		k = &p.PublicKey
	case "rsa":
		var p *rsa.PrivateKey
		p, err = rsa.GenerateKey(rand.Reader, 2048)
		k = &p.PublicKey
	}
	if err != nil {
		t.Fatal(err)
	}
	pk, err := ssh.NewPublicKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pk
}

var anyRemote = &net.TCPAddr{IP: net.IPv4zero}

func checkKey(t *testing.T, files []string, addr string, key ssh.PublicKey) ([]string, error) {
	t.Helper()
	cb, algos, err := newHostKeys(files).check(addr)
	if err != nil {
		t.Fatal(err)
	}
	return algos, cb(addr, anyRemote, key)
}

func TestUnknownHostThenTrust(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ssh", "known_hosts") // directory does not exist yet
	key := pubKey(t, "ed25519")
	for _, c := range []struct{ addr, host string }{{"bastion:22", "bastion"}, {"bastion:2222", "[bastion]:2222"}} {
		addr, host := c.addr, c.host
		_, err := checkKey(t, []string{file}, addr, key)
		var ue *UnknownHostError
		if !errors.As(err, &ue) {
			t.Fatalf("%s: err = %v, want UnknownHostError", addr, err)
		}
		if ue.Host != host || ue.File != file || !strings.HasPrefix(ue.Fingerprint(), "SHA256:") {
			t.Fatalf("%s: %+v fingerprint %s", addr, ue, ue.Fingerprint())
		}
		if !strings.Contains(ue.Error(), ue.Fingerprint()) {
			t.Fatalf("message lacks the fingerprint: %s", ue.Error())
		}
		if err := Trust(ue); err != nil {
			t.Fatal(err)
		}
		if _, err := checkKey(t, []string{file}, addr, key); err != nil {
			t.Fatalf("%s: trusted key rejected: %v", addr, err)
		}
	}
	data, _ := os.ReadFile(file)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "bastion ") || !strings.HasPrefix(lines[1], "[bastion]:2222 ") {
		t.Fatalf("known_hosts:\n%s", data)
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
}

func TestTrustAddsMissingNewline(t *testing.T) {
	file := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(file, []byte("# no trailing newline"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := pubKey(t, "ed25519")
	if err := Trust(&UnknownHostError{Host: "h", Key: key, File: file}); err != nil {
		t.Fatal(err)
	}
	if _, err := checkKey(t, []string{file}, "h:22", key); err != nil {
		t.Fatalf("appended line must be on its own line: %v", err)
	}
}

func TestHostKeyChanged(t *testing.T) {
	file := filepath.Join(t.TempDir(), "known_hosts")
	old := pubKey(t, "ed25519")
	if err := os.WriteFile(file, []byte(knownhosts.Line([]string{"bastion:22"}, old)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := checkKey(t, []string{file}, "bastion:22", pubKey(t, "ed25519"))
	var ce *HostKeyChangedError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "CHANGED") || !strings.Contains(err.Error(), file+":1") {
		t.Fatalf("message = %s", err)
	}
}

func TestKnownKeyTypeIsRequested(t *testing.T) {
	file := filepath.Join(t.TempDir(), "known_hosts")
	ed, rsaKey := pubKey(t, "ed25519"), pubKey(t, "rsa")
	content := knownhosts.Line([]string{"a:22"}, ed) + "\n" + knownhosts.Line([]string{"b:22"}, rsaKey) + "\n"
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if algos, _ := checkKey(t, []string{file}, "a:22", ed); !slices.Equal(algos, []string{ssh.KeyAlgoED25519}) {
		t.Fatalf("a: algos = %v", algos)
	}
	if algos, _ := checkKey(t, []string{file}, "b:22", rsaKey); !slices.Equal(algos,
		[]string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}) {
		t.Fatalf("b: algos = %v", algos)
	}
	if algos, _ := checkKey(t, []string{file}, "c:22", ed); algos != nil {
		t.Fatalf("unknown host: algos = %v, want the defaults (nil)", algos)
	}
}

func TestHashedAndMissingFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "known_hosts")
	key := pubKey(t, "ecdsa")
	line := knownhosts.HashHostname("bastion") + " " + strings.TrimPrefix(knownhosts.Line([]string{"x"}, key), "x ")
	if err := os.WriteFile(file, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []string{filepath.Join(dir, "missing"), file}
	if _, err := checkKey(t, files, "bastion:22", key); err != nil {
		t.Fatalf("hashed entry not matched: %v", err)
	}
	_, err := checkKey(t, files, "other:22", key)
	var ue *UnknownHostError
	if !errors.As(err, &ue) || ue.File != filepath.Join(dir, "missing") {
		t.Fatalf("new hosts go to the first configured file: %v", err)
	}
}
