package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingFilesGivesDefaults(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Connections) != 0 {
		t.Fatalf("connections = %v, want none", cfg.Connections)
	}
	if cfg.Settings != DefaultSettings() {
		t.Fatalf("settings = %+v, want %+v", cfg.Settings, DefaultSettings())
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "connections.toml", `
[[connection]]
name   = "local"
driver = "mysql"
user   = "root"

[[connection]]
name     = "prod-pg"
driver   = "postgres"
host     = "10.0.1.5"
env      = "prod"
params   = { sslmode = "require" }
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	local, prod := cfg.Connections[0], cfg.Connections[1]
	if local.Host != "localhost" || local.Port != 3306 || local.Env != EnvDev {
		t.Errorf("local = %+v, want host localhost, port 3306, env dev", local)
	}
	if local.IsReadOnly() {
		t.Error("dev connection should not be read-only by default")
	}
	if prod.Port != 5432 || !prod.IsReadOnly() {
		t.Errorf("prod = %+v, want port 5432 and read-only", prod)
	}
	if prod.Params["sslmode"] != "require" {
		t.Errorf("params = %v", prod.Params)
	}
}

func TestExplicitReadOnlyOverridesEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "connections.toml", `
[[connection]]
name = "p"
driver = "postgres"
env = "prod"
readonly = false
`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Connections[0].IsReadOnly() {
		t.Error("readonly = false must override env = prod")
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"missing name", "[[connection]]\ndriver = \"mysql\"\n", "name is required"},
		{"duplicate", "[[connection]]\nname=\"a\"\ndriver=\"mysql\"\n[[connection]]\nname=\"a\"\ndriver=\"mysql\"\n", "duplicate"},
		{"unknown driver", "[[connection]]\nname=\"a\"\ndriver=\"oracle\"\n", "unknown driver"},
		{"bad env", "[[connection]]\nname=\"a\"\ndriver=\"mysql\"\nenv=\"qa\"\n", "unknown env"},
		{"unknown key", "[[connection]]\nname=\"a\"\ndriver=\"mysql\"\nhostt=\"x\"\n", "hostt"},
		{"syntax", "[[connection]]\nname = \n", "connections.toml:2:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "connections.toml", tt.body)
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), "connections.toml") {
				t.Fatalf("err = %v, want it to name the file", err)
			}
		})
	}
}

func TestSettingsOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.toml", "page_size = 50\n")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.PageSize != 50 || cfg.Settings.MaxColumnWidth != 40 {
		t.Fatalf("settings = %+v", cfg.Settings)
	}
}

func TestSettingsValidation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.toml", "page_size = 0\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "page_size") {
		t.Fatalf("err = %v, want page_size error", err)
	}
}

func TestDirsHonorXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/config")
	t.Setenv("XDG_STATE_HOME", "/x/state")
	if d, _ := Dir(); d != "/x/config/sqlvim" {
		t.Errorf("Dir = %q", d)
	}
	if d, _ := StateDir(); d != "/x/state/sqlvim" {
		t.Errorf("StateDir = %q", d)
	}
}

func TestDirsDefaultToDotConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/u")
	if d, _ := Dir(); d != "/home/u/.config/sqlvim" {
		t.Errorf("Dir = %q", d)
	}
}
