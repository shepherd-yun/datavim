package ui

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db/tunnel"
	"github.com/shepherd-yun/sqlvim/internal/session"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

type (
	passphraseMsg    struct{ path, passphrase string }
	hostTrustedMsg   struct{ host *tunnel.UnknownHostError }
	secretEnteredMsg struct{ conn, kind, value string }
)

// userPrompt is the popup that resolves err when err needs the user: SSH's
// questions, or a secret other than the connection's password.
func (m *Model) userPrompt(err error) overlay.Overlay {
	if o := sshPrompt(err); o != nil {
		return o
	}
	return m.secretPrompt(err)
}

// secretPrompt asks for a *session.SecretRequiredError's secret; nil for
// other errors.
func (m *Model) secretPrompt(err error) overlay.Overlay {
	var se *session.SecretRequiredError
	if !errors.As(err, &se) {
		return nil
	}
	cfg, _ := m.backend.Connection(se.Conn)
	title, label := "Schema Registry", "Password for "+cfg.Params["schema_registry_user"]+":"
	switch se.Kind {
	case session.SecretOAuth:
		title, label = "OAuth", "Client secret for "+cfg.Params["oauth_client_id"]+":"
	case session.SecretConnect:
		title, label = "Kafka Connect", "Password for "+cfg.Params["connect_user"]+":"
	}
	if se.Wrong {
		label = "Wrong. " + label
	}
	conn, kind := se.Conn, se.Kind
	return overlay.NewSecret(title, label, func(s string) tea.Msg {
		return secretEnteredMsg{conn: conn, kind: kind, value: s}
	})
}

func (m *Model) secretEntered(msg secretEnteredMsg) tea.Cmd {
	if err := m.backend.SetSecret(msg.conn, msg.kind, msg.value); err != nil {
		m.setStatus(err.Error(), true)
	} else if m.retryNode == nil {
		m.setStatus("secret saved · run it again", false)
	}
	return m.retryLoad()
}

// sshPrompt returns the popup that resolves err when err needs the user: a
// key passphrase, or accepting an unknown host key. Otherwise nil.
func sshPrompt(err error) overlay.Overlay {
	var pe *tunnel.PassphraseError
	var ue *tunnel.UnknownHostError
	switch {
	case errors.As(err, &pe):
		label := "Passphrase for " + pe.Path + ":"
		if pe.Wrong {
			label = "Wrong passphrase. Passphrase for " + pe.Path + ":"
		}
		path := pe.Path
		return overlay.NewSecret("SSH key", label, func(s string) tea.Msg {
			return passphraseMsg{path: path, passphrase: s}
		})
	case errors.As(err, &ue):
		return overlay.NewConfirm("Unknown SSH host", []string{
			"The authenticity of host " + ue.Host + " can't be established.",
			ue.Key.Type() + " key fingerprint:",
			ue.Fingerprint(),
			"",
			"Compare it with the server's real key before you continue.",
			"Accepting adds the key to " + ue.File + ".",
		}, "yes", hostTrustedMsg{host: ue})
	}
	return nil
}

func (m *Model) passphraseEntered(msg passphraseMsg) tea.Cmd {
	m.backend.SetPassphrase(msg.path, msg.passphrase)
	return m.retryLoad()
}

func (m *Model) hostTrusted(msg hostTrustedMsg) tea.Cmd {
	if err := m.backend.TrustHost(msg.host); err != nil {
		m.setStatus("cannot update known_hosts: "+err.Error(), true)
		return nil
	}
	m.setStatus("added "+msg.host.Host+" to "+msg.host.File, false)
	return m.retryLoad()
}

// retryLoad reloads the tree node whose load needed the user's answer.
// Other operations are simply run again by the user.
func (m *Model) retryLoad() tea.Cmd {
	node := m.retryNode
	m.retryNode = nil
	if node == nil {
		if m.status == "" {
			m.setStatus("ssh ready · run it again", false)
		}
		return nil
	}
	m.tree.StartLoad(node)
	return loadChildren(m.backend, node)
}
