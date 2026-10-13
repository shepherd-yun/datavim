# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.4.0] - 2026-10-14

### Added
- `<Space><Space>` searches tables, views, columns, routines, schemas and databases of the
  connection in context (`@` for every connection); `Enter` opens data, `Ctrl-s` the structure.
- `<Space>e` lists recently opened tables, structures and consoles, kept across restarts.
- Console completion for tables, columns, aliases and keywords, with quoting where needed.
- Syntax highlighting in the console editor and in the structure view's DDL.
- The console cursor jumps to the position of a syntax error reported by the server.
- Catalogs load in the background; `<Space>r` and DDL in a console reload them.
- Edit (`e`) and delete (`dd`) connections from the tree. Only the connection's own block
  in `connections.toml` is rewritten, keeping comments, `params` and `readonly`; a saved
  password follows a rename and is removed with the connection.
- Pasting (Cmd+V / bracketed paste) into the connection form, password and passphrase
  prompts, confirmations, the history filter and the `:` command line.

### Fixed
- `:set noreadonly` / `:set readonly` no longer freeze the UI on PostgreSQL while a console
  holds a session.

## [0.3.0] - 2026-10-13

### Added
- SSH tunnels from `~/.ssh/config` (`ssh = "<Host>"`): ssh-agent and key files, passphrase
  prompts, ProxyJump chains, known_hosts checks with a fingerprint prompt for new hosts,
  keepalive and automatic reconnect. No local port is opened.
- Production connections: red tab frame, `[PROD]` status, confirmation before writes and DDL.
- `:set noreadonly` / `:set readonly` for the current session.
- Manual commit mode (`<Space>t`) with `TX n` in the status bar, `:commit` / `:rollback`,
  and a prompt before quitting or closing a console with an open transaction.
- SSH host field in the new connection form.

### Changed
- `:q!` / `:qa!` skip the open-transaction prompt.

## [0.2.0] - 2026-10-13

### Added
- SQL console (`<space>c`) with statement splitting, run statement under cursor or all,
  async execution and `Ctrl-c` cancellation on the server.
- Editing the console in `$EDITOR` (`<space>v`).
- Copy results as TSV/CSV/JSON/Markdown/INSERT and export them as CSV/JSON/SQL/xlsx,
  including streaming whole-table exports.
- SQL history (`<space>h`) and console drafts that survive restarts.
- Read-only connections enforced in the app and on the database session; confirmation
  for UPDATE/DELETE without WHERE.
- New connection form (`<space>n`); sqlvim starts with an empty config.
- Search (`/`, `n`, `N`) in the tree and grids; `zh`/`zl` sideways scrolling.
- Automatic reconnect for lost connections.

### Changed
- `Ctrl-c` cancels the running query instead of quitting; use `:qa` to quit.

## [0.1.0] - 2026-10-13

### Added
- Connection tree, paged table data, structure view, help and which-key hints.
