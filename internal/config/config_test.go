package config

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
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
		{"unknown driver", "[[connection]]\nname=\"a\"\ndriver=\"sqlite\"\n", "unknown driver"},
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

func TestNewSettingsDefaultsAndValidation(t *testing.T) {
	if got := DefaultSettings().MaxRows; got != 1000 {
		t.Fatalf("MaxRows default = %d", got)
	}
	dir := t.TempDir()
	writeFile(t, dir, "config.toml", "max_rows = 50\neditor = \"code -w\"\nexport_dir = \"~/out\"\n")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Settings
	if s.MaxRows != 50 || s.Editor != "code -w" || s.ExportDir != "~/out" {
		t.Fatalf("settings = %+v", s)
	}
	writeFile(t, dir, "config.toml", "max_rows = 0\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "max_rows") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditorCommand(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if got := (Settings{}).EditorCommand(getenv); !slices.Equal(got, []string{"nvim"}) {
		t.Fatalf("fallback = %v", got)
	}
	env["EDITOR"] = "vim"
	if got := (Settings{}).EditorCommand(getenv); !slices.Equal(got, []string{"vim"}) {
		t.Fatalf("EDITOR = %v", got)
	}
	env["VISUAL"] = "hx"
	if got := (Settings{}).EditorCommand(getenv); !slices.Equal(got, []string{"hx"}) {
		t.Fatalf("VISUAL wins over EDITOR: %v", got)
	}
	if got := (Settings{Editor: "code -w"}).EditorCommand(getenv); !slices.Equal(got, []string{"code", "-w"}) {
		t.Fatalf("setting wins: %v", got)
	}
}

