package secret

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestEnvVar(t *testing.T) {
	if got := EnvVar("prod-pg.main"); got != "DATAVIM_PASSWORD_PROD_PG_MAIN" {
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
		if name == "DATAVIM_PASSWORD_LOCAL" {
			return "from-env"
		}
		return ""
	}}
	_ = k.Set("local", "from-keychain")
	if got, _ := k.Get("local"); got != "from-env" {
		t.Fatalf("Get = %q, want from-env", got)
	}
}

func TestKeyringDelete(t *testing.T) {
	keyring.MockInit()
	k := Keyring{Getenv: func(string) string { return "" }}
	if err := k.Delete("local"); err != nil {
		t.Fatalf("deleting a missing password is not an error: %v", err)
	}
	if err := k.Set("local", "pw"); err != nil {
		t.Fatal(err)
	}
	if err := k.Delete("local"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get("local"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete: %v", err)
	}
}

func TestKeyringPrefixKeepsWorkspacesApart(t *testing.T) {
	keyring.MockInit()
	noEnv := func(string) string { return "" }
	work := Keyring{Getenv: noEnv}
	personal := Keyring{Getenv: noEnv, Prefix: "personal/"}

	if err := work.Set("local", "work-pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := personal.Get("local"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("personal sees the default workspace's password: %v", err)
	}
	if err := personal.Set("local", "home-pw"); err != nil {
		t.Fatal(err)
	}
	if got, _ := work.Get("local"); got != "work-pw" {
		t.Fatalf("default Get = %q, want work-pw", got)
	}
	if got, _ := personal.Get("local"); got != "home-pw" {
		t.Fatalf("personal Get = %q, want home-pw", got)
	}
	if got, err := keyring.Get("datavim", "personal/local"); err != nil || got != "home-pw" {
		t.Fatalf("keychain account personal/local = %q, %v", got, err)
	}
	if err := personal.Delete("local"); err != nil {
		t.Fatal(err)
	}
	if got, _ := work.Get("local"); got != "work-pw" {
		t.Fatalf("deleting in personal removed the default password: %q", got)
	}
}

func TestEnvOverrideIgnoresPrefix(t *testing.T) {
	keyring.MockInit()
	k := Keyring{Prefix: "personal/", Getenv: func(name string) string {
		if name == "DATAVIM_PASSWORD_LOCAL" {
			return "from-env"
		}
		return ""
	}}
	if got, _ := k.Get("local"); got != "from-env" {
		t.Fatalf("Get = %q, want from-env", got)
	}
}

func TestGetMovesLegacyPassword(t *testing.T) {
	keyring.MockInit()
	k := Keyring{Getenv: func(string) string { return "" }, Prefix: "ws/"}
	if err := keyring.Set(legacyService, "ws/local", "old-pw"); err != nil {
		t.Fatal(err)
	}
	if got, err := k.Get("local"); err != nil || got != "old-pw" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if got, err := keyring.Get(service, "ws/local"); err != nil || got != "old-pw" {
		t.Fatalf("moved = %q, %v", got, err)
	}
	if _, err := keyring.Get(legacyService, "ws/local"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("legacy still there: %v", err)
	}
}

func TestLegacyEnvVarStillWorks(t *testing.T) {
	keyring.MockInit()
	k := Keyring{Getenv: func(name string) string {
		if name == "SQLVIM_PASSWORD_LOCAL" {
			return "from-old-env"
		}
		return ""
	}}
	if got, err := k.Get("local"); err != nil || got != "from-old-env" {
		t.Fatalf("Get = %q, %v", got, err)
	}
}

func TestKeyringUnavailable(t *testing.T) {
	keyring.MockInitWithError(errors.New(`exec: "dbus-launch": executable file not found in $PATH`))
	t.Cleanup(keyring.MockInit)
	k := Keyring{Getenv: func(string) string { return "" }}
	if _, err := k.Get("local"); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNotFound) {
		t.Fatalf("Get: %v", err)
	}
	if err := k.Set("local", "pw"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Set: %v", err)
	}
	if err := k.Delete("local"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Delete: %v", err)
	}
	env := Keyring{Getenv: func(string) string { return "from-env" }}
	if pw, err := env.Get("local"); err != nil || pw != "from-env" {
		t.Fatalf("the env var still wins: %q, %v", pw, err)
	}
}
