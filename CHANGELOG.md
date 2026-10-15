# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
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
- `a` in the tree opens a menu of what you can do with the selected node and runs the entry
  you pick. Truncating a table lives there (bind `table.truncate` in `keymap.toml` for a key).
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

[Unreleased]: https://github.com/shepherd-yun/sqlvim/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/shepherd-yun/sqlvim/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/shepherd-yun/sqlvim/releases/tag/v1.0.0