func TestExportDirectory(t *testing.T) {
	cases := map[string]string{
		"":         "/home/u/Downloads",
		"~":        "/home/u",
		"~/out":    "/home/u/out",
		"/tmp/x":   "/tmp/x",
		"relative": "relative",
	}
	for in, want := range cases {
		if got := (Settings{ExportDir: in}).ExportDirectory("/home/u"); got != want {
			t.Errorf("ExportDirectory(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalize(t *testing.T) {
	c, err := Normalize(Connection{Name: "x", Driver: "postgres"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 5432 || c.Host != "localhost" || c.Env != EnvDev {
		t.Fatalf("defaults not applied: %+v", c)
	}
	for driver, port := range map[string]int{"sqlserver": 1433, "oracle": 1521} {
		c, err := Normalize(Connection{Name: "x", Driver: driver})
		if err != nil || c.Port != port {
			t.Errorf("%s: port %d, err %v", driver, c.Port, err)
		}
	}
	for _, bad := range []Connection{
		{Driver: "mysql"},
		{Name: "x", Driver: "sqlite"},
		{Name: "x", Driver: "mysql", Env: "qa"},
	} {
		if _, err := Normalize(bad); err == nil {
			t.Errorf("Normalize(%+v) succeeded", bad)
		}
	}
}

func TestAppendConnectionKeepsExistingContent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "connections.toml", "# my servers\n[[connection]]\nname = \"a\"\ndriver = \"mysql\"\n")
	got, err := AppendConnection(dir, Connection{Name: "b \"quoted\"", Driver: "postgres", User: "app", Database: "shop", Env: EnvProd})
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != 5432 {
		t.Fatalf("returned connection is normalized: %+v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "connections.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "# my servers\n") {
		t.Fatalf("existing content rewritten:\n%s", data)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("file no longer loads: %v\n%s", err, data)
	}
	if len(cfg.Connections) != 2 || cfg.Connections[1].Name != `b "quoted"` || cfg.Connections[1].Env != EnvProd {
		t.Fatalf("connections = %+v", cfg.Connections)
	}
	if _, err := AppendConnection(dir, Connection{Name: "a", Driver: "mysql"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate err = %v", err)
	}
}

func TestAppendConnectionWritesReadOnly(t *testing.T) {
	dir := t.TempDir()
	off := false
	if _, err := AppendConnection(dir, Connection{Name: "prod", Driver: "postgres", Env: EnvProd, ReadOnly: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendConnection(dir, Connection{Name: "auto", Driver: "postgres", Env: EnvProd}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Connections[0].IsReadOnly() || cfg.Connections[1].ReadOnly != nil || !cfg.Connections[1].IsReadOnly() {
		t.Fatalf("connections = %+v", cfg.Connections)
	}
}

func TestAppendConnectionCreatesPrivateFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "sqlvim")
	if _, err := AppendConnection(dir, Connection{Name: "a", Driver: "mysql"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "connections.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o", perm)
	}
}

func TestNormalizeSSH(t *testing.T) {
	c, err := Normalize(Connection{Name: "a", Driver: "mysql", SSH: " bastion "})
	if err != nil || c.SSH != "bastion" {
		t.Fatalf("c = %+v, err = %v", c, err)
	}
	_, err = Normalize(Connection{Name: "a", Driver: "mysql", SSH: "ops@bastion -p 2222"})
	if err == nil || !strings.Contains(err.Error(), "ssh") {
		t.Fatalf("err = %v", err)
	}
}

func TestAppendConnectionWritesSSH(t *testing.T) {
	dir := t.TempDir()
	if _, err := AppendConnection(dir, Connection{Name: "remote", Driver: "postgres", SSH: "bastion"}); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendConnection(dir, Connection{Name: "local", Driver: "mysql"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Connections[0].SSH != "bastion" || cfg.Connections[1].SSH != "" {
		t.Fatalf("connections = %+v", cfg.Connections)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "connections.toml"))
	if n := strings.Count(string(data), "ssh"); n != 1 {
		t.Fatalf("an empty ssh key is left out; file:\n%s", data)
	}
}

const editableConns = `# my connections
[[connection]]
name = "local"   # the laptop
driver = "mysql"
user = "root"

# production, careful
[[connection]]
name     = 'prod-pg'
driver   = 'postgres'
host     = '10.0.1.5'
user     = 'app'
database = 'app'
env      = 'prod'
readonly = true
params   = { sslmode = "require" }

[[connection]]
name = "last"
driver = "postgres"
`

func TestUpdateConnectionTouchesOnlyItsBlock(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "connections.toml", editableConns)
	on := true
	c := Connection{
		Name: "prod", Driver: "postgres", Host: "10.0.1.6", Port: 5432, User: "app", SSH: "bastion", Env: EnvProd, ReadOnly: &on,
		Params: map[string]string{"sslmode": "require"},
	}
	if _, err := UpdateConnection(dir, "prod-pg", c); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "connections.toml"))
	got := string(data)
	for _, keep := range []string{
		"# my connections", `name = "local"   # the laptop`, "# production, careful",
		"readonly = true", `params   = { sslmode = "require" }`, "user     = 'app'", `name = "last"`,
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("lost %q:\n%s", keep, got)
		}
	}
	if strings.Contains(got, "database") {
		t.Errorf("an emptied optional key is removed:\n%s", got)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Connections[1]
	if p.Name != "prod" || p.Host != "10.0.1.6" || p.SSH != "bastion" || !p.IsReadOnly() || p.Params["sslmode"] != "require" {
		t.Fatalf("prod = %+v", p)
	}
	if cfg.Connections[0].Name != "local" || cfg.Connections[2].Name != "last" {
		t.Fatalf("other connections changed: %+v", cfg.Connections)
	}
}

func TestUpdateConnectionReadOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "connections.toml", editableConns)
	off := false
	prod := Connection{Name: "prod-pg", Driver: "postgres", Host: "10.0.1.5", User: "app", Database: "app", Env: EnvProd, Params: map[string]string{"sslmode": "require"}}
	local := Connection{Name: "local", Driver: "mysql", User: "root", ReadOnly: &off}
	read := func() string {
		data, _ := os.ReadFile(filepath.Join(dir, "connections.toml"))
		return string(data)
	}
	if _, err := UpdateConnection(dir, "prod-pg", prod); err != nil {
		t.Fatal(err)
	}
	if got := read(); strings.Contains(got, "readonly") || !strings.Contains(got, "sslmode") {
		t.Fatalf("auto removes the readonly key:\n%s", got)
	}
	if _, err := UpdateConnection(dir, "local", local); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Connections[1].IsReadOnly() || cfg.Connections[1].ReadOnly != nil {
		t.Fatalf("prod-pg = %+v: readonly follows env", cfg.Connections[1])
	}
	if l := cfg.Connections[0]; l.ReadOnly == nil || *l.ReadOnly {
		t.Fatalf("local = %+v: readonly = false is written", l)
	}
	if got := read(); !strings.Contains(got, `name = "local"   # the laptop`) {
		t.Fatalf("other lines are kept:\n%s", got)
	}
}

func TestUpdateConnectionErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "connections.toml", editableConns)
	if _, err := UpdateConnection(dir, "nope", Connection{Name: "nope", Driver: "mysql"}); err == nil {
		t.Error("unknown connection")
	}
	if _, err := UpdateConnection(dir, "local", Connection{Name: "last", Driver: "mysql"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("rename onto another connection: %v", err)
	}
	if _, err := UpdateConnection(dir, "local", Connection{Name: "local", Driver: "sqlite"}); err == nil {
		t.Error("invalid connection")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "connections.toml"))
	if string(data) != editableConns {
		t.Fatalf("failed updates leave the file alone:\n%s", data)
	}
}

