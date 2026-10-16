# sqlvim

A terminal SQL client for MySQL, PostgreSQL, SQL Server and Oracle with vim keys and DataGrip-style workflows. It also browses and queries Redis.

[中文说明](README.zh-CN.md)

![sqlvim demo](docs/demo.gif)

- Browse databases, filter and sort table data, and **edit it in place**: changes are staged,
  shown as SQL and submitted in one transaction. Expand a table in the tree to see its columns,
  keys, foreign keys and indexes.
- A highlighted SQL console with a **built-in vim editor**, completion and `$EDITOR` hand-off.
- `<Space><Space>` searches every table, view, column and routine; `<Space>f` reopens recent tabs.
- Copy and export results (TSV, CSV, JSON, Markdown, INSERT, xlsx), SQL history.
- Import CSV, TSV and xlsx files into a table, or into a new table inferred from the file.
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
compare_max_rows = 100000        # rows a data compare reads per side
editor           = "nvim"        # <space>v; default $VISUAL, $EDITOR, nvim
export_dir       = "~/Downloads"
icons            = "nerd"        # tree icons: "nerd" (needs a Nerd Font) or "plain"
```

The tree draws [Nerd Font](https://www.nerdfonts.com) icons (v3): the MySQL / PostgreSQL / Redis logo
before each connection, and database, schema, table, folder and key icons below it. Without a
Nerd Font they show as boxes: set `icons = "plain"`.

No config yet? Start sqlvim and press `<space>n` to add a connection.

**SQL Server** (2014–2022, user name and password): `driver = "sqlserver"`, port 1433. Values in
`params` go to the driver as they are. Older servers often need TLS relaxed:
`params = { encrypt = "disable" }`, or `TrustServerCertificate = "true"` for a self-signed
certificate, and `tlsmin = "1.0"` for a server that only speaks TLS 1.0/1.1. The connection form
(`<space>n`) has `Encrypt` and `Trust cert` fields for these. Named instances
(`host\instance`) are not supported: give the instance's port. Modify Table, schema and data
compare and import are not available on SQL Server yet.

**Oracle** (12c and later, user name and password, no Instant Client needed): `driver = "oracle"`,
port 1521, and `database` is the service name (`FREEPDB1` when empty; the form calls it
`Service`). SIDs, TNS names and wallets are not supported. The tree lists schemas right under the
connection. Statements end with `;`; a PL/SQL block (`BEGIN`, `DECLARE`, `CREATE PROCEDURE` …)
ends with a line holding only `/`, as in SQL*Plus. Statements commit on their own; `<Space>t` or
`SET TRANSACTION` keeps a transaction open until `COMMIT` or `ROLLBACK`. Modify Table, compare
and import are not available on Oracle yet.

**Redis** (5.0 and later, standalone servers): `driver = "redis"`,
port 6379, and `database` is the default db number (0 when empty; the form calls it `DB`). A
password is optional: sqlvim connects without one and asks only when the server answers
`NOAUTH`. `params`: `separator` groups the keys into folders (default `:`), `tls = "true"` and
`tls_skip_verify = "true"`. Expanding a db scans its keys with `SCAN` in batches of 1,000 (never
`KEYS`): a load stops after 10,000 keys, `↓ load more` goes on (at most 100,000 keys per db),
`Ctrl-c` stops it, and on prod connections sqlvim pauses between batches. `F` on a db, folder or
key filters with a pattern (`user:*:sess`; text without `*` `?` `[` matches anywhere) and stops
at 500 matches or 100,000 keys scanned; `fx` clears it. `<Space>k` opens a key by name (`Tab`
completes the keys scanned so far). `Enter` on a key opens its tab: type, TTL, length, size and
encoding, then the value — a string (only the first 1 MB of a bigger one; `v` cycles auto /
text / JSON / hex) or the fields, items or members of a hash, list, set, sorted set or stream,
a page at a time (`]` loads more, `F` filters hash fields and set members, `K` shows a value).
Binary key names show as `\xNN`; `yy` in the tree copies a key name as redis-cli takes it.

The Redis console (`<Space>c` on a Redis connection, db or key) runs one command a line, quoted
as in redis-cli (`SET "a b" "line\n"`, `'it\'s'`); `#` and `//` lines are comments. `Enter` runs
the line under the cursor or the selected lines, `R` everything and `<Space>R` from the cursor
on; a failed command stops the rest. A reply shows as a value (JSON indented, binary as hex), a
table (arrays, field / value pairs of `HGETALL` or `CONFIG GET`, member / score pairs of
`ZRANGE … WITHSCORES`, the keys of a `SCAN`) or redis-cli's indented text; `M` shows the message
log and `g d` on a table cell opens that key. The console keeps its own connection, so `SELECT`
(the tab title shows the db) and `MULTI` / `WATCH` last; the status bar shows `TX n` or `WATCH`,
and closing the tab or quitting asks first. `Ctrl-c` cancels a blocking command such as
`BLPOP q 0`; that drops the connection, and the next command reconnects on the same db. Inside
`MULTI` or `WATCH` nothing reconnects: sqlvim says the transaction is lost. Typing completes
commands, subcommands and keys (the keys the tree scanned, plus a small `SCAN` for the prefix)
and shows the command's arguments. Known limits: other connection state (`CLIENT SETNAME` …) is
not restored after a reconnect, `SELECT` inside `MULTI` is refused, `:e` / `:w` do not work in a
Redis console, and `SUBSCRIBE`, `MONITOR`, `HELLO`, `RESET`, `CLIENT TRACKING` and the like are
not supported. Structure, compare and import are not available for Redis.

