package tunnel

import (
	"crypto/x509"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// PassphraseError means a key needs a passphrase datavim does not know yet,
// or the one given was wrong. The UI asks for it and calls SetPassphrase.
type PassphraseError struct {
	Path  string
	Wrong bool
}

func (e *PassphraseError) Error() string {
	if e.Wrong {
		return "ssh: wrong passphrase for key " + e.Path
	}
	return "ssh: key " + e.Path + " needs a passphrase"
}

var defaultIdentities = []string{"id_ed25519", "id_ecdsa", "id_rsa"}

// keys are the signers offered to one hop.
type keys struct {
	signers []ssh.Signer
	locked  string   // the first encrypted key skipped for lack of a passphrase
	agent   net.Conn // kept open until the handshake is over
}

func (k *keys) close() {
	if k.agent != nil {
		_ = k.agent.Close()
	}
}

// loadKeys collects the agent's keys, then the identity files. An encrypted
// key without a known passphrase is skipped and remembered in locked, so the
// user is asked for it only when nothing else logs in.
func (p *Pool) loadKeys(h Hop) (*keys, error) {
	k := &keys{}
	if sock := p.opts.AgentSocket; sock != "" {
		c, err := net.Dial("unix", sock)
		if err != nil {
			slog.Debug("ssh-agent unavailable", "err", err)
		} else if s, err := agent.NewClient(c).Signers(); err != nil {
			_ = c.Close()
			slog.Debug("ssh-agent keys unavailable", "err", err)
		} else {
			k.agent, k.signers = c, s
		}
	}
	files := h.Identities
	if len(files) == 0 {
		for _, n := range defaultIdentities {
			files = append(files, filepath.Join(p.opts.Env.Home, ".ssh", n))
		}
	}
	for _, path := range files {
		s, err := p.parseKey(path)
		var pe *PassphraseError
		switch {
		case errors.As(err, &pe) && pe.Wrong:
			k.close()
			return nil, err
		case errors.As(err, &pe):
			if k.locked == "" {
				k.locked = path
			}
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			slog.Warn("ssh: skipping unreadable key", "path", path, "err", err)
		default:
			k.signers = append(k.signers, s)
		}
	}
	return k, nil
}

// parseKey reads a private key, decrypting it with a passphrase given
// earlier when it is encrypted.
func (p *Pool) parseKey(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := ssh.ParsePrivateKey(data)
	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) {
		return s, err
	}
	pass, ok := p.passphrase(path)
	if !ok {
		return nil, &PassphraseError{Path: path}
	}
	s, err = ssh.ParsePrivateKeyWithPassphrase(data, pass)
	if errors.Is(err, x509.IncorrectPasswordError) {
		p.forgetPassphrase(path)
		return nil, &PassphraseError{Path: path, Wrong: true}
	}
	return s, err
}