func TestRemoveConnection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "connections.toml", editableConns)
	if err := RemoveConnection(dir, "local"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "connections.toml"))
	got := string(data)
	if strings.Contains(got, "laptop") || !strings.Contains(got, "# my connections") || !strings.Contains(got, "# production, careful") {
		t.Fatalf("only the block goes; comments above other blocks stay:\n%s", got)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Connections) != 2 || cfg.Connections[0].Name != "prod-pg" {
		t.Fatalf("connections = %+v", cfg.Connections)
	}
	if err := RemoveConnection(dir, "local"); err == nil {
		t.Fatal("removing it twice fails")
	}
	if info, _ := os.Stat(filepath.Join(dir, "connections.toml")); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
}

func TestCompareMaxRows(t *testing.T) {
	if got := DefaultSettings().CompareMaxRows; got != 100000 {
		t.Fatalf("CompareMaxRows default = %d", got)
	}
	dir := t.TempDir()
	writeFile(t, dir, "config.toml", "compare_max_rows = 5000\n")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.CompareMaxRows != 5000 {
		t.Fatalf("settings = %+v", cfg.Settings)
	}
	writeFile(t, dir, "config.toml", "compare_max_rows = 0\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "compare_max_rows") {
		t.Fatalf("err = %v", err)
	}
}

