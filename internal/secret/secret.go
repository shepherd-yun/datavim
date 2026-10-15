// Package secret stores connection passwords in the OS keychain.
package secret

import (
	"errors"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
)

const service = "sqlvim"

// ErrNotFound means no password is stored for the connection.
var ErrNotFound = errors.New("secret not found")

// Store reads and writes connection passwords.
type Store interface {
	Get(name string) (string, error)
	Set(name, password string) error
	// Delete removes the password; a missing one is not an error.
	Delete(name string) error
}

// Keyring is a Store backed by the macOS Keychain (or the platform equivalent).
// The environment variable EnvVar(name) takes precedence, which is handy on CI.
type Keyring struct {
	// Getenv defaults to os.Getenv.
	Getenv func(string) string
	// Prefix goes before the connection name in the keychain account, so
	// workspaces keep same-named connections apart ("personal/").
	Prefix string
}

// Get returns the password for the named connection.
func (k Keyring) Get(name string) (string, error) {
	getenv := k.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if v := getenv(EnvVar(name)); v != "" {
		return v, nil
	}
	pw, err := keyring.Get(service, k.Prefix+name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return pw, err
}

// Set stores the password for the named connection.
func (k Keyring) Set(name, password string) error {
	return keyring.Set(service, k.Prefix+name, password)
}

// Delete removes the stored password for the named connection.
func (k Keyring) Delete(name string) error {
	if err := keyring.Delete(service, k.Prefix+name); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

// EnvVar is the environment variable that overrides the keychain, e.g.
// "prod-pg" -> "SQLVIM_PASSWORD_PROD_PG".
func EnvVar(name string) string {
	return "SQLVIM_PASSWORD_" + strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, name)
}
