// Command sqlvim is a terminal SQL client with vim keys.
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db/mysql"
	"github.com/shepherd-yun/sqlvim/internal/db/oracle"
	"github.com/shepherd-yun/sqlvim/internal/db/postgres"
	"github.com/shepherd-yun/sqlvim/internal/db/sqlserver"
	"github.com/shepherd-yun/sqlvim/internal/db/tunnel"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/rdb"
	"github.com/shepherd-yun/sqlvim/internal/secret"
	"github.com/shepherd-yun/sqlvim/internal/session"
	"github.com/shepherd-yun/sqlvim/internal/ui"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sqlvim:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("sqlvim", flag.ContinueOnError)
	showVersion := fs.Bool("version", false, "print version and exit")
	debug := fs.Bool("debug", false, "write debug-level logs")
	configDir := fs.String("config", "", "config directory (default $XDG_CONFIG_HOME/sqlvim or ~/.config/sqlvim)")
	var wsName string
	fs.StringVar(&wsName, "workspace", "", "workspace to open (default $SQLVIM_WORKSPACE, else default)")
	fs.StringVar(&wsName, "w", "", "shorthand for --workspace")
	create := fs.Bool("create", false, "create the workspace if it does not exist")
	listWorkspaces := fs.Bool("list-workspaces", false, "print the workspaces and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		_, err := fmt.Fprintln(stdout, "sqlvim", version)
		return err
	}

	dir := *configDir
	if dir == "" {
		d, err := config.Dir()
		if err != nil {
			return err
		}
		dir = d
	}
	if *listWorkspaces {
		names, err := config.ListWorkspaces(dir)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, strings.Join(names, "\n"))
		return err
	}
	stateRoot, err := config.StateDir()
	if err != nil {
		return err
	}
	ws, err := selectWorkspace(dir, stateRoot, workspaceOptions{
		name: wsName, env: os.Getenv("SQLVIM_WORKSPACE"), create: *create,
	})
	if err != nil {
		return err
	}
	cfg, err := config.LoadIn(dir, ws.ConfigDir)
	if err != nil {
		return err
	}
	keys, err := buildKeymap(dir, cfg.Keymap)
	if err != nil {
		return err
	}
	if err := theme.Apply(cfg.Theme); err != nil {
		return fmt.Errorf("%s: %w", filepath.Join(dir, "config.toml"), err)
	}

	logPath, closeLog, err := setupLogging(*debug)
	if err != nil {
		return err
	}
	defer closeLog()

	if err := os.MkdirAll(ws.StateDir, 0o700); err != nil {
		return err
	}
	st, err := openStore(ws.StateDir)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	tunnels := tunnel.NewPool(tunnel.Options{
		ConfigFile:  filepath.Join(home, ".ssh", "config"),
		Env:         tunnel.Env{Home: home, User: localUser()},
		AgentSocket: os.Getenv("SSH_AUTH_SOCK"),
	})
	defer tunnels.Close()

	mgr := newManager(cfg, ws, tunnels)
	defer mgr.CloseAll()

	slog.Info("starting", "version", version, "workspace", ws.Name, "connections", len(cfg.Connections))
	model := ui.New(mgr, st, cfg.Settings)
	model.SetKeymap(keys)
	model.SetWorkspace(ws.Name)
	if _, err := tea.NewProgram(model).Run(); err != nil {
		slog.Error("program exited", "err", err)
		return fmt.Errorf("%w (log: %s)", err, logPath)
	}
	return nil
}

func setupLogging(debug bool) (path string, closeFn func(), err error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	path = filepath.Join(dir, "sqlvim.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return "", nil, err
	}
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level})))
	return path, func() { _ = f.Close() }, nil
}

// openStore opens state.db, falling back to an in-memory store so a broken
// state file never stops sqlvim from starting.
func openStore(stateDir string) (*history.Store, error) {
	st, err := history.Open(filepath.Join(stateDir, "state.db"))
	if err == nil {
		return st, nil
	}
	slog.Warn("state database unavailable; history and drafts will not be kept", "err", err)
	return history.Open(":memory:")
}

// newManager builds the session manager for workspace ws; connections added,
// edited or deleted in the UI are saved to its connections.toml, passwords
// go to its keychain accounts, and ssh connections use tunnels.
func newManager(cfg *config.Config, ws config.Workspace, tunnels session.Tunneler) *session.Manager {
	dir := ws.ConfigDir
	mgr := session.NewManager(cfg.Connections, secret.Keyring{Prefix: keychainPrefix(ws)},
		mysql.Driver{}, postgres.Driver{}, sqlserver.Driver{}, oracle.Driver{})
	mgr.Persist = func(c config.Connection) error {
		_, err := config.AppendConnection(dir, c)
		return err
	}
	mgr.PersistUpdate = func(name string, c config.Connection) error {
		_, err := config.UpdateConnection(dir, name, c)
		return err
	}
	mgr.PersistRemove = func(name string) error { return config.RemoveConnection(dir, name) }
	mgr.Tunnels = tunnels
	mgr.RedisOpen = rdb.Open
	mgr.RedisSessionOpen = rdb.OpenSession
	mgr.KafkaOpen = kafka.Open
	mgr.KafkaReaderOpen = kafka.OpenReader
	return mgr
}

// localUser is the default SSH user, as for ssh(1).
func localUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

// buildKeymap applies keymap.toml to the built-in keymap; errors name the file.
func buildKeymap(dir string, f config.KeymapFile) (*keymap.Keymap, error) {
	overrides := map[keymap.Context]map[string]string{}
	for ctx, m := range f.Overrides() {
		overrides[keymap.Context(ctx)] = m
	}
	k, err := keymap.Custom(f.Leader, overrides)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, "keymap.toml"), err)
	}
	return k, nil
}
