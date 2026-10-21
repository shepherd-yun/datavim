# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Kafka ACLs: `ACLs` under a Kafka connection in the tree opens the cluster's ACLs (principal,
  host, resource, pattern, operation, permission). `o` adds ACLs from a form (one per operation),
  `dd` deletes the ACL under the cursor after you type its principal, and `gd` opens the topic or
  group of a literal ACL. A delete matches every field of the ACL, so it never takes a prefixed or
  wildcard ACL with it; if the cluster deletes more anyway, sqlvim says so. A cluster without an
  authorizer says ACLs are off.
- An audit log of the writes sqlvim sent: SQL statements that may write (DML, DDL, unknown
  statements, and `BEGIN` / `COMMIT` / `ROLLBACK` in a console), Redis write, dangerous and
  unknown commands and key-tab edits, and every Kafka and Schema Registry write. Each entry
  keeps the time, connection, database, operation, object, a detail (SQL with passwords
  hidden, never statement parameters or message contents) and the result, failed writes
  included. `<Space>a` or `:audit [connection]` opens it; `F` searches it, `gd` opens the
  table, key, topic, group or subject. It lives in `state.db` and keeps the latest 50,000 writes.
- Kafka K2. The topic tab's Config section edits configs as staged changes (`i` edits, `dd`
  goes back to the default, `u` / `U` undo, `<Space>w` applies only what changed, and gives up
  if someone changed those configs since the preview); `o` in Partitions adds partitions; `X`
  deletes messages before an offset (in Partitions) or up to the message under the cursor (in
  Messages). `dd` in the tree deletes an empty consumer group and `X` in a group's lag deletes
  its offsets on that topic. With `params.schema_registry_url` (and `schema_registry_user`) the
  tree lists the Schema Registry's subjects and versions, messages in the Confluent wire format
  are decoded from Avro, Protobuf and JSON Schema (a schema that can't be read shows that
  message as hex with the reason, the others still decode), and `Enter` on a subject opens a
  Schema tab: versions, `m` / `C` to diff two of them, `<Space>v` to register a new version in
  `$EDITOR` after a compatibility check, `c` to set the compatibility level and `dd` to
  soft-delete a version. The producer console takes `key-schema:` / `value-schema:`
  `subject[@version][#message]` lines to serialize a JSON body, `key-encoding:` /
  `value-encoding: base64|hex`, `header-base64:` and `header-binary:` for binary data. `p` / `P` in Messages copy
  the message under the cursor / every shown message to a producer console (of this or another
  topic) to send them again byte for byte, and `:send <file.jsonl>` in a producer console sends
  a JSON Lines export after one confirmation. `I` switches a message view between
  read_committed and read_uncommitted. Connections take `params.broker_map` (dial another
  address for an advertised broker), `sasl = "oauth"` (client credentials: `oauth_token_url`,
  `oauth_client_id`, `oauth_scope`) and `sasl = "aws-msk-iam"` (`aws_region`, `aws_profile`,
  AWS's own credential chain). The registry password and the OAuth client secret are asked for
  the first time they are needed and kept in the keychain. Read-only connections refuse every
  K2 write; deleting messages, groups, group offsets and schema versions wants the topic, group
  or subject name typed on every connection, and prod asks before the other writes.

### Changed
- Kafka message views read `read_committed` by default, so messages of aborted transactions are
  no longer shown (`I` shows them again).

## [2.4.0] - 2026-10-21

### Added
- Kafka connections: `driver = "kafka"` with a broker list in `host`, SASL PLAIN / SCRAM-SHA-256 /
  SCRAM-SHA-512 (`params.sasl`, the password in the keychain), TLS and mTLS (`tls`, `tls_ca`,
  `tls_cert`, `tls_key`, `tls_skip_verify`) and SSH tunnels that carry every broker connection.
  The tree lists Brokers, Topics (grouped by name prefix, internal topics behind `.`) and Consumer
  Groups sorted by lag, lagging ones in red (`s` sorts by name; `F` / `fx` filter by name). A
  topic tab shows Messages, Partitions, Config and Consumers (`[` / `]`): the newest 200
  messages, more as the cursor passes the first or last row, `t` jumps to an offset, a time, the
  tail or some partitions (a deleted offset is said so, never skipped silently), `T` live tail,
  `F` / `ff` … filter (key, value, headers, JSON paths) and `n` / `N` search past what is loaded,
  `K` shows a message in auto / text / JSON / hex / base64 (`v`), `yy` copies it as JSON, `c`
  shows headers and `<Space>xl` exports JSON Lines. A group tab shows lag (refreshed every 5 s)
  and members; `gd` jumps to the message the group is stuck on and `R` resets offsets after a
  preview, only while the group is empty and unchanged since the preview. `<Space>c` on a topic
  opens a producer console (`key:` / `partition:` / `header:` lines, `===` between messages),
  `o` / `dd` in the tree create and delete topics, and `<Space>i` shows the cluster status.
  Browsing never joins a group, commits offsets or creates topics; read-only connections refuse
  every write, prod asks before writes and wants the group name typed to reset offsets, and
  deleting a topic always wants its name typed.

### Fixed
- Params set in the new/edit connection form (SQL Server `Encrypt` and `Trust cert`) are now
  saved to `connections.toml`; they were dropped before.
- Deleting a connection whose params are a `[connection.params]` table no longer fails, and
  editing it keeps working now that the form saves params.

## [2.3.0] - 2026-10-20

### Added
- `y` in a grid also copies the result: `yt` / `yc` / `yj` / `ym` / `yi` as TSV / CSV / JSON /
  Markdown / INSERT, like `<Space>y…`. `yd` copies the DDL in the tree, a grid and the structure
  tab, so the `y` popup lists every copy.

## [2.2.0] - 2026-10-19

### Added
- The new/edit connection form has a `Read-only` field: `auto` (on for prod, the default),
  `yes` or `no`, saved as `readonly` in `connections.toml`. Editing other fields keeps a
  `:set [no]readonly` switch for the session and leaves it out of the file.

## [2.1.1] - 2026-10-19

### Changed
- `p` in a console, the Redis console or the cell editor puts what `y` last copied anywhere: a
  cell, a row, a tree name, a DDL or a result copied as CSV/JSON/INSERT. The editors share one
  set of registers, so text yanked in one console also puts in another.

## [2.1.0] - 2026-10-19

### Added
- Server status for MySQL, PostgreSQL, SQL Server and Oracle connections (`<Space>i`, or
  `Server status` in the tree menu): version, uptime, role, connections, cache hit rate and
  more, with sparklines of the load; the sessions with what they run (`c` cancels a query, `dd`
  disconnects a session, guarded like Redis's client kill), lock waits, the top statements,
  settings (`F` filters them) and replication. Passwords in the SQL shown are masked.

### Fixed
- A console whose session the server killed now reports the connection as lost (Oracle and
  other `database/sql` drivers answered with "connection is already closed").

## [2.0.3] - 2026-10-19

### Added
- Tree: `Z` collapses everything, like `zM` (as in the LazyVim explorer).

## [2.0.2] - 2026-10-17

### Added
- Full screen: `+` (or `=`) makes the focused pane fill the screen, as in
  lazygit, and `-` (or `_`) goes back. In a console only the editor or the
  results is shown. Full screen follows the focus and stays on while you
  switch tabs.

## [2.0.1] - 2026-10-17

### Fixed
- An unreachable Redis server no longer garbles the screen: go-redis's own
  messages (such as `failed to dial after 5 attempts`) now go to the log file
  instead of the terminal.

## [2.0.0] - 2026-10-17

### Added
- Redis connections: `driver = "redis"`, port 6379, `database` is
  the db number (0 when empty), password optional (sqlvim connects without one and asks only
  when the server says `NOAUTH`), SSH tunnels, and `params` `tls` / `tls_skip_verify`. The tree
  lists the dbs with their key counts, and expanding a db scans its keys in batches with `SCAN`
  (never `KEYS`), grouped into folders by `params.separator` (default `:`), with a type badge
  per key. A load stops after 10,000 keys (`↓ load more` continues, up to 100,000 per db),
  `Ctrl-c` stops a scan, and prod connections pause between batches. `F` filters the keys with a
  pattern (`*` `?` `[` as in `SCAN MATCH`, plain text matches anywhere; a filter stops at 500
  matches or 100,000 keys scanned), `fx` clears it, and `<Space>k` opens a key by name. The key
  tab shows type, TTL, length, size and encoding, a string (the first 1 MB of a bigger one; `v`
  cycles auto / text / JSON / hex) or the fields, items or members of a hash, list, set, sorted
  set or stream page by page (`]` loads more, `F` filters hash fields and set members). Binary
  key names are shown as `\xNN` and `yy` copies a key name as redis-cli takes it. Structure,
  compare, import and the other SQL features say "not supported for redis yet".
- Redis command console: `<Space>c` on a Redis connection, db, folder or key opens
  `redis:<conn>/db<N>`, one command a line with redis-cli quoting (`"…"` with `\n` `\xNN`
  escapes, `'…'`), `#` / `//` comment lines, and the SQL console's keys (`Enter` runs the line
  or the selected lines, `R` all, `<Space>R` from the cursor; a failed command stops the batch).
  Replies show as a value (JSON indented, binary as hex), a table (arrays; `HGETALL`,
  `CONFIG GET`, `ZRANGE … WITHSCORES` as field / value or member / score, also over RESP2; `SCAN`
  replies as a table of the keys with the cursor in the message) or redis-cli's indented text.
  The console has its own connection, so `SELECT` (the title follows the db) and
  `MULTI` / `WATCH` stick; `TX n` / `WATCH` show in the status bar and closing or quitting asks
  first. `Ctrl-c` cancels a blocking command (`BLPOP q 0`); the next command reconnects on the
  same db (`reconnected (db N)`), except inside `MULTI` / `WATCH`, where the transaction is
  reported lost and nothing reconnects until you run again. Completion of commands,
  subcommands and keys (the tree's scanned keys, plus a small `SCAN MATCH prefix*`), the
  command's arguments as a hint, highlighting, drafts and history (`AUTH` passwords are shown
  as `***`). `g d` on a result cell opens that key. Safety: read-only connections refuse write,
  dangerous and unknown commands; prod asks `y` for writes and unknown commands; dangerous
  commands (`FLUSHDB`, `CONFIG SET`, `CLIENT KILL`, `DEBUG` …) always ask, and on prod want the
  connection name; `KEYS` is refused on a db of more than 10,000 keys; a batch is checked as a
  whole, so `MULTI` does not get around it, and `SELECT` inside `MULTI` is refused.
  `SUBSCRIBE`, `PSUBSCRIBE`, `SSUBSCRIBE`, `MONITOR`, `SYNC`, `PSYNC`, `CLIENT TRACKING`,
  `CLIENT REPLY`, `RESET`, `HELLO` and `QUIT` are not supported.
- Redis key editing. The key tab stages changes like table data: `i` / `Enter` edits a cell,
  `o` adds a row, `dd` deletes one, `u` / `U` undo, and `<Space>w` previews the checks and
  commands, then submits. A string is edited in the cell editor, or in `$EDITOR` with
  `<Space>v` (long values, values with newlines; JSON gets a `.json` file); a hash can change
  values, add, delete and rename fields; a list can change and delete elements (deleting
  removes exactly that position, even among equal values) and push at the `head` or `tail`; a
  set and a sorted set can add, delete and rename members (and change scores); a stream can add
  and delete entries. A submit `WATCH`es the key and first compares the values the changes
  depend on with what was loaded: when anything differs, or the key is written meanwhile,
  nothing is written (`key changed since loaded; refresh and retry`). A command failing inside
  `EXEC` is reported (`command N of M failed: … · the others were applied`), as Redis does not
  roll back. `t` sets or removes the TTL, `r` renames (`RENAMENX`; an existing target asks
  before it is overwritten), `D` (or `dd` on a key in the tree) deletes with `UNLINK`, `o` in
  the tree creates a key (pick the type, the name and the first values), and `dd` on a folder
  or db deletes the keys matching its prefix or filter: counted first, previewed with the first
  20 keys, unlinked 500 at a time, and stopped with `Ctrl-c`. The node menu (`a`) now works on
  Redis nodes. Read-only connections refuse every edit; prod asks `y` for TTL, rename and new
  keys and wants the connection name for deleting, the submit preview is titled `PROD · …`. A
  string loaded only in part (over 1 MB) and binary values are not edited in place (use the
  console); Redis has no NULL. On Redis 5, changing a string keeps its TTL.
- Redis server tab and tools. `<Space>i` on a Redis connection (or the node menu's
  `Server status`) opens `<conn> · server`: version, role and replicas, uptime, clients, keys,
  memory / peak / `maxmemory` and policy, hit rate, evicted and expired keys, and sparklines of
  ops/sec, memory and network in / out over the last 5 minutes, sampled every 2 seconds and
  only while the status is on screen. `[` / `]` switch to the slow log (`SLOWLOG GET 128`;
  passwords in `AUTH`, `CONFIG SET requirepass` and the like show as `***`) and the client list
  (`CLIENT LIST`), where `dd` kills a client (`CLIENT KILL ID`, confirmed like a dangerous
  command; refused on read-only connections). A part the server's ACL refuses shows
  `permission denied` and the others still work. `*` stars a key (in the tree or its tab, shown
  as `★`) and `<Space>F` lists the starred keys; opened keys join `<Space>f`, binary names
  included. `<Space>x…` on a key tab exports its value: the loaded rows, or, when there are more,
  the whole key read 1,000 at a time; binary values are written as `\xNN` and counted in the
  status. `B` on a db or folder analyzes the biggest keys (like `redis-cli --bigkeys`): `SCAN` in
  batches of 1,000 with a pause between them, the lengths (optionally `MEMORY USAGE`) in one
  pipeline per batch, the 20 biggest keys of each type with a summary, progress, `Ctrl-c` to
  stop and `<Space>r` to run it again; prod asks first, and `Enter` opens a key.
- SQL Server support (2014–2022, user name and password, SSH tunnels): browse databases,
  schemas, tables, views and routines, run T-SQL in the console (`GO` separates batches,
  `BEGIN TRAN` / `COMMIT` / `ROLLBACK` are tracked), filter, sort and edit table data (`N'…'`
  literals; tables without a key are matched by all their values with `TOP (1)`), view
  structure and DDL, export, rename, truncate and drop tables. The connection form has
  `Encrypt` and `Trust cert` fields for older servers. Read-only SQL Server connections are
  guarded by sqlvim alone and more strictly: every statement of a batch is checked, and
  `EXEC` of anything but a few read-only system procedures is blocked. Modify Table, compare
  and import say "not supported for sqlserver yet".
- Oracle support (12c and later, user name and password, service name, SSH tunnels; pure Go, no
  Instant Client): schemas right under the connection, tables, views, functions, procedures and
  packages, a console that splits PL/SQL blocks at a `/` line and keeps a transaction open after
  `SET TRANSACTION` or `<Space>t`, data editing (tables without a key are matched by `ROWID`),
  structure and DDL (`DBMS_METADATA`), export, rename, truncate and drop. Read-only Oracle
  connections are guarded by sqlvim alone: PL/SQL blocks, `CALL` and `EXEC` are blocked.
- Typing a space in the console opens what usually comes next, with nothing selected so that
  `Enter` still types a newline (`Tab` or `↓` picks): `*` and the columns after `SELECT`,
  `FROM` after the select list, `WHERE` / `JOIN` / `ORDER BY` … after a table, `ON` after a
  joined table, operators after a column in a condition and `AND` / `OR` after it. While a
  word is typed the words that fit there come before other keywords (`from users or` offers
  `ORDER BY`, not `OR`).
- `<Space><Space>` in the console's VISUAL mode starts the search with the selected text
  (on one line).
- `<Space>s` in a console lists the columns of the result on screen, filtered by typing like a
  table's; `Enter` moves the results cursor to the column. When the result comes from one
  table the popup shows its key, types, NULLs, defaults and comments, and `Ctrl-o` opens its
  structure.
- `Ctrl-a` / `Ctrl-e` in the console's INSERT mode move to the start / end of the line.
- Import data from CSV, TSV or XLSX files, like DataGrip's Import Data from File: `I` on a
  table (or "Import data from file…" in its `a` menu) opens an Import tab with the file's
  settings (delimiter, UTF-8 or GBK, header row, sheet, NULL text), the column mapping (`x`
  imports or skips a column, `i` picks the target column) and a preview of the converted
  rows. `<Space>w` checks the whole file first and lists the rows that cannot be imported,
  then inserts everything in one transaction: any failure, `Ctrl-c` or a lost connection
  writes nothing, and a rejected row is named by its line. On a database or schema `I`
  creates a new table with column types inferred from the file (`t` changes a type).
  Read-only connections refuse; prod asks for the table name. When the file has no value
  for the table's integer primary key (and it is not auto-increment), the import numbers
  the rows from `MAX(id)+1` inside the transaction, with the table locked; `x` turns it off.
- Open `.sql` files in a console: `:e <file>` opens one on the connection and database in
  context (`~` and relative paths work; files up to 2 MB). It runs like any console, the tab
  shows `[+]` while it has unsaved changes, and `:w` saves it (`:w <file>` saves as, `:wq`
  saves and closes, `:w!` overwrites a file changed on disk). In a file console `:w` only
  saves the file; staged result edits are submitted with `<Space>w`. `:w <file>` in a plain
  console turns it into a file console. Closing, quitting or editing the connection never
  drops unsaved text silently. Files appear in `<Space>f`. A leading UTF-8 BOM is dropped.
- `<Space>R` in the console runs from the statement under the cursor to the end. When one of
  several statements fails, the status names it (`statement 2/3 failed`) and the cursor
  moves to it.
- Workspaces keep sets of connections apart, like DataGrip's: `sqlvim -w personal` (or
  `SQLVIM_WORKSPACE=personal`) opens a workspace with its own connections, keychain
  passwords, history and drafts, while `config.toml` and `keymap.toml` stay shared. Create
  one with `--create`, list them with `--list-workspaces`. Without `-w` nothing changes.
