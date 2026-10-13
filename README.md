# sqlvim

A terminal SQL client for MySQL and PostgreSQL with vim keys and DataGrip-style workflows.
一个带 vim 键位、DataGrip 式工作流的终端 SQL 客户端，支持 MySQL 和 PostgreSQL。

> Status: v0.2 — browse databases and tables, write and run SQL in a console
> (with `$EDITOR` support), copy and export results, SQL history, read-only
> protection. SSH tunnels and production safeguards arrive in v0.3.

## Install

```sh
go install github.com/shepherd-yun/sqlvim/cmd/sqlvim@latest
```

## Configure

Create `~/.config/sqlvim/connections.toml` — see [docs/examples/connections.toml](docs/examples/connections.toml).
On first connect sqlvim asks for the password and can save it to the macOS Keychain.

Optional `~/.config/sqlvim/config.toml` (see [docs/examples/config.toml](docs/examples/config.toml)):

```toml
page_size        = 200
max_column_width = 40
max_rows         = 1000          # rows fetched per console query
editor           = "nvim"        # <space>v; default $VISUAL, $EDITOR, nvim
export_dir       = "~/Downloads"
```

No config yet? Start sqlvim and press `<space>n` to add a connection.

## Keys

| Key | Action |
|---|---|
| `j` `k` `h` `l`, `gg` `G`, `Ctrl-d` `Ctrl-u` | Move |
| `l` / `Enter` | Expand node / open table (retry a disconnected connection) |
| `h` | Collapse / go to parent |
| `Tab`, `Ctrl-h` `Ctrl-l` | Switch pane |
| `1`–`9` | Go to tab |
| `/` `n` `N` | Search in the tree or a result grid |
| `[` `]` | Previous / next page (data), section (structure); `]` loads more console rows |
| `zh` `zl` | Scroll a grid sideways |
| `K` | Show full cell value |
| `yl` / `yy` | Copy cell / row |
| `p` | Re-enter connection password (tree) |
| `<Space>c` | SQL console for the selected connection / database |
| `i` `a` / `Esc` | Console: insert mode / back to normal mode |
| `Enter` / `R` | Console: run statement under cursor / run all (`Ctrl-Enter` or `Ctrl-j` in insert mode) |
| `Ctrl-c` | Cancel the running query (never quits) |
| `Ctrl-j` / `Ctrl-k` | Console: results / editor; `M` toggles the message log |
| `<Space>v` | Edit the console in `$EDITOR` |
| `<Space>h` | SQL history |
| `<Space>y t/c/j/m/i` | Copy result as TSV / CSV / JSON / Markdown / INSERT |
| `<Space>x c/j/s/x` | Export result as CSV / JSON / SQL / xlsx |
| `<Space>n` | New connection |
| `<Space>s` | Table structure |
| `<Space>b` | Toggle tree |
| `<Space>r` | Refresh |
| `?` | Help for the focused pane |
| `:q` / `:qa` | Close tab / quit |

Press `<Space>` and wait to see every leader key.
Ghostty users can map Cmd shortcuts to leader keys: [docs/examples/ghostty.conf](docs/examples/ghostty.conf).

## Safety

- `readonly = true` (the default for `env = "prod"`) blocks INSERT/UPDATE/DELETE and DDL
  in the console, and the database session itself is read-only as a second line of defence.
- `UPDATE` / `DELETE` without `WHERE` asks you to type the table name before it runs.

## Development

```sh
make test               # unit tests
make test-integration   # needs Docker (OrbStack: `orb start`)
make lint
```

Logs: `~/.local/state/sqlvim/sqlvim.log` (`--debug` for more detail).

## License

MIT
