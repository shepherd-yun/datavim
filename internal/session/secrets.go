package session

import (
	"errors"
	"fmt"
)

// The secrets a connection may need besides its password, each kept in
// the keychain account "<conn>#<kind>".
const (
	SecretSchemaRegistry = "schema-registry"
	SecretOAuth          = "oauth"
	SecretConnect        = "connect" // connect_user's password, for every Connect cluster
)

var secretKinds = []string{SecretSchemaRegistry, SecretOAuth, SecretConnect}

// SecretRequiredError is a secret other than the connection's password
// that datavim does not know yet, or one the server refused (Wrong).
type SecretRequiredError struct {
	Conn, Kind string
	Wrong      bool
}

func (e *SecretRequiredError) Error() string {
	switch e.Kind {
	case SecretOAuth:
		return "oauth client secret required"
	case SecretConnect:
		return "kafka connect password required"
	}
	return "schema registry password required"
}

func secretAccount(conn, kind string) string { return conn + "#" + kind }

// accounts are every keychain account of conn: its password and secrets.
func accounts(conn string) []string {
	out := []string{conn}
	for _, k := range secretKinds {
		out = append(out, secretAccount(conn, k))
	}
	return out
}

// SetSecret remembers a secret for this run, saves it to the keychain
// account "<conn>#<kind>" and resets the connection's clients.
func (m *Manager) SetSecret(conn, kind, value string) error {
	acct := secretAccount(conn, kind)
	m.mu.Lock()
	m.passwords[acct] = value
	m.resetLocked(conn)
	m.mu.Unlock()
	if err := m.secrets.Set(acct, value); err != nil {
		return fmt.Errorf("%w: %w", ErrKeychain, err)
	}
	return nil
}

// secretFor reads "<conn>#<kind>": this run's, then the keychain;
// none: *SecretRequiredError.
func (m *Manager) secretFor(conn, kind string) (string, error) {
	s, err := m.password(secretAccount(conn, kind))
	if errors.Is(err, ErrPasswordRequired) {
		return "", &SecretRequiredError{Conn: conn, Kind: kind}
	}
	return s, err
}

// refusedSecret forgets this run's copy of a secret the server refused
// and asks for it again.
func (m *Manager) refusedSecret(conn, kind string) error {
	m.mu.Lock()
	delete(m.passwords, secretAccount(conn, kind))
	m.mu.Unlock()
	return &SecretRequiredError{Conn: conn, Kind: kind, Wrong: true}
}
