// Package secret stores connection passwords in the OS keychain.
package secret

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
)

const service = "datavim"

// legacyService is the keychain service (and env var prefix) from before the
// rename to datavim. Get still reads it and moves what it finds over.
const legacyService = "sqlvim"

// ErrNotFound means no password is stored for the connection.
var ErrNotFound = errors.New("secret not found")

// ErrUnavailable means the keychain itself cannot be used: on Linux and
// WSL, no Secret Service (gnome-keyring, KWallet …) on the session D-Bus.
var ErrUnavailable = errors.New("keychain unavailable")

// unavailable marks an error of the keychain other than a missing entry.
func unavailable(err error) error {
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

// Store reads and writes connection passwords.
type Store interface {
	Get(name string) (string, error)
	Set(name, password string) error
	// Delete removes the password; a missing one is not an error.
	Delete(name string) error
}

// Keyring is a Store backed by the macOS Keychain (or the platform
// equivalent: the Secret Service on Linux). Its errors other than
// ErrNotFound are ErrUnavailable.
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
	if v := getenv(legacyEnvVar(name)); v != "" {
		return v, nil
	}
	pw, err := keyring.Get(service, k.Prefix+name)
	if errors.Is(err, keyring.ErrNotFound) {
		return k.getLegacy(name)
	}
	return pw, unavailable(err)
}

// getLegacy reads the password from the legacy service and moves it to the
// current one; a failed move still returns the password.
func (k Keyring) getLegacy(name string) (string, error) {
	pw, err := keyring.Get(legacyService, k.Prefix+name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", unavailable(err)
	}
	if keyring.Set(service, k.Prefix+name, pw) == nil {
		_ = keyring.Delete(legacyService, k.Prefix+name)
	}
	return pw, nil
}

// Set stores the password for the named connection.
func (k Keyring) Set(name, password string) error {
	return unavailable(keyring.Set(service, k.Prefix+name, password))
}

// Delete removes the stored password for the named connection.
func (k Keyring) Delete(name string) error {
	for _, svc := range []string{service, legacyService} {
		if err := keyring.Delete(svc, k.Prefix+name); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return unavailable(err)
		}
	}
	return nil
}

// EnvVar is the environment variable that overrides the keychain, e.g.
// "prod-pg" -> "DATAVIM_PASSWORD_PROD_PG".
func EnvVar(name string) string {
	return "DATAVIM_PASSWORD_" + envSuffix(name)
}

func legacyEnvVar(name string) string {
	return "SQLVIM_PASSWORD_" + envSuffix(name)
}

func envSuffix(name string) string {
	return strings.Map(func(r rune) rune {
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