- The tree draws Nerd Font icons like DataGrip: the MySQL or PostgreSQL logo before each
  connection, and icons for databases, schemas, tables, views, folders, columns, keys and
  indexes. Groups show how many entries they hold (`Columns 15`). Set `icons = "plain"` in
  `config.toml` for a terminal font without Nerd Font glyphs.
- Tables and views expand in the tree into Columns, Keys, Foreign Keys and Indexes, each
  with its type or columns. `Enter` on a column, key or index opens the structure at it, `e`
  modifies the table there and `dd` stages the drop in Modify Table (still submitted with
  `<Space>w`); `Enter` on a foreign key goes to the referenced table.
- Vim fold keys in the tree: `zo` `zc` `za`, `zO` / `zC` for the subtree under the cursor,
  `zR` / `zM` for the whole tree and `zx` to keep only the path to the cursor. `zO` and `zR`
  open what is loaded already and never query the server.
- Console results can be filtered and edited like table data. `f` and `F` narrow any SELECT
  result by running it again on the server: a single-table query gets the condition in its
  WHERE, any other query is wrapped in a subquery. A single-table SELECT of plain columns
  that includes the table's key can be edited with `i`, `o`, `dd`, `u`, `U` and submitted
  with `:w`. Changes join the console's open transaction behind a savepoint, so a failed
  submit keeps the transaction and `:rollback` undoes them.
