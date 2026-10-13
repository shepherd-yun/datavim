# sqlvim

A terminal SQL client for MySQL and PostgreSQL with vim keys and DataGrip-style workflows.
一个带 vim 键位、DataGrip 式工作流的终端 SQL 客户端，支持 MySQL 和 PostgreSQL。

> Status: v0.3 — browse databases and tables, write and run SQL in a console
> (with `$EDITOR` support), copy and export results, SQL history, SSH tunnels
> (agent, keys, ProxyJump), production safeguards and manual transactions.
> Search, completion and highlighting arrive in v0.4.

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

## SSH tunnels

Set `ssh` to a `Host` alias from `~/.ssh/config`; `host` is then resolved by the SSH server:

```toml
[[connection]]
name     = "prod-pg"
driver   = "postgres"
host     = "10.0.1.5"   # as seen from the bastion
user     = "app"
database = "app"
env      = "prod"
ssh      = "bastion"
```

sqlvim reads `HostName`, `Port`, `User`, `IdentityFile`, `ProxyJump` (any number of hops)
and `UserKnownHostsFile`. It tries ssh-agent first, then your identity files, and asks
for a key's passphrase only when nothing else works (passphrases are kept in memory).
Unknown hosts show their fingerprint and are added to `known_hosts` once you type `yes`;
a changed host key is an error. `ProxyCommand` is not supported.

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
| `e` / `dd` | Tree: edit / delete the selected connection (saved to `connections.toml`) |
| `<Space>s` | Table structure |
| `<Space>b` | Toggle tree |
| `<Space>r` | Refresh |
| `?` | Help for the focused pane |
| `:q` / `:qa` | Close tab / quit |
| `<Space>t` | Console: toggle manual commit (`TX n` in the status bar) |
| `:commit` / `:rollback` | Console: end the transaction |
| `:set noreadonly` / `:set readonly` | Unlock / lock the connection for this session |
| `:q!` / `:qa!` | Close / quit without asking about open transactions |

Press `<Space>` and wait to see every leader key.
Ghostty users can map Cmd shortcuts to leader keys: [docs/examples/ghostty.conf](docs/examples/ghostty.conf).

## Safety

- `readonly = true` (the default for `env = "prod"`) blocks INSERT/UPDATE/DELETE and DDL
  in the console, and the database session itself is read-only as a second line of defence.
  `:set noreadonly` unlocks the connection until sqlvim exits; `connections.toml` is not changed.
- `env = "prod"` frames the tab area in red and shows `[PROD]` in the status bar; every
  write or DDL statement on a prod connection asks for `y` first.
- `UPDATE` / `DELETE` without `WHERE` asks you to type the table name before it runs.
- In manual commit mode (`<Space>t`) quitting or closing a console with an open transaction
  asks first. If the connection drops during a transaction, sqlvim says the transaction is
  lost instead of reconnecting silently. Use `<Space>t` rather than `SET autocommit = 0`.

## Development

```sh
make test               # unit tests
make test-integration   # needs Docker (OrbStack: `orb start`)
make lint
```

Logs: `~/.local/state/sqlvim/sqlvim.log` (`--debug` for more detail).

## License

MIT
