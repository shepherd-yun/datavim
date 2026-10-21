# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

datavim is a terminal SQL client (TUI) for MySQL and PostgreSQL with vim keys and DataGrip-style workflows. It is written in Go and uses Bubble Tea v2 (`charm.land/bubbletea/v2`) and Lip Gloss v2.

## Commands

```sh
make build              # bin/datavim
make run                # build and run
make test               # go test -race ./...
make test-integration   # go test -race -tags integration ./internal/db/... ./internal/rdb/... ./internal/kafka/... ./internal/es/...  (needs Docker; OrbStack: `orb start`)
make lint               # golangci-lint (gofumpt + goimports formatters, revive, gocritic, errorlint, …)
make snapshot           # local GoReleaser build
make release-notes VERSION=1.0.0   # prints that CHANGELOG.md section (used by the release workflow)
make demo               # re-record docs/demo.gif (vhs + Docker)
```

Single test: `go test -race ./internal/ui -run TestName`. Integration tests sit behind the `integration` build tag, e.g. `go test -race -tags integration ./internal/db/postgres -run TestContract`.

Run against a test config: `./bin/datavim --config <dir> --debug`. The log is written to `~/.local/state/datavim/datavim.log`.

CI runs `go vet`, the unit tests on Ubuntu and macOS, the integration tests, and golangci-lint.

## Architecture

The design spec is `docs/superpowers/specs/2026-10-13-datavim-design.md` (in Chinese). Each release has an implementation plan in `docs/superpowers/plans/`. Read the spec before making a structural change.

Dependencies only point downward: `ui` → `session`/`meta`/`edit`/`export`/`history`/`sqlx` → `db` → drivers. Everything lives under `internal/`, and there is no public API.

- **`internal/db`** is the only connection to database servers; the UI never deals with dialect differences. `driver.go` defines `Driver`, `Conn` (pooled, safe for concurrent use), `Session` (a dedicated server connection used for console transactions), `Querier` (`Query`/`Exec`/`Stream`), `MetaReader` and `Dialect`. `db/mysql` and `db/postgres` implement them: catalog/meta queries, DDL, and error positions via `errpos.go`. A database is called a `Namespace{Database, Schema}`, and MySQL leaves `Schema` empty. `Driver.DatabaseScoped()` is true for PostgreSQL, so it needs one `Conn` per database.
- **`internal/db/dbtest/contract.go`** is the contract that every driver must pass. The integration tests for each driver start MySQL 8 or PG 16 with testcontainers and call `dbtest.RunContract`. Driver behaviour changes belong in this contract, so both engines get tested.
- **`internal/db/tunnel`** implements SSH tunnels with `x/crypto/ssh`. They are injected as `ConnConfig.Dial` and never listen on a local port. Tests run an in-memory SSH server.
- **`internal/session`** (`Manager`) owns connections, passwords (keychain via `internal/secret`), tunnels and read-only toggles. The UI uses it through the `ui.Backend` interface (`internal/ui/backend.go`), and `ui.Store` (`store.go`) is backed by `internal/history`, which uses SQLite at `~/.local/state/datavim/state.db`.
- **Pure, terminal-free packages:** `sqlx` (lexing, statement splitting, statement classification for readonly/prod guards, completion context, tx detection), `edit` (staged cell/row changes → DML plan, row location by PK / unique NOT NULL index / all values / `ctid`), `export`, `meta` (in-memory catalog cache used by search and completion), `config` (TOML: `connections.toml`, `config.toml`, `keymap.toml`), and `ui/vimedit` (the built-in vim editor state machine).
- **`internal/ui`** is the Bubble Tea app. `app.go`'s `Model` handles layout, focus, overlays and key dispatch. Tabs (`tabs.go`, `console.go`, `datatab_edit.go`, structure) implement the `tab` interface, and async results come back as messages with `TabID()` so they reach the right tab. The UI does no IO directly: every query runs in a `tea.Cmd` with a cancellable `context.Context`. `ui/panes/*` are the tree/grid/editor/structure/completion widgets, and `ui/overlay/*` are the popups (pickers, confirm, cmdline, which-key, forms).
- **Keys:** `ui/keymap` maps (context × key sequence) → `Action`. The default bindings, with their descriptions, are in `keymap/defaults.go`, and help, which-key, the hints (`ui/hints.go`) and the welcome text are all generated from them. Never hardcode a key name in UI text; use `keyHint` so user remaps from `keymap.toml` show up. The console editor's own vim keys (`vimedit`) are not in the keymap.
- `ui/theme` defines named color slots that `[theme]` in `config.toml` can override.

## Testing conventions

- Development is test-first. Tests are table-driven for the pure packages, and `vimedit` tests take initial text + cursor + a key sequence and expect a given text/cursor/mode.
- UI tests drive `Model.Update` with messages and assert on state. They use `fakeConn`/fake backend in `internal/ui/fakes_test.go` and `uitest.Key("ctrl+d")` to build key events.

## Conventions

- Conventional Commits with a scope: `feat(ui): …`, `fix(keymap): …`, `fix(db): …`, `docs: …`.
- Every user-visible change gets a line under `[Unreleased]` in `CHANGELOG.md` (Keep a Changelog format). Release notes are taken from it.
- UI text is in English. README changes must also be made in `README.zh-CN.md`.
- Passwords must never be logged or shown.
- Safety rules (readonly, prod confirmations, UPDATE/DELETE without WHERE, staged edits that must affect exactly one row, refusing to reconnect silently inside a transaction) are core behaviour. Keep them when changing execution paths.