func TestIcons(t *testing.T) {
	if got := DefaultSettings().Icons; got != IconsNerd {
		t.Fatalf("Icons default = %q", got)
	}
	dir := t.TempDir()
	writeFile(t, dir, "config.toml", `icons = "plain"`+"\n")
	cfg, err := Load(dir)
	if err != nil || cfg.Settings.Icons != IconsPlain {
		t.Fatalf("settings = %+v, err %v", cfg, err)
	}
	writeFile(t, dir, "config.toml", `icons = "emoji"`+"\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), `icons must be "nerd" or "plain"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadInReadsConnectionsFromWorkspace(t *testing.T) {
	root := t.TempDir()
	connDir := filepath.Join(root, "workspaces", "personal")
	if err := os.MkdirAll(connDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "connections.toml", "[[connection]]\nname = \"work-db\"\ndriver = \"mysql\"\n")
	writeFile(t, connDir, "connections.toml", "[[connection]]\nname = \"home-db\"\ndriver = \"postgres\"\n")
	writeFile(t, root, "config.toml", "page_size = 7\n")
	writeFile(t, root, "keymap.toml", "leader = \",\"\n")

	cfg, err := LoadIn(root, connDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Connections) != 1 || cfg.Connections[0].Name != "home-db" {
		t.Fatalf("connections = %+v, want only home-db", cfg.Connections)
	}
	if cfg.Settings.PageSize != 7 || cfg.Keymap.Leader != "," {
		t.Fatalf("settings/keymap come from the root: %+v, %+v", cfg.Settings, cfg.Keymap)
	}
}

func TestNormalizeRedis(t *testing.T) {
	c, err := Normalize(Connection{Name: "r", Driver: "redis"})
	if err != nil || c.Port != 6379 || c.Host != "localhost" || RedisDB(c) != 0 {
		t.Fatalf("c = %+v, err = %v", c, err)
	}
	c, err = Normalize(Connection{Name: "r", Driver: "redis", Database: "3"})
	if err != nil || RedisDB(c) != 3 {
		t.Fatalf("c = %+v, err = %v", c, err)
	}
	for _, bad := range []string{"-1", "abc"} {
		_, err := Normalize(Connection{Name: "r", Driver: "redis", Database: bad})
		if err == nil || !strings.Contains(err.Error(), "redis database must be a number") {
			t.Errorf("Database %q: err = %v", bad, err)
		}
	}
	if _, err := Normalize(Connection{Name: "x", Driver: "sqlite"}); err == nil || !strings.Contains(err.Error(), "redis or kafka") {
		t.Errorf("unknown driver err = %v", err)
	}
}

func TestNormalizeKafka(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      Connection
		brokers []string
		err     string
	}{
		{"defaults", Connection{Name: "k", Driver: "kafka"}, []string{"localhost:9092"}, ""},
		{"broker list", Connection{Name: "k", Driver: "kafka", Host: "b1, b2:9093,, "}, []string{"b1:9092", "b2:9093"}, ""},
		{"port applies to bare hosts", Connection{Name: "k", Driver: "kafka", Host: "b1,b2", Port: 19092}, []string{"b1:19092", "b2:19092"}, ""},
		{"ipv6", Connection{Name: "k", Driver: "kafka", Host: "[::1]:9094,[::2]"}, []string{"[::1]:9094", "[::2]:9092"}, ""},
		{"scram", Connection{Name: "k", Driver: "kafka", User: "app", Params: map[string]string{"sasl": "scram-sha-512"}}, []string{"localhost:9092"}, ""},
		{
			"gssapi not supported",
			Connection{Name: "k", Driver: "kafka", User: "app", Params: map[string]string{"sasl": "gssapi"}},
			nil,
			`connection "k": kafka sasl "gssapi" is not supported (want plain, scram-sha-256, scram-sha-512, oauth or aws-msk-iam)`,
		},
		{
			"sasl needs user",
			Connection{Name: "k", Driver: "kafka", Params: map[string]string{"sasl": "plain"}},
			nil,
			`connection "k": kafka sasl needs a user`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Normalize(tc.in)
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Port == 0 {
				t.Fatalf("port not defaulted: %+v", c)
			}
			if got := KafkaBrokers(c); !slices.Equal(got, tc.brokers) {
				t.Fatalf("brokers = %q, want %q", got, tc.brokers)
			}
		})
	}
	if c, _ := Normalize(Connection{Name: "k", Driver: "kafka"}); c.Port != 9092 {
		t.Fatalf("port = %d", c.Port)
	}
}