- A terminal paste (Cmd-V / Ctrl-Shift-V) works in every mode of the console editor and the
  cell editor, like Neovim: NORMAL mode puts the text after the cursor and VISUAL mode replaces
  the selection. It used to be ignored outside INSERT mode. The registers are left alone.
- A plain `y` in the console editor and the cell editor copies to the system clipboard as well
  as the unnamed register (vim's `clipboard=unnamedplus`, for yanks only: `x`, `d` and `c` do
  not overwrite the clipboard). `"+p` still pastes from it.
- `yy` in the tree copies the name of the selected connection, database, schema, table or view,
  and `yq` its qualified name as SQL writes it (`app.users` on MySQL, `public.users` on
  PostgreSQL, quoted when needed). Both are in the `a` menu too. Rebind them as `yank.name` /
  `yank.qualified`.
- `yy` in a structure tab copies the name of the column, index or foreign key under the cursor,
  or the whole DDL on the DDL page.
- `<Space>l` (or `:locate`) selects what the current tab shows in the tree: its table, or the
  database / schema of a console. It expands and loads the tree on the way and shows a hidden
  tree. Rebind it as `tree.locate`.
- `Tab` / `Shift-Tab` in the search switch its scope between objects, the lines of the open
  consoles (`Enter` jumps to the line) and the SQL history (an empty query lists the recent
  statements, `Enter` inserts one like `<Space>h`). A leading `@` narrows every scope to the
  connection in context. Rebind them as `search.scope.next` / `search.scope.prev` in `[popup]`.
