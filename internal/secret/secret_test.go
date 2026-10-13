package secret

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestEnvVar(t *testing.T) {
	if got := EnvVar("prod-pg.main"); got != "SQLVIM_PASSWORD_PROD_PG_MAIN" {
		t.Fatalf("EnvVar = %q", got)
	}
}

func TestKeyringRoundTrip(t *testing.T) {
	keyring.MockInit()
	k := Keyring{Getenv: func(string) string { return "" }}

	if _, err := k.Get("local"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing: err = %v, want ErrNotFound", err)
	}
	if err := k.Set("local", "p@ss:w/rd"); err != nil {
		t.Fatal(err)
	}
	got, err := k.Get("local")
	if err != nil || got != "p@ss:w/rd" {
		t.Fatalf("Get = %q, %v", got, err)
	}
}

func TestEnvOverridesKeyring(t *testing.T) {
	keyring.MockInit()
	k := Keyring{Getenv: func(name string) string {
		if name == "SQLVIM_PASSWORD_LOCAL" {
			return "from-env"
		}
		return ""
	}}
	_ = k.Set("local", "from-keychain")
	if got, _ := k.Get("local"); got != "from-env" {
		t.Fatalf("Get = %q, want from-env", got)
	}
}
