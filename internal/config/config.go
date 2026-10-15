// Package config loads sqlvim's TOML configuration files.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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
	PageSize       int    `toml:"page_size"`
	MaxColumnWidth int    `toml:"max_column_width"`
	MaxRows        int    `toml:"max_rows"`
	CompareMaxRows int    `toml:"compare_max_rows"`
	Editor         string `toml:"editor"`
	ExportDir      string `toml:"export_dir"`
	Icons          string `toml:"icons"` // IconsNerd or IconsPlain
}

// The icon sets of the tree: Nerd Font glyphs, or plain Unicode for
// terminals without a Nerd Font.
const (
	IconsNerd  = "nerd"
	IconsPlain = "plain"
)

// DefaultSettings returns the settings used when config.toml omits a key.
func DefaultSettings() Settings {
	return Settings{PageSize: 200, MaxColumnWidth: 40, MaxRows: 1000, CompareMaxRows: 100000, Icons: IconsNerd}
}

// EditorCommand is the command line <space>v runs: the editor setting, then
// $VISUAL, then $EDITOR, then nvim.
func (s Settings) EditorCommand(getenv func(string) string) []string {
	for _, v := range []string{s.Editor, getenv("VISUAL"), getenv("EDITOR")} {
		if f := strings.Fields(v); len(f) > 0 {
			return f
		}
	}
	return []string{"nvim"}
}

// ExportDirectory returns export_dir with a leading ~ expanded, defaulting to ~/Downloads.
func (s Settings) ExportDirectory(home string) string {
	switch d := s.ExportDir; {
	case d == "":
		return filepath.Join(home, "Downloads")
	case d == "~":
		return home
	case strings.HasPrefix(d, "~/"):
		return filepath.Join(home, d[2:])
	default:
		return d
	}
}

// Config is everything loaded from the config directory.
type Config struct {
	Connections []Connection
	Settings    Settings
	Theme       map[string]string // config.toml [theme]; ui/theme validates it
	Keymap      KeymapFile
}

// settingsFile is config.toml: the settings plus the [theme] table.
type settingsFile struct {
	Settings
	Theme map[string]string `toml:"theme"`
}

var defaultPorts = map[string]int{"mysql": 3306, "postgres": 5432}

type connectionsFile struct {
	Connections []Connection `toml:"connection"`
}

// Load reads connections.toml, config.toml and keymap.toml from dir. Missing files are not an error.
func Load(dir string) (*Config, error) { return LoadIn(dir, dir) }

// LoadIn reads config.toml and keymap.toml from root and connections.toml
// from connDir, a workspace's directory. Missing files are not an error.
func LoadIn(root, connDir string) (*Config, error) {
	cfg := &Config{Settings: DefaultSettings()}

	connPath := filepath.Join(connDir, "connections.toml")
	var cf connectionsFile
	if err := decodeFile(connPath, &cf); err != nil {
		return nil, err
	}
	conns, err := normalize(cf.Connections)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", connPath, err)
	}
	cfg.Connections = conns

	settingsPath := filepath.Join(root, "config.toml")
	sf := settingsFile{Settings: cfg.Settings}
	if err := decodeFile(settingsPath, &sf); err != nil {
		return nil, err
	}
	cfg.Settings, cfg.Theme = sf.Settings, sf.Theme
	if cfg.Settings.PageSize <= 0 {
		return nil, fmt.Errorf("%s: page_size must be positive", settingsPath)
	}
	if cfg.Settings.MaxColumnWidth <= 0 {
		return nil, fmt.Errorf("%s: max_column_width must be positive", settingsPath)
	}
	if cfg.Settings.MaxRows <= 0 {
		return nil, fmt.Errorf("%s: max_rows must be positive", settingsPath)
	}
	if cfg.Settings.CompareMaxRows <= 0 {
		return nil, fmt.Errorf("%s: compare_max_rows must be positive", settingsPath)
	}
	if i := cfg.Settings.Icons; i != IconsNerd && i != IconsPlain {
		return nil, fmt.Errorf("%s: icons must be %q or %q, got %q", settingsPath, IconsNerd, IconsPlain, i)
	}
	if err := decodeFile(filepath.Join(root, "keymap.toml"), &cfg.Keymap); err != nil {
		return nil, err
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

// Normalize validates one connection and fills in defaults (host, port, env).
func Normalize(c Connection) (Connection, error) {
	if c.Name == "" {
		return c, errors.New("connection: name is required")
	}
	port, ok := defaultPorts[c.Driver]
	if !ok {
		return c, fmt.Errorf("connection %q: unknown driver %q (want mysql or postgres)", c.Name, c.Driver)
	}
	if c.Port == 0 {
		c.Port = port
	}
	if c.Host == "" {
		c.Host = "localhost"
	}
	c.SSH = strings.TrimSpace(c.SSH)
	if strings.ContainsAny(c.SSH, " \t") {
		return c, fmt.Errorf("connection %q: ssh must be one Host alias from ~/.ssh/config, got %q", c.Name, c.SSH)
	}
	switch c.Env {
	case "":
		c.Env = EnvDev
	case EnvDev, EnvStaging, EnvProd:
	default:
		return c, fmt.Errorf("connection %q: unknown env %q (want dev, staging or prod)", c.Name, c.Env)
	}
	return c, nil
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
		c, err := Normalize(c)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// connBlock is what <space>n writes; empty optional keys are left out.
type connBlock struct {
	Name     string `toml:"name"`
	Driver   string `toml:"driver"`
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	User     string `toml:"user,omitempty"`
	Database string `toml:"database,omitempty"`
	SSH      string `toml:"ssh,omitempty"`
	Env      Env    `toml:"env"`
}

// AppendConnection validates c and appends it to dir/connections.toml as a new
// [[connection]] block, leaving existing content (and comments) untouched.
func AppendConnection(dir string, c Connection) (Connection, error) {
	c, err := Normalize(c)
	if err != nil {
		return c, err
	}
	cfg, err := Load(dir)
	if err != nil {
		return c, err
	}
	for _, e := range cfg.Connections {
		if e.Name == c.Name {
			return c, fmt.Errorf("connection %q already exists", c.Name)
		}
	}
	block, err := toml.Marshal(struct {
		Connection []connBlock `toml:"connection"`
	}{[]connBlock{{
		Name: c.Name, Driver: c.Driver, Host: c.Host, Port: c.Port,
		User: c.User, Database: c.Database, SSH: c.SSH, Env: c.Env,
	}}})
	if err != nil {
		return c, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return c, err
	}
	path := filepath.Join(dir, "connections.toml")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return c, err
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		block = append([]byte("\n"), block...)
	}
	if _, err := f.Write(block); err != nil {
		return c, err
	}
	return c, f.Close()
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
