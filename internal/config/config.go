// Package config loads sqlvim's TOML configuration files.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Env classifies a connection; prod enables safety features.
type Env string

const (
	EnvDev     Env = "dev"
	EnvStaging Env = "staging"
	EnvProd    Env = "prod"
)

// Connection is one entry of connections.toml. Passwords are never stored here.
type Connection struct {
	Name     string            `toml:"name"`
	Driver   string            `toml:"driver"`
	Host     string            `toml:"host"`
	Port     int               `toml:"port"`
	User     string            `toml:"user"`
	Database string            `toml:"database"`
	Env      Env               `toml:"env"`
	ReadOnly *bool             `toml:"readonly"`
	SSH      string            `toml:"ssh"`
	Params   map[string]string `toml:"params"`
}

// IsReadOnly reports the effective read-only flag: explicit value, else true for prod.
func (c Connection) IsReadOnly() bool {
	if c.ReadOnly != nil {
		return *c.ReadOnly
	}
	return c.Env == EnvProd
}

// Settings is config.toml.
type Settings struct {
	PageSize       int `toml:"page_size"`
	MaxColumnWidth int `toml:"max_column_width"`
}

// DefaultSettings returns the settings used when config.toml omits a key.
func DefaultSettings() Settings {
	return Settings{PageSize: 200, MaxColumnWidth: 40}
}

// Config is everything loaded from the config directory.
type Config struct {
	Connections []Connection
	Settings    Settings
}

var defaultPorts = map[string]int{"mysql": 3306, "postgres": 5432}

type connectionsFile struct {
	Connections []Connection `toml:"connection"`
}

// Load reads connections.toml and config.toml from dir. Missing files are not an error.
func Load(dir string) (*Config, error) {
	cfg := &Config{Settings: DefaultSettings()}

	connPath := filepath.Join(dir, "connections.toml")
	var cf connectionsFile
	if err := decodeFile(connPath, &cf); err != nil {
		return nil, err
	}
	conns, err := normalize(cf.Connections)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", connPath, err)
	}
	cfg.Connections = conns

	settingsPath := filepath.Join(dir, "config.toml")
	if err := decodeFile(settingsPath, &cfg.Settings); err != nil {
		return nil, err
	}
	if cfg.Settings.PageSize <= 0 {
		return nil, fmt.Errorf("%s: page_size must be positive", settingsPath)
	}
	if cfg.Settings.MaxColumnWidth <= 0 {
		return nil, fmt.Errorf("%s: max_column_width must be positive", settingsPath)
	}
	return cfg, nil
}

func decodeFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var se *toml.StrictMissingError
		if errors.As(err, &se) {
			return fmt.Errorf("%s: %s", path, se.String())
		}
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return fmt.Errorf("%s:%d:%d: %s", path, row, col, de.Error())
		}
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func normalize(in []Connection) ([]Connection, error) {
	seen := make(map[string]bool, len(in))
	out := make([]Connection, 0, len(in))
	for i, c := range in {
		if c.Name == "" {
			return nil, fmt.Errorf("connection #%d: name is required", i+1)
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("connection %q: duplicate name", c.Name)
		}
		seen[c.Name] = true
		port, ok := defaultPorts[c.Driver]
		if !ok {
			return nil, fmt.Errorf("connection %q: unknown driver %q (want mysql or postgres)", c.Name, c.Driver)
		}
		if c.Port == 0 {
			c.Port = port
		}
		if c.Host == "" {
			c.Host = "localhost"
		}
		switch c.Env {
		case "":
			c.Env = EnvDev
		case EnvDev, EnvStaging, EnvProd:
		default:
			return nil, fmt.Errorf("connection %q: unknown env %q (want dev, staging or prod)", c.Name, c.Env)
		}
		out = append(out, c)
	}
	return out, nil
}

// Dir returns $XDG_CONFIG_HOME/sqlvim or ~/.config/sqlvim.
func Dir() (string, error) {
	return xdgDir("XDG_CONFIG_HOME", ".config")
}

// StateDir returns $XDG_STATE_HOME/sqlvim or ~/.local/state/sqlvim.
func StateDir() (string, error) {
	return xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
}

func xdgDir(env, fallback string) (string, error) {
	if base := os.Getenv(env); base != "" {
		return filepath.Join(base, "sqlvim"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, fallback, "sqlvim"), nil
}
