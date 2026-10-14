# sqlvim

A terminal SQL client for MySQL and PostgreSQL with vim keys and DataGrip-style workflows.

[中文说明](README.zh-CN.md)

![sqlvim demo](docs/demo.gif)

- Browse databases, filter and sort table data, and **edit it in place**: changes are staged,
  shown as SQL and submitted in one transaction.
- A highlighted SQL console with a **built-in vim editor**, completion and `$EDITOR` hand-off.
- `<Space><Space>` searches every table, view, column and routine; `<Space>e` reopens recent tabs.
- Copy and export results (TSV, CSV, JSON, Markdown, INSERT, xlsx), SQL history.
- SSH tunnels (agent, keys, ProxyJump), production safeguards, manual transactions.
- Your own keys (`keymap.toml`) and colors (`[theme]`).

## Install

```sh
brew install shepherd-yun/tap/sqlvim
```

or with Go (the version in `go.mod`):

```sh
go install github.com/shepherd-yun/sqlvim/cmd/sqlvim@latest
```

Binaries for macOS and Linux are on the [releases page](https://github.com/shepherd-yun/sqlvim/releases).

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

## Editing table data

Changes are staged: edited cells turn yellow, new rows green and rows marked for deletion red.
`<Space>w` (or `:w`) shows the statements; `y` runs them in one transaction, which is rolled
back if any statement fails or does not change exactly one row. Rows are located by their
primary key, else a unique index over NOT NULL columns; tables without one are matched by all
their values with `LIMIT 1` (MySQL) or by `ctid` (PostgreSQL), and the preview says so.
Read-only connections refuse changes, PROD connections show `PROD ·` in the preview, and
paging, refreshing, filtering, sorting, closing the tab or quitting ask or refuse while
changes are staged. Console results stay read-only.

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
| `1`–`9` | Go to tab (in the console editor digits are counts: use `gt` / `gT` / `{n}gt`) |
| `H` `L`, `gT` `gt` | Previous / next tab, wrapping around (from any pane or tab) |
| `/` `n` `N` | Search in the tree or a result grid |
| `[` `]` | Previous / next page (data), section (structure); `]` loads more console rows |
| `0` / `$` | Grid: first / last column |
| `zh` `zl` / `zH` `zL` | Scroll a grid sideways by half a screen / one column |
| `K` | Show full cell value |
| `ff` / `f!` | Filter: the column equals / is not the cell's value (NULL → `IS NULL` / `IS NOT NULL`) |
| `fs` or `f~` | Filter: the column contains… (asks, starting from the cell; ignores case; `%` `_` match themselves) |
| `fl` | Filter: the column is LIKE a pattern you type (`%` any text, `_` one character; starts from `%cell%`; ignores case) |
| `f>` / `f<` | Filter: the column is greater / less than… (asks, starting from the cell) |
| `fn` / `fN` | Filter: the column is / is not NULL |
| `fx` | Clear the filter |
| `F` | Edit the filter as SQL (`WHERE …`): starts from the column, `Tab` completes column names and keywords |
| `s` | Table data: sort by the column — ascending, descending, off |
| `i` / `Enter` | Table data: edit the cell in a vim editor (`Enter` saves, `Ctrl-n` NULL, `Esc` NORMAL / cancel) |
| `o` / `dd` | Table data: add a row below / mark the row for deletion |
| `u` / `U` | Table data: undo the last change / discard every change |
| `<Space>w`, `:w` | Table data: preview the changes as SQL, `y` submits them in one transaction |
| `yl` / `yy` | Copy cell / row |
| `p` | Re-enter connection password (tree) |
| `<Space>c` | SQL console for the selected connection / database |
| `Enter` / `R` | Console: run statement under cursor, or the visual selection / run all (`Ctrl-Enter` or `Ctrl-j` in insert mode) |
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
| `<Space>r` | Refresh the tree node or tab and reload the connection's metadata |
| `?` | Help for the focused pane |
| `:q` / `:qa` | Close tab / quit |
| `<Space>t` | Console: toggle manual commit (`TX n` in the status bar) |
| `:commit` / `:rollback` | Console: end the transaction |
| `:set noreadonly` / `:set readonly` | Unlock / lock the connection for this session |
| `:q!` / `:qa!` | Close / quit without asking about open transactions |
| `<Space><Space>` | Search tables, views, columns, routines, schemas and databases (`@` searches every connection and loads the metadata of those not opened yet; `Enter` opens, `Ctrl-s` opens the structure) |
| `<Space>e` | Recently opened tables, structures and consoles |
| `↓` / `↑`, `Ctrl-n` / `Ctrl-p`, `Ctrl-j` / `Ctrl-k`, `Tab` / `Shift-Tab` | Console, insert mode: next / previous completion (`Ctrl-n` / `Ctrl-p` open the popup when closed) |
| `Enter` / `Ctrl-y` | Console, insert mode: accept the completion (`Esc` closes the popup, a second `Esc` leaves insert mode) |

Filters made with `f…` are added with `AND`; `F` shows and edits the whole condition. Text inputs
take readline keys: `←` `→`, `Home` `End` / `Ctrl-a` `Ctrl-e`, `Alt-b` `Alt-f`, `Ctrl-w`, `Ctrl-u`, `Ctrl-k`.

Press `<Space>` and wait to see every leader key. These are the defaults; see Customize.

## Customize

**Keys** — `~/.config/sqlvim/keymap.toml` changes the leader and binds, rebinds or unbinds
keys per context ([example with every action](docs/examples/keymap.toml)):

```toml
leader = ","

[grid]
"S" = "sort.column"
"s" = ""
```

sqlvim checks the file at startup and names the binding that is unknown or conflicts.
Help (`?`), which-key and the hints follow your keys. The console editor's vim keys, text
inputs and popup lists keep their keys.

**Colors** — a `[theme]` table in `config.toml`; values are ANSI indexes `"0"`–`"255"` or
`"#rrggbb"`. Slots: `border` `focus` `error` `accent` `on_error` `visual_bg` `visual_fg`
`changed` `inserted` `view` `builtin` `string` `number` `operator`.

```toml
[theme]
focus  = "#7aa2f7"
accent = "14"
```

**Ghostty** — map Cmd shortcuts to leader keys with
[docs/examples/ghostty.conf](docs/examples/ghostty.conf), e.g. `keybind = cmd+e=text:\x20e`.

## Console editor

The console is a vim editor: NORMAL, INSERT, VISUAL (`v`) and V-LINE (`V`) modes;
motions `h j k l w b e W B E 0 ^ $ gg G f F t T % { }` and `Ctrl-d` `Ctrl-u`; operators
`d c y > <` with a motion, a text object (`iw aw i" a" i' a' i( a( i[ a[ i{ a{`) or doubled
(`dd` `cc` `yy` `>>` `<<`); `x D C p P o O i a I A`; `u` / `Ctrl-r` undo and redo; `.`
repeats the last change; counts (`3dd`, `2dw`); registers `"a`–`"z`, and `"+` for the
system clipboard. In the editor the digits are counts, so `gt` / `gT` / `{n}gt` or `H` / `L`
switch tabs. `Enter` in VISUAL mode runs the selected statements. `>` and `<` indent by four
spaces; `Tab` in INSERT mode types four spaces.

Not built in: macros, ex ranges, windows, `/` search — `<Space>v` opens the console in
`$EDITOR` for those.

## Search and completion

sqlvim reads each connection's catalog (tables, views, columns, routines) in the
background the first time you use it — the whole server for MySQL, one database at a time
for PostgreSQL — and keeps it in memory. `<Space>r` reloads it; running DDL in a console
reloads it automatically.

In the console, completion pops up as you type: tables after `FROM`/`JOIN`/`UPDATE`/`INTO`,
columns of the statement's tables (and of `alias.`), keywords elsewhere. Names that need
quoting (`order`, `MixedCase` on PostgreSQL) are inserted quoted. When a statement fails
with a syntax error, the cursor jumps to where the server says the error is.

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
make snapshot           # build release archives locally with GoReleaser
make demo               # re-record docs/demo.gif (needs vhs and Docker)
```

Logs: `~/.local/state/sqlvim/sqlvim.log` (`--debug` for more detail).
See [CONTRIBUTING.md](CONTRIBUTING.md) before sending a pull request.

## License

MIT — see [LICENSE](LICENSE).