func TestNormalizeKafkaK2(t *testing.T) {
	oauth := func(extra map[string]string) map[string]string {
		p := map[string]string{"sasl": "oauth", "oauth_token_url": "https://idp/token", "oauth_client_id": "cid"}
		maps.Copy(p, extra)
		return p
	}
	for _, tc := range []struct {
		name    string
		params  map[string]string
		wantTLS string // "" means params.tls left unset
		err     string
	}{
		{"oauth needs no user", oauth(nil), "", ""},
		{"oauth needs a token url", map[string]string{"sasl": "oauth", "oauth_client_id": "cid"}, "", `connection "k": kafka sasl oauth needs params.oauth_token_url`},
		{"oauth needs a client id", map[string]string{"sasl": "oauth", "oauth_token_url": "https://idp/token"}, "", `connection "k": kafka sasl oauth needs params.oauth_client_id`},
		{"msk iam needs no user and turns tls on", map[string]string{"sasl": "aws-msk-iam"}, "true", ""},
		{"msk iam keeps tls false", map[string]string{"sasl": "aws-msk-iam", "tls": "false"}, "false", ""},
		{"broker map", map[string]string{"broker_map": "b1:9092=127.0.0.1:19092"}, "", ""},
		{"bad broker map", map[string]string{"broker_map": "b1:9092"}, "", `connection "k": params.broker_map: want host:port=host:port, …`},
		{"registry url", map[string]string{"schema_registry_url": "https://sr:8081"}, "", ""},
		{"bad registry url", map[string]string{"schema_registry_url": "sr:8081"}, "", `connection "k": params.schema_registry_url must start with http:// or https://`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Normalize(Connection{Name: "k", Driver: "kafka", Params: tc.params})
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Params["tls"]; got != tc.wantTLS {
				t.Fatalf("tls = %q, want %q", got, tc.wantTLS)
			}
		})
	}
}

func TestKafkaBrokerMap(t *testing.T) {
	m, err := KafkaBrokerMap(Connection{Params: map[string]string{"broker_map": "b1:9092=127.0.0.1:19092, b2:9092 = 127.0.0.1:19093"}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"b1:9092": "127.0.0.1:19092", "b2:9092": "127.0.0.1:19093"}
	if !maps.Equal(m, want) {
		t.Fatalf("map = %v, want %v", m, want)
	}
	if _, err := KafkaBrokerMap(Connection{Params: map[string]string{"broker_map": "b1:9092"}}); err == nil {
		t.Fatal("want an error for a pair without =")
	}
	if m, err := KafkaBrokerMap(Connection{}); m != nil || err != nil {
		t.Fatalf("empty = %v, %v", m, err)
	}
}

func TestAppendConnectionWritesParams(t *testing.T) {
	dir := t.TempDir()
	p := map[string]string{"sasl": "scram-sha-512", "tls": "true", "odd key": `a"b`}
	if _, err := AppendConnection(dir, Connection{Name: "k", Driver: "kafka", User: "app", Params: p}); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendConnection(dir, Connection{Name: "m", Driver: "mysql"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(cfg.Connections[0].Params, p) || cfg.Connections[1].Params != nil {
		t.Fatalf("connections = %+v", cfg.Connections)
	}
}

func TestUpdateConnectionWritesParams(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "connections.toml", editableConns)
	prod := Connection{
		Name: "prod-pg", Driver: "postgres", Host: "10.0.1.5", User: "app", Database: "app", Env: EnvProd,
		ReadOnly: new(true), Params: map[string]string{"sslmode": "require", "x": "1"},
	}
	if _, err := UpdateConnection(dir, "prod-pg", prod); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(cfg.Connections[1].Params, prod.Params) {
		t.Fatalf("params = %v", cfg.Connections[1].Params)
	}
	prod.Params = nil
	if _, err := UpdateConnection(dir, "prod-pg", prod); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "connections.toml"))
	if strings.Contains(string(data), "params") {
		t.Fatalf("emptied params are removed:\n%s", data)
	}
	// local has no params: adding some appends a line
	local := Connection{Name: "local", Driver: "mysql", User: "root", Params: map[string]string{"tls": "true"}}
	if _, err := UpdateConnection(dir, "local", local); err != nil {
		t.Fatal(err)
	}
	if cfg, err = Load(dir); err != nil || cfg.Connections[0].Params["tls"] != "true" {
		t.Fatalf("local = %+v, err %v", cfg.Connections, err)
	}
}

