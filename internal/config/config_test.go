package config

import (
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
	c := Connection{Name: "prod", Driver: "postgres", Host: "10.0.1.6", Port: 5432, User: "app", SSH: "bastion", Env: EnvProd}
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

func TestUpdateConnectionErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "connections.toml", editableConns)
	if _, err := UpdateConnection(dir, "nope", Connection{Name: "nope", Driver: "mysql"}); err == nil {
		t.Error("unknown connection")
	}
	if _, err := UpdateConnection(dir, "local", Connection{Name: "last", Driver: "mysql"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("rename onto another connection: %v", err)
	}
	if _, err := UpdateConnection(dir, "local", Connection{Name: "local", Driver: "oracle"}); err == nil {
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