Redis keys are edited in their tab the way table data is: `i` / `Enter` edits a cell (a string
value: the cell editor, or `$EDITOR` for long values and values with newlines; `<Space>v` always
opens `$EDITOR`), `o` adds a field, element (`head` or `tail` in the index column says where it
goes), member or stream entry, `dd` deletes one, `u` / `U` undo, and `<Space>w` shows the checks
and commands before it submits. sqlvim `WATCH`es the key and checks that every value the
changes rely on (a field's value, the element at an index, a member's score) is still what was
loaded; if not, or if the key is written before `EXEC`, nothing is written. Redis does not roll
back: when a command fails inside `EXEC`, the ones before and after it are still applied, and
sqlvim says which one failed. Strings over 1 MB (only partly loaded) and binary values are not
edited in place; use the console. On a key tab `t` sets or removes the TTL, `r` renames
(`RENAMENX`, confirming before it overwrites an existing key) and `D` deletes (`UNLINK`); in the
tree `r` / `dd` on a key do the same, `o` creates a key, and `dd` on a folder or db deletes the
keys matching it (the prefix, or the db's filter): sqlvim counts them, lists the first 20 and
deletes them 500 at a time; `Ctrl-c` stops it. Keys written while it runs may be missed.

The server tab (`<Space>i` on a Redis connection, or `Server status` in its node menu) shows the
server's status from `INFO` — version, role, uptime, clients, keys, memory, hit rate, evicted
and expired keys — with sparklines of ops/sec, memory and network traffic. It samples every 2
seconds, and only while its status section is on screen: switching to another tab or section
stops it. `[` / `]` switch to the slow log and the client list (`<Space>r` reloads them); `dd`
in the client list kills a client after a confirmation. Whatever the server's ACL refuses shows
`permission denied`. `*` stars the key under the cursor or in the tab, `<Space>F` lists the
starred keys, and opened keys are listed in `<Space>f`. On a key tab `<Space>x…` exports the
value (the loaded rows or the whole key; binary values are written as `\xNN`). `B` on a db or
folder finds the biggest keys of each type: it walks every key that matches the db's filter or
the folder (in batches of 1,000, pausing between them, `Ctrl-c` stops it), so on a production
instance run it at a quiet time.

## Editing table data

Changes are staged: edited cells turn yellow, new rows green and rows marked for deletion red.
`<Space>w` (or `:w`) shows the statements; `y` runs them in one transaction, which is rolled
back if any statement fails or does not change exactly one row. Rows are located by their
primary key, else a unique index over NOT NULL columns; tables without one are matched by all
their values with `LIMIT 1` (MySQL) or `TOP (1)` (SQL Server), or by `ctid` (PostgreSQL) or
`ROWID` (Oracle), and the preview says so.
Read-only connections refuse changes, PROD connections show `PROD ·` in the preview, and
paging, refreshing, filtering, sorting, closing the tab or quitting ask or refuse while
changes are staged. The same filter and edit keys work on console results of a single-table SELECT.

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

## Workspaces

A workspace is its own set of connections, say one for work and one for personal databases.
Each has its own `connections.toml`, keychain passwords, history and console drafts; settings,
theme and keys are shared.

```sh
sqlvim -w personal --create   # create it once
sqlvim -w personal            # or: SQLVIM_WORKSPACE=personal sqlvim
sqlvim --list-workspaces
```

Without `-w` sqlvim opens the `default` workspace, which is the files it has always used.
The workspace name shows in the status bar and the window title.

| | default | `personal` |
|---|---|---|
| connections | `~/.config/sqlvim/connections.toml` | `~/.config/sqlvim/workspaces/personal/connections.toml` |
| history, drafts | `~/.local/state/sqlvim/state.db` | `~/.local/state/sqlvim/workspaces/personal/state.db` |
| keychain account | `<connection>` | `personal/<connection>` |

To move connections into a workspace, create it, cut their `[[connection]]` blocks into its
`connections.toml`, and enter each password once more on first connect. Their history stays
in the default workspace.

With [direnv](https://direnv.net), `export SQLVIM_WORKSPACE=work` in a project's `.envrc`
picks the workspace by directory.

## Keys

| Key | Action |
|---|---|
| `j` `k` `h` `l`, `gg` `G`, `Ctrl-d` `Ctrl-u` | Move |
| `l` / `→` | Expand a node; a table or view shows its Columns, Keys, Foreign Keys and Indexes (retry a disconnected connection) |
| `Enter` | Open a table's data; on a column, key or index open the structure at it, on a foreign key go to the referenced table |
| `zo` `zc` `za` | Expand / collapse (on a column, its group) / toggle |
| `zO` `zC` | Expand everything loaded / collapse everything under the cursor |
| `zR` `zM` `zx` | Expand everything loaded / collapse everything / collapse all but the path to the cursor |
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
| `yy` / `yq` | Tree: copy the name of the connection, database, schema or table / its qualified name as SQL writes it (`app.users`) |
| `yy` | Structure: copy the name of the column, index or foreign key under the cursor (on the DDL page, the DDL) |
| `p` | Re-enter connection password (tree) |
| `<Space>c` | SQL console for the selected connection / database |
| `Enter` / `R` | Console: run statement under cursor, or the visual selection / run all (`Ctrl-Enter` or `Ctrl-j` in insert mode) |
| `<Space>R` | Console: run from the statement under the cursor to the end |
| `Ctrl-c` | Cancel the running query (never quits) |
| `Ctrl-j` / `Ctrl-k` | Console: results / editor; `M` toggles the message log |
| `<Space>v` | Edit the console in `$EDITOR` |
| `<Space>h` | SQL history |
| `<Space>y t/c/j/m/i` | Copy result as TSV / CSV / JSON / Markdown / INSERT |
| `<Space>yd` | Copy the DDL of the table under the cursor (tree, data or structure tab); `y` in a routine's definition copies it |
| `<Space>x c/j/s/x` | Export result as CSV / JSON / SQL / xlsx |
| `I` | Tree: import a CSV / TSV / xlsx file into the table; on a database or schema, into a new table |
| `x` / `a` `A` / `i` / `t` | Import tab: import or skip a column / all / none / pick the target column (new table: rename) / new table: column type; `[` `]` switch sections, `<Space>w` checks and imports |
| `<Space>n` | New connection |
| `e` | Tree: edit the selected connection (saved to `connections.toml`), or modify the selected table — on a column or index, with the cursor on it |
| `e` | Structure: modify the table — `i` edits a field (`h` / `l` pick it), `o` adds a column or index, `dd` deletes, `u` / `U` undo, `<Space>w` previews and runs the ALTER |
| `dd` | Tree: delete the selected connection, or drop the selected table or view (type its name to confirm); on a column or index, stage its drop in Modify Table |
| `r` | Tree: rename the selected table or view |
| `T` | Tree: truncate the selected table (type its name to confirm) |
| `a` | Tree: actions on the selected node; press an entry's key to run it (the same key as in the tree) |
| `m` / `C` | Tree: mark a table, MySQL database or PostgreSQL schema as the compare source / compare it with the selected one (without a mark, pick the source from the loaded metadata) |
| `x` / `]c` `[c` / `~` | Diff tab: include or skip a change in the script / next, previous change / swap source and target |
| `<Space>w` | Diff tab: write the picked changes as a script into a new console on the target (nothing runs until you run it there) |
| `D` / `F` | Diff tab: compare the rows of the tables by their key, or go back to the structure / in the data view, a WHERE both sides are read with |
| `<Space>s` | Columns popup: type to filter, `Enter` puts the data tab's cursor on the column (opening the tab if need be), `Ctrl-o` opens the full structure (indexes, foreign keys, DDL). In a console it lists the columns of the result and moves the results cursor |
| `<Space>S` | Full structure of the table under the cursor, without the popup |
| `<Space>e` | Toggle tree |
| `<Space>l` | Locate the current tab in the tree: the table, or the database / schema of a console (also `:locate`) |
| `<Space>r` | Refresh the tree node or tab and reload the connection's metadata |
| `?` | Help for the focused pane |
| `:q` / `:qa` | Close tab / quit |
| `:e <file>` | Open a `.sql` file in a console on the connection / database in context |
| `:w` / `:w <file>` / `:wq` | File console: save / save as (a plain console becomes a file console) / save and close; `:w!` overwrites a file changed on disk |
| `<Space>t` | Console: toggle manual commit (`TX n` in the status bar) |
| `:commit` / `:rollback` | Console: end the transaction |
| `:set noreadonly` / `:set readonly` | Unlock / lock the connection for this session |
| `:q!` / `:qa!` | Close / quit without asking about open transactions |
| `<Space><Space>` | Search tables, views, columns, routines, schemas and databases in every connection, loading the metadata of those not opened yet (`db.table.column`, `table.column` or any part of it narrows by parent, fuzzily, and opening a column puts the cursor on it; `@` searches only the connection in context; `Enter` opens, `Ctrl-o` opens the structure). `Tab` / `Shift-Tab` switch to the lines of the open consoles (`Enter` jumps to the line) or the SQL history (an empty query lists the recent statements, `Enter` inserts one as `<Space>h` does) |
| `<Space>f` | Recently opened tables, structures, consoles, files and Redis keys |
| `F` / `fx` | Tree, on a Redis db, folder or key: filter the keys with a pattern / clear the filter |
| `<Space>k` | Open a Redis key by name |
| `v` | Redis key tab: show a string as auto / text / JSON / hex |
| `<Space>v` | Redis key tab: edit the value under the cursor in `$EDITOR` |
| `t` / `r` / `D` | Redis key tab: set or remove the TTL / rename the key / delete it |
| `o` | Tree, on a Redis db, folder or key: new key |
| `g d` | Redis console results: open the key in the cell |
| `<Space>i` | Redis server tab: status, slow log, clients |
| `[` / `]` | Redis server tab: previous / next section |
| `dd` | Redis server tab, client list: kill the client |
| `*` | Tree or Redis key tab: star / unstar the key |
| `<Space>F` | Starred Redis keys |
| `B` | Tree, on a Redis db or folder: find the biggest keys (`Enter` opens one) |
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
`changed` `inserted` `view` `builtin` `string` `number` `operator`, and the Redis type badges
`key_string` `key_hash` `key_list` `key_set` `key_zset` `key_stream` `key_other`.

```toml
[theme]
focus  = "#7aa2f7"
accent = "14"
```

**Ghostty** — map Cmd shortcuts to leader keys with
[docs/examples/ghostty.conf](docs/examples/ghostty.conf), e.g. `keybind = cmd+e=text:\x20e`.

## Console editor

The console is a vim editor that opens in INSERT mode, ready to type (`Esc` for NORMAL
mode, the leader and tab switching): NORMAL, INSERT, VISUAL (`v`) and V-LINE (`V`) modes;
motions `h j k l w b e W B E 0 ^ $ gg G f F t T % { }` and `Ctrl-d` `Ctrl-u`; operators
`d c y > <` with a motion, a text object (`iw aw i" a" i' a' i( a( i[ a[ i{ a{`) or doubled
(`dd` `cc` `yy` `>>` `<<`); `x D C p P o O i a I A`; `u` / `Ctrl-r` undo and redo; `.`
repeats the last change; counts (`3dd`, `2dw`); registers `"a`–`"z`, and `"+` for the
system clipboard. A plain `y` (no register) also copies to the system clipboard, while `x`,
`d` and `c` leave it alone; `"+p` pastes from it. In the editor the digits are counts, so `gt` / `gT` / `{n}gt` or `H` / `L`
switch tabs. `Enter` in VISUAL mode runs the selected statements. `>` and `<` indent by four
spaces; `Tab` in INSERT mode types four spaces, `Ctrl-a` / `Ctrl-e` move to the start / end
of the line. `<Space><Space>` in VISUAL mode searches for the selected text.

Not built in: macros, ex ranges, windows, `/` search — `<Space>v` opens the console in
`$EDITOR` for those.

## Search and completion

sqlvim reads each connection's catalog (tables, views, columns, routines) in the
background the first time you use it — the whole server for MySQL, one database at a time
for PostgreSQL — and keeps it in memory. `<Space>r` reloads it; running DDL in a console
reloads it automatically.

In the console, completion pops up as you type: tables after `FROM`/`JOIN`/`UPDATE`/`INTO`,
columns of the statement's tables (and of `alias.`), keywords elsewhere. A space opens what
usually comes next, with nothing selected (`Tab` or `↓` picks, `Enter` still types a newline):
`*` and the columns after `SELECT`, `FROM` after the select list, `WHERE` / `JOIN` /
`ORDER BY` … after a table, operators after a column in a condition, `AND` / `OR` after it.
Typing narrows it, and the words that fit come before other keywords. Names that need
quoting (`order`, `MixedCase` on PostgreSQL) are inserted quoted. When a statement fails
with a syntax error, the cursor jumps to where the server says the error is.

## Safety

- `readonly = true` (the default for `env = "prod"`) blocks INSERT/UPDATE/DELETE and DDL
  in the console, and the database session itself is read-only as a second line of defence.
  `:set noreadonly` unlocks the connection until sqlvim exits; `connections.toml` is not changed.
- SQL Server and Oracle have no read-only session, so there sqlvim is the only guard, and a stricter one:
  it reads every statement of a batch (T-SQL needs no semicolons) and blocks anything it does
  not know to be harmless — `EXEC` of any procedure but a few read-only system ones
  (`sp_help`, `sp_helptext`, `sp_columns`, `sp_who` …), `IF` blocks, `DBCC`, PL/SQL blocks, `CALL`
  and the like (`ALTER SESSION SET CURRENT_SCHEMA` / `NLS_…` are allowed). For a
  production server also use an account that can only read.
- Redis has no read-only session either: browsing sends only read commands, and in the console
  sqlvim alone enforces read-only — it refuses write, dangerous and unknown commands (module
  commands included). On prod, writes and unknown commands ask for `y`; dangerous commands
  (`FLUSHDB`, `FLUSHALL`, `CONFIG SET`, `CLIENT KILL`, `SHUTDOWN`, `DEBUG` …) ask on every
  connection and on prod want the connection name typed; `KEYS` is refused on a db of more than
  10,000 keys (use `SCAN` or the tree filter). Several lines are checked together before any
  runs, so `MULTI` does not get around these rules. Edits, TTL changes, renames and deletes from
  the key tab and the tree go through the same checks. For a production instance also use an
  ACL user that can only read (`+@read`).
- `env = "prod"` frames the tab area in red and shows `[PROD]` in the status bar; every
  write or DDL statement on a prod connection asks for `y` first.
- `UPDATE` / `DELETE` without `WHERE` asks you to type the table name before it runs.
- In manual commit mode (`<Space>t`) quitting or closing a console with an open transaction
  asks first. If the connection drops during a transaction, sqlvim says the transaction is
  lost instead of reconnecting silently. Use `<Space>t` rather than `SET autocommit = 0`.
- Imports run in one transaction and write nothing unless every row goes in; on MySQL a table the import created is dropped again.

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
