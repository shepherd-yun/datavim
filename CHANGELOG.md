# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

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