func TestUpdateConnectionParamsSubTable(t *testing.T) {
	const conns = `[[connection]]
name = "a"
driver = "postgres"
host = "h1"

# TLS settings
[connection.params]
sslmode = "require"

[[connection]]
name = "b"
driver = "mysql"
host = "h2"
params.tls = "true"
`
	for _, tc := range []struct {
		name   string
		edit   Connection
		params map[string]string
	}{
		{"a", Connection{Name: "a", Driver: "postgres", Host: "h9", Params: map[string]string{"sslmode": "require"}}, map[string]string{"sslmode": "require"}},
		{"a", Connection{Name: "a", Driver: "postgres", Host: "h1", User: "u", Params: map[string]string{"sslmode": "disable"}}, map[string]string{"sslmode": "disable"}},
		{"a", Connection{Name: "a", Driver: "postgres", Host: "h1"}, nil},
		{"b", Connection{Name: "b", Driver: "mysql", Host: "h9", Params: map[string]string{"tls": "true"}}, map[string]string{"tls": "true"}},
		{"b", Connection{Name: "b", Driver: "mysql", Host: "h2", Params: map[string]string{"tls": "false"}}, map[string]string{"tls": "false"}},
	} {
		dir := t.TempDir()
		writeFile(t, dir, "connections.toml", conns)
		want, err := UpdateConnection(dir, tc.name, tc.edit)
		if err != nil {
			t.Fatalf("%s %+v: %v", tc.name, tc.edit, err)
		}
		cfg, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Connections) != 2 {
			t.Fatalf("%d connections", len(cfg.Connections))
		}
		for _, c := range cfg.Connections {
			if c.Name != tc.name {
				continue
			}
			if c.Host != want.Host || c.User != want.User || !maps.Equal(c.Params, tc.params) {
				data, _ := os.ReadFile(filepath.Join(dir, "connections.toml"))
				t.Fatalf("%s = %+v\n%s", tc.name, c, data)
			}
		}
	}
}

func TestRemoveConnectionWithSubTable(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "connections.toml", `[[connection]]
name = "a"
driver = "postgres"
host = "h1"

[connection.params]
sslmode = "require"

[[connection]]
name = "b"
driver = "mysql"
host = "h2"
`)
	if err := RemoveConnection(dir, "a"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil || len(cfg.Connections) != 1 || len(cfg.Connections[0].Params) != 0 {
		t.Fatalf("%+v, err %v", cfg.Connections, err)
	}
}

func TestKafkaConnectClusters(t *testing.T) {
	conn := func(v string) Connection {
		return Connection{Name: "k", Driver: "kafka", Params: map[string]string{"connect_url": v}}
	}
	for _, tc := range []struct {
		in   string
		want []ConnectCluster
		err  string
	}{
		{"", nil, ""},
		{"http://c1:8083", []ConnectCluster{{"connect", "http://c1:8083"}}, ""},
		{"main=http://c1:8083, cdc = https://u:p@c2:8083/", []ConnectCluster{{"main", "http://c1:8083"}, {"cdc", "https://u:p@c2:8083/"}}, ""},
		{"http://c1:8083/?a=b", []ConnectCluster{{"connect", "http://c1:8083/?a=b"}}, ""},
		{"main=http://c1:8083?x=1", []ConnectCluster{{"main", "http://c1:8083?x=1"}}, ""},
		{"main=http://c1:8083, http://c2:8083", nil, "want name=http(s)://host:port"},
		{"a=http://c1:8083, a=http://c2:8083", nil, `duplicate connect cluster "a"`},
		{"ftp://c1", nil, "want name=http(s)://host:port"},
		{"main=", nil, "want name=http(s)://host:port"},
	} {
		got, err := KafkaConnectClusters(conn(tc.in))
		switch {
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%q: err = %v, want %q", tc.in, err, tc.err)
		case tc.err == "" && err != nil:
			t.Errorf("%q: %v", tc.in, err)
		case tc.err == "" && !slices.Equal(got, tc.want):
			t.Errorf("%q = %v, want %v", tc.in, got, tc.want)
		}
	}
	if _, err := Normalize(conn("ftp://c1")); err == nil || !strings.Contains(err.Error(), `connection "k": params.connect_url`) {
		t.Fatalf("Normalize: %v", err)
	}
}