- The search takes dotted queries matched by segment: `app.users.email`, `ap.usr.eml`,
  `shop.orders.total` (the schema left out) or `users.` (the columns of users). The last
  segment matches the name, the others its database, schema and table.
- `<Space>yd` copies the DDL of the table under the cursor in the tree, a data tab or a
  structure tab, without opening anything.
- `<Space>S` opens the full structure of the table under the cursor, skipping the columns popup.
- `y` in a popup that shows a value in full (a cell, a routine's definition) copies the text.
- A `[popup]` table in `keymap.toml` rebinds the keys popups read themselves: `structure.full`
  in the columns popup and the search, `form.save` in the connection form.
- `dd` on a table or view in the tree drops it once you type its name, and `r` renames it;
  open tabs of the table close or follow the new name. Read-only connections, a console with
  an open transaction and unsaved edits of the table block both.
- `a` in the tree opens a menu of what you can do with the selected node. An entry's key, the
  one it has in the tree (`e`, `dd`, `T`, `yy`, `<Space>c` …), runs it right away; `j` / `k`
  and `Enter` pick one too.
- `T` in the tree truncates the selected table (type its name to confirm); rebind
  `table.truncate` in `keymap.toml` to change it.
- Schema compare: `m` in the tree marks a table, a MySQL database or a PostgreSQL schema as the
  source and `C` compares it with the selected node of the same kind, on the same connection or
  another one of the same engine. The diff tab lists the differing columns, indexes, comments
  and options (foreign keys are shown, not synced); `x` picks what to sync (deletions start
  unpicked), `~` swaps the sides and `<Space>w` writes the script into a new console on the
  target, where the usual read-only and prod checks apply. Changes sqlvim cannot script stay in
  it as comments.
- Modify Table: `e` on a table in the tree, or `e` in its structure tab, edits the columns
  (name, type, NULL, default, comment) and indexes (name, kind, columns) with the data grid's
  keys: `i` edits the field under the cursor (`h` / `l` move between fields), `o` adds a column
  or index, `dd` deletes, `u` / `U` undo. `<Space>w` (or `:w`) previews the ALTER with the
  destructive and lossy changes in red and runs it — PostgreSQL in one transaction — once you
  press `y`, or type the table's name on a prod connection. A failed or cancelled (`Ctrl-c`)
  submit keeps the edits. Foreign keys, column order, generated and auto-increment columns are
  left to a console.
- Data compare: `D` in a diff tab compares the rows of the two tables (or of the `~` table under
  the cursor) by their primary key, or a unique NOT NULL index both have. It lists the rows only
  the source has (`+`), only the target has (`-`) and those whose values differ (`~`, shown as
  target → source). `F` sets a WHERE both sides are read with, `x` picks rows (deletions start
  unpicked), `~` swaps the sides, `h` / `l` scroll the columns, and `<Space>w` writes the
  INSERT / UPDATE / DELETE script into a new console on the target. Each side reads at most
  `compare_max_rows` rows (`config.toml`, default 100000); `D` goes back to the structure.

### Changed
- Help (`?`) and the leader's which-key list only the keys for the kind of connection in
  view: on a Redis connection, db, key or tab they leave out tables, structure, DDL and manual
  commit; on a SQL one they leave out the Redis keys, favorites and server status. Shared keys
  such as `dd`, `r` and `<Space>h` are described for that kind. The keys themselves still work
  the same everywhere.
- The tree's actions menu (`a`) on a Redis connection, db, folder, key or "load more" line
  lists everything that applies there: opening a key by name, starred keys, the key filter (and
  clearing it while one is set), new key, big-key analysis, console, server status, refresh and
  the rest.
- A console opens in INSERT mode, ready to type; `Esc` goes to NORMAL mode for the vim keys,
  the leader and tab switching.
- In the tree, `l` and `→` on a table or view expand it; `Enter` opens its data. Rebind
  `tree.open` in `keymap.toml` to change it.
- `<Space>e` toggles the tree (was `<Space>b`) and `<Space>f` opens the recent tabs (was
  `<Space>e`). Remap them in `keymap.toml` as `tree.toggle` / `recent.open` to keep the old keys.
- `Ctrl-H` / `Ctrl-L` wrap: `Ctrl-H` on the tree moves to the tabs and `Ctrl-L` on the tabs
  moves back to the tree, like `Tab` / `Shift-Tab`.
- In a console, `Ctrl-J` / `Ctrl-K` wrap too: `Ctrl-K` on the editor moves to the results and
  `Ctrl-J` on the results moves back to the editor.
- `<Space><Space>` searches every connection by default and starts loading the metadata of
  those not loaded yet when it opens; a leading `@` now narrows the search to the connection
  in context (it used to be the other way round).
- The columns popup and the search open the structure with `Ctrl-o` instead of `Ctrl-s`,
  which is a common tmux prefix (and terminal XOFF) and never reached sqlvim there. Their
  footers name the key you bound.
- The action ID `conn.delete` is now `node.delete`; `keymap.toml` files that use the old ID
  keep working.
- The action ID `conn.edit` is now `node.edit` (`e` in the tree edits a connection or modifies
  a table); `keymap.toml` files that use the old ID keep working.
- The structure tab has a cursor: `j` / `k` move over the rows of columns, indexes and foreign
  keys, and a hint line at the bottom.

### Fixed
- Editing a connection in the form (`e` in the tree) no longer drops its `params` and
  `readonly` settings from `connections.toml`.
- The search (`<Space><Space>`), the columns popup and the pickers draw the cursor where it is:
  after `Ctrl-a`, `←` or `Alt-b` it used to stay drawn at the end of the text.
- The MySQL driver's own messages (such as `unexpected EOF` when a server cannot be reached)
  go to the log file instead of being printed over the screen.
- A MySQL query that stops early (a console reaching `max_rows`, a data compare over
  `compare_max_rows`) no longer reads the rest of the result before returning; on PostgreSQL
  the data compare stops at once too.

## [1.1.0] - 2026-10-14

### Changed
- `<Space>s` opens a popup of the table's columns (key, type, NULL, default, comment) instead
  of a structure tab. Type to filter by name or comment; `Enter` puts the data tab's cursor on
  the column, opening the table's data first when needed; `Ctrl-s` opens the full structure.
- An `@` search loads the metadata of every connection that was not loaded yet, so it finds
  tables in connections you have not opened; the popup counts the connections still loading.

### Fixed
- Hints no longer end with a dangling ` · ` when `keymap.toml` unbinds the keys they name.
- `keymap.toml` key sequences like `"f<a"` mean `f`, `<`, `a`; only `<` followed by a name of
  two or more letters (`<ctrl+d>`) is read as a named key.

## [1.0.0] - 2026-10-14

### Added
- `~/.config/sqlvim/keymap.toml`: change the leader and bind, rebind or unbind keys per
  context; conflicts and unknown actions are reported at startup with the file and key.
- `[theme]` in `config.toml`: override the colors of 14 slots with ANSI indexes or `#rrggbb`.
- Hints, the welcome screen and the status bar name the keys you actually bound.
- Releases for macOS and Linux (amd64, arm64) and a Homebrew cask:
  `brew install shepherd-yun/tap/sqlvim`.
- README in Chinese, a demo GIF, CONTRIBUTING and issue / pull request templates.
- `H` / `L` switch to the previous / next tab, wrapping around, from any pane or tab.
- `f` is a quick filter menu for the current column: `ff` equals, `f!` not equal, `fs` / `f~`
  contains, `fl` LIKE a typed pattern (`%` and `_` are wildcards), `f>` / `f<` greater / less
  than, `fn` / `fN` NULL / not NULL, `fx` clears.
- `F` starts from the cursor's column, completes column names and keywords with `Tab`, keeps
  the prompt open on a malformed filter, and brings back a filter the server rejected.
- `Ctrl-j` / `Ctrl-k` move down / up in every list popup (pickers, search, completion), like
  `Ctrl-n` / `Ctrl-p`.
- Text inputs (prompts, `:` and `/`, the connection form, passwords) move the cursor with
  readline keys: `←` `→`, `Home` `End`, `Ctrl-a` `Ctrl-e`, `Alt-b` `Alt-f`, `Ctrl-w`, `Ctrl-k`.

### Changed
- Console hints write keys the way help does (`<enter> run`, `<ctrl+j> results`).
- `f` alone no longer filters; `ff` filters by the cell's value.
- `Ctrl-u` in a text input deletes to the start of the line instead of clearing it.
- The console frames its editor and its results separately; `Ctrl-j` / `Ctrl-k` light the
  focused one's border, the other's cursor turns into an underline, and the hint line follows
  the focus. A prod tab's unfocused frames are faint red.

### Fixed
- `gt` / `gT` switch tabs in table data, structure and console result tabs and in the tree,
  not only in the console editor, and wrap around like `H` / `L`.

## [0.6.0] - 2026-10-14

### Added
- Edit table data in place: `i` / `Enter` edit a cell in the vim editor (`Ctrl-n` sets NULL),
  `o` adds a row, `dd` marks a row for deletion, `u` / `U` undo one / every change.
- Staged changes are colored and submitted with `<Space>w` or `:w` after a SQL preview, in one
  transaction that rolls back unless every statement changes exactly one row.
- Rows without a primary key or unique NOT NULL index are located by all their values
  (MySQL, `LIMIT 1`) or by `ctid` (PostgreSQL).
- `f` filters table data by the cell's value, `F` edits the `WHERE` condition, `s` sorts by the
  column; whole-table exports keep the filter and order.
- `:q`, `:qa` and editing or deleting a connection ask or refuse while table changes are unsaved.
- `zH` / `zL` scroll a grid one column sideways.

### Changed
- `zh` / `zl` scroll a grid half a screen sideways instead of one column.
- MySQL reports the rows an `UPDATE` matched rather than the rows it changed, like PostgreSQL.

## [0.5.0] - 2026-10-14

### Added
- A built-in vim editor in the console: NORMAL, INSERT, VISUAL and V-LINE modes, motions
  (`w b e W B E 0 ^ $ gg G f F t T % { }`), operators `d c y > <` with motions, text
  objects and counts, `x p P o O D C I A`, undo / redo, `.` repeat and registers
  `"a`–`"z`; `"+` reads and writes the system clipboard (pbcopy / pbpaste locally,
  OSC 52 over SSH).
- `Enter` in VISUAL mode runs the selected statements.
- `gt` / `gT` / `{n}gt` switch tabs from the console editor.
- The console status line shows the command being typed (`"a2d`), and `?` lists the
  editor's keys.

### Changed
- In the console editor the digits `1`–`9` are counts; they still go to a tab everywhere else.
- Text from `<Space>v` and from SQL history can be undone with `u`.
- The Bubbles dependency is gone.

## [0.4.0] - 2026-10-14

### Added
- `<Space><Space>` searches tables, views, columns, routines, schemas and databases of the
  connection in context (`@` for every connection); `Enter` opens data, `Ctrl-s` the structure.
- `<Space>e` lists recently opened tables, structures and consoles, kept across restarts.
- Console completion for tables, columns, aliases and keywords, with quoting where needed
  (every reserved word of the dialect); `↑`/`↓` pick a candidate, `Enter` accepts it.
- Tabs and console runs on a connection whose password was not entered yet ask for it and
  try again once it is entered.
- Syntax highlighting in the console editor and in the structure view's DDL.
- The console cursor jumps to the position of a syntax error reported by the server.
- Catalogs load in the background; `<Space>r` and DDL in a console reload them.
- Long consoles stay responsive: highlighting is cached per statement and edits no longer
  re-render the whole text.
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

[Unreleased]: https://github.com/shepherd-yun/sqlvim/compare/v2.4.0...HEAD
[2.4.0]: https://github.com/shepherd-yun/sqlvim/compare/v2.3.0...v2.4.0
[2.3.0]: https://github.com/shepherd-yun/sqlvim/compare/v2.2.0...v2.3.0
[2.2.0]: https://github.com/shepherd-yun/sqlvim/compare/v2.1.1...v2.2.0
[2.1.1]: https://github.com/shepherd-yun/sqlvim/compare/v2.1.0...v2.1.1
[2.1.0]: https://github.com/shepherd-yun/sqlvim/compare/v2.0.3...v2.1.0
[2.0.3]: https://github.com/shepherd-yun/sqlvim/compare/v2.0.2...v2.0.3
[2.0.2]: https://github.com/shepherd-yun/sqlvim/compare/v2.0.1...v2.0.2
[2.0.1]: https://github.com/shepherd-yun/sqlvim/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/shepherd-yun/sqlvim/compare/v1.1.0...v2.0.0
[1.1.0]: https://github.com/shepherd-yun/sqlvim/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/shepherd-yun/sqlvim/releases/tag/v1.0.0
