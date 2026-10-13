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

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db/mysql"
	"github.com/shepherd-yun/sqlvim/internal/db/postgres"
	"github.com/shepherd-yun/sqlvim/internal/db/tunnel"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/secret"
	"github.com/shepherd-yun/sqlvim/internal/session"
	"github.com/shepherd-yun/sqlvim/internal/ui"
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
	cfg, err := config.Load(dir)
	if err != nil {
		return err
	}

	logPath, closeLog, err := setupLogging(*debug)
	if err != nil {
		return err
	}
	defer closeLog()

	st, err := openStore(filepath.Dir(logPath))
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

	mgr := newManager(cfg, dir, tunnels)
	defer mgr.CloseAll()

	slog.Info("starting", "version", version, "connections", len(cfg.Connections))
	if _, err := tea.NewProgram(ui.New(mgr, st, cfg.Settings)).Run(); err != nil {
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

// newManager builds the session manager; connections added in the UI are
// appended to dir/connections.toml, and ssh connections use tunnels.
func newManager(cfg *config.Config, dir string, tunnels session.Tunneler) *session.Manager {
	mgr := session.NewManager(cfg.Connections, secret.Keyring{}, mysql.Driver{}, postgres.Driver{})
	mgr.Persist = func(c config.Connection) error {
		_, err := config.AppendConnection(dir, c)
		return err
	}
	mgr.Tunnels = tunnels
	return mgr
}

// localUser is the default SSH user, as for ssh(1).
func localUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}
