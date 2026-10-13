package ui

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/crypto/ssh"

	"github.com/shepherd-yun/sqlvim/internal/db/tunnel"
)

func testHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestPassphrasePromptRetriesTreeLoad(t *testing.T) {
	m, b := newTestModel(t)
	b.setConnErr(fmt.Errorf("connect local via ssh bastion: %w", &tunnel.PassphraseError{Path: "/home/me/.ssh/id_ed25519"}))
	press(t, m, "l")
	if s := screen(m); !strings.Contains(s, "Passphrase for /home/me/.ssh/id_ed25519") {
		t.Fatalf("screen:\n%s", s)
	}
	if s := screen(m); strings.Contains(s, "needs a passphrase") {
		t.Fatalf("the tree shows no error while the prompt is open:\n%s", s)
	}
	press(t, m, "s", "3", "enter")
	b.mu.Lock()
	got := slices.Clone(b.passphrases)
	b.mu.Unlock()
	if !slices.Equal(got, []string{"/home/me/.ssh/id_ed25519:s3"}) {
		t.Fatalf("passphrases = %v", got)
	}
	if s := screen(m); !strings.Contains(s, "app") || strings.Contains(s, "Passphrase") {
		t.Fatalf("the tree load is retried:\n%s", s)
	}
}

func TestWrongPassphraseSaysSo(t *testing.T) {
	o := sshPrompt(&tunnel.PassphraseError{Path: "/k", Wrong: true})
	if o == nil || !strings.Contains(ansi.Strip(o.View(80, 20)), "Wrong passphrase") {
		t.Fatal("wrong passphrase prompt")
	}
	if sshPrompt(fmt.Errorf("dial tcp: connection refused")) != nil {
		t.Fatal("other errors need no prompt")
	}
}

func TestUnknownHostPromptNeedsYes(t *testing.T) {
	m, b := newTestModel(t)
	ue := &tunnel.UnknownHostError{Host: "[bastion]:2222", Key: testHostKey(t), File: "/home/me/.ssh/known_hosts"}
	b.setConnErr(fmt.Errorf("connect local via ssh bastion: %w", ue))
	press(t, m, "l")
	s := screen(m)
	if !strings.Contains(s, ue.Fingerprint()) || !strings.Contains(s, "[bastion]:2222") {
		t.Fatalf("screen:\n%s", s)
	}
	press(t, m, "y", "enter")
	b.mu.Lock()
	n := len(b.trusted)
	b.mu.Unlock()
	if n != 0 {
		t.Fatal("y alone does not trust a host; yes must be typed")
	}
	press(t, m, "e", "s", "enter")
	b.mu.Lock()
	trusted := slices.Clone(b.trusted)
	b.mu.Unlock()
	if !slices.Equal(trusted, []string{"[bastion]:2222"}) {
		t.Fatalf("trusted = %v", trusted)
	}
	if s := screen(m); !strings.Contains(s, "app") {
		t.Fatalf("the tree load is retried:\n%s", s)
	}
}

func TestSSHPromptFromConsole(t *testing.T) {
	m, b := newTestModel(t)
	c := openLocalConsole(t, m)
	b.setConnErr(fmt.Errorf("connect local via ssh bastion: %w", &tunnel.PassphraseError{Path: "/k"}))
	runInConsole(t, m, c, "select 1")
	if !strings.Contains(screen(m), "Passphrase for /k") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	press(t, m, "p", "enter")
	if s := statusLine(m); !strings.Contains(s, "run it again") {
		t.Fatalf("status: %s", s)
	}
	runInConsole(t, m, c, "select 1")
	if got := b.conn.lastQuery(); got != "select 1" {
		t.Fatalf("last query = %q", got)
	}
}
