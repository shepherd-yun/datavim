# sqlvim

A terminal SQL client for MySQL and PostgreSQL with vim keys and DataGrip-style workflows.
一个带 vim 键位、DataGrip 式工作流的终端 SQL 客户端，支持 MySQL 和 PostgreSQL。

> Status: v0.1 — browse connections, databases, schemas and tables; page through table
> data; inspect structure. Writing SQL arrives in v0.2.

## Install

```sh
go install github.com/shepherd-yun/sqlvim/cmd/sqlvim@latest
```

## Configure

Create `~/.config/sqlvim/connections.toml` — see [docs/examples/connections.toml](docs/examples/connections.toml).
On first connect sqlvim asks for the password and can save it to the macOS Keychain.

Optional `~/.config/sqlvim/config.toml`:

```toml
page_size        = 200
max_column_width = 40
```

## Keys

| Key | Action |
|---|---|
| `j` `k` `h` `l`, `gg` `G`, `Ctrl-d` `Ctrl-u` | Move |
| `l` / `Enter` | Expand node / open table |
| `h` | Collapse / go to parent |
| `Tab`, `Ctrl-h` `Ctrl-l` | Switch pane |
| `1`–`9` | Go to tab |
| `[` `]` | Previous / next page (data), section (structure) |
| `K` | Show full cell value |
| `p` | Re-enter connection password (tree) |
| `<Space>s` | Table structure |
| `<Space>b` | Toggle tree |
| `<Space>r` | Refresh |
| `?` | Help for the focused pane |
| `:q` / `:qa` | Close tab / quit |

Press `<Space>` and wait to see every leader key.
Ghostty users can map Cmd shortcuts to leader keys: [docs/examples/ghostty.conf](docs/examples/ghostty.conf).

## Development

```sh
make test               # unit tests
make test-integration   # needs Docker (OrbStack: `orb start`)
make lint
```

Logs: `~/.local/state/sqlvim/sqlvim.log` (`--debug` for more detail).

## License

MIT
