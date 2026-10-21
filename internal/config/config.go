// Package config loads datavim's TOML configuration files.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	Masks       []MaskRule        // config.toml [[mask]]
	Keymap      KeymapFile
}

// settingsFile is config.toml: the settings plus the [theme] table.
type settingsFile struct {
	Settings
	Theme map[string]string `toml:"theme"`
	Mask  []MaskRule        `toml:"mask"`
}

var defaultPorts = map[string]int{"mysql": 3306, "postgres": 5432, "sqlserver": 1433, "oracle": 1521, "redis": 6379, "kafka": 9092}

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
	cfg.Settings, cfg.Theme, cfg.Masks = sf.Settings, sf.Theme, sf.Mask
	if err := checkMasks(cfg.Masks); err != nil {
		return nil, fmt.Errorf("%s: %w", settingsPath, err)
	}
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
		return c, fmt.Errorf("connection %q: unknown driver %q (want mysql, postgres, sqlserver, oracle, redis or kafka)", c.Name, c.Driver)
	}
	if c.Port == 0 {
		c.Port = port
	}
	if c.Host == "" {
		c.Host = "localhost"
	}
	if c.Driver == "redis" && c.Database != "" {
		if n, err := strconv.Atoi(c.Database); err != nil || n < 0 {
			return c, fmt.Errorf("connection %q: redis database must be a number (0, 1, …), got %q", c.Name, c.Database)
		}
	}
	if c.Driver == "kafka" {
		var err error
		if c, err = normalizeKafka(c); err != nil {
			return c, err
		}
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

func normalizeKafka(c Connection) (Connection, error) {
	switch mech := c.Params["sasl"]; mech {
	case "", "plain", "scram-sha-256", "scram-sha-512":
		if mech != "" && c.User == "" {
			return c, fmt.Errorf("connection %q: kafka sasl needs a user", c.Name)
		}
	case "oauth":
		for _, k := range []string{"oauth_token_url", "oauth_client_id"} {
			if c.Params[k] == "" {
				return c, fmt.Errorf("connection %q: kafka sasl oauth needs params.%s", c.Name, k)
			}
		}
	case "aws-msk-iam":
		// MSK's IAM listener only takes TLS.
		if _, ok := c.Params["tls"]; !ok {
			p := maps.Clone(c.Params)
			p["tls"] = "true"
			c.Params = p
		}
	default:
		return c, fmt.Errorf("connection %q: kafka sasl %q is not supported (want plain, scram-sha-256, scram-sha-512, oauth or aws-msk-iam)", c.Name, mech)
	}
	if _, err := KafkaBrokerMap(c); err != nil {
		return c, fmt.Errorf("connection %q: %w", c.Name, err)
	}
	if u := c.Params["schema_registry_url"]; u != "" && !httpURL(u) {
		return c, fmt.Errorf("connection %q: params.schema_registry_url must start with http:// or https://", c.Name)
	}
	if _, err := KafkaConnectClusters(c); err != nil {
		return c, fmt.Errorf("connection %q: %w", c.Name, err)
	}
	return c, nil
}

func httpURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// ConnectCluster is one Kafka Connect cluster of a kafka connection.
type ConnectCluster struct{ Name, URL string }

// KafkaConnectClusters parses params.connect_url: "name=url" items
// separated by commas, or one bare url named "connect". Names are unique;
// urls are http(s).
func KafkaConnectClusters(c Connection) ([]ConnectCluster, error) {
	v := strings.TrimSpace(c.Params["connect_url"])
	if v == "" {
		return nil, nil
	}
	errFormat := errors.New("params.connect_url: want name=http(s)://host:port, …")
	items := strings.Split(v, ",")
	var out []ConnectCluster
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		cl := ConnectCluster{Name: "connect", URL: item}
		// an = before :// names the cluster; one after it is the URL's query
		if eq, scheme := strings.Index(item, "="), strings.Index(item, "://"); eq >= 0 && (scheme < 0 || eq < scheme) {
			cl.Name, cl.URL = strings.TrimSpace(item[:eq]), strings.TrimSpace(item[eq+1:])
		} else if len(items) > 1 {
			return nil, errFormat // only a lone URL goes without a name
		}
		if cl.Name == "" || !httpURL(cl.URL) || len(cl.URL) <= len("https://") {
			return nil, errFormat
		}
		if slices.ContainsFunc(out, func(o ConnectCluster) bool { return o.Name == cl.Name }) {
			return nil, fmt.Errorf("params.connect_url: duplicate connect cluster %q", cl.Name)
		}
		out = append(out, cl)
	}
	return out, nil
}

// KafkaBrokerMap parses params.broker_map, "from=to" pairs of host:port
// separated by commas: the address to dial for each advertised one.
func KafkaBrokerMap(c Connection) (map[string]string, error) {
	v := strings.TrimSpace(c.Params["broker_map"])
	if v == "" {
		return nil, nil
	}
	errFormat := errors.New("params.broker_map: want host:port=host:port, …")
	m := make(map[string]string)
	for pair := range strings.SplitSeq(v, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		from, to, ok := strings.Cut(pair, "=")
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if !ok {
			return nil, errFormat
		}
		for _, a := range []string{from, to} {
			if _, _, err := net.SplitHostPort(a); err != nil {
				return nil, errFormat
			}
		}
		m[from] = to
	}
	return m, nil
}

// RedisDB is a redis connection's default db number; Normalize has
// checked it, so an empty or bad value is 0.
func RedisDB(c Connection) int {
	n, _ := strconv.Atoi(c.Database)
	return max(n, 0)
}

// KafkaBrokers is a kafka connection's bootstrap broker list: Host split
// at commas, with Port added to the entries that have none.
func KafkaBrokers(c Connection) []string {
	var out []string
	for b := range strings.SplitSeq(c.Host, ",") {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(b); err != nil {
			b = net.JoinHostPort(strings.Trim(b, "[]"), strconv.Itoa(c.Port))
		}
		out = append(out, b)
	}
	return out
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
	Name     string            `toml:"name"`
	Driver   string            `toml:"driver"`
	Host     string            `toml:"host"`
	Port     int               `toml:"port"`
	User     string            `toml:"user,omitempty"`
	Database string            `toml:"database,omitempty"`
	SSH      string            `toml:"ssh,omitempty"`
	Env      Env               `toml:"env"`
	ReadOnly *bool             `toml:"readonly,omitempty"`
	Params   map[string]string `toml:"params,omitempty,inline"`
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
		User: c.User, Database: c.Database, SSH: c.SSH, Env: c.Env, ReadOnly: c.ReadOnly, Params: c.Params,
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

// Dir returns $XDG_CONFIG_HOME/datavim or ~/.config/datavim.
func Dir() (string, error) {
	return xdgDir("XDG_CONFIG_HOME", ".config")
}

// StateDir returns $XDG_STATE_HOME/datavim or ~/.local/state/datavim.
func StateDir() (string, error) {
	return xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
}

// legacyName is the name the project had before datavim; its directories
// are moved over the first time they are looked up.
const legacyName = "sqlvim"

func xdgDir(env, fallback string) (string, error) {
	base := os.Getenv(env)
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, fallback)
	}
	dir := filepath.Join(base, "datavim")
	migrateLegacyDir(filepath.Join(base, legacyName), dir)
	return dir, nil
}

// migrateLegacyDir renames old to dir when only old exists. A failure leaves
// both alone: datavim then starts with a fresh directory.
func migrateLegacyDir(old, dir string) {
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	if info, err := os.Stat(old); err != nil || !info.IsDir() {
		return
	}
	_ = os.Rename(old, dir)
}
