# datavim

A terminal SQL client for MySQL, PostgreSQL, SQL Server and Oracle with vim keys and DataGrip-style workflows. It also browses and queries Redis, Kafka and Elasticsearch.

[中文说明](README.zh-CN.md)

![datavim demo](docs/demo.gif)

- Browse databases, filter and sort table data, and **edit it in place**: changes are staged,
  shown as SQL and submitted in one transaction. Expand a table in the tree to see its columns,
  keys, foreign keys and indexes.
- A highlighted SQL console with a **built-in vim editor**, completion and `$EDITOR` hand-off.
- `<Space><Space>` searches every table, view, column and routine; `<Space>f` reopens recent tabs.
- Copy and export results (TSV, CSV, JSON, Markdown, INSERT, xlsx), SQL history.
- Import CSV, TSV and xlsx files into a table, or into a new table inferred from the file.
- Kafka: topics, consumer groups by lag, messages with jumps, filters and live tail, offset resets
  and a producer console; topic configs and partitions, deleting messages and groups, a
  Schema Registry (Avro, Protobuf and JSON Schema), ACLs, Kafka Connect and masking of message fields.
- Elasticsearch and OpenSearch: indices, aliases and data streams, documents found by `_id` or a
  query string with hits highlighted, edited with concurrency checks, delete by query, index
  management, update by query and reindex, index templates, ILM policies and ingest pipelines
  (with `_simulate`), snapshots and restores, a REST console with completion and table views, a
  cluster tab with nodes, shards (why one is unassigned), tasks, settings, users and roles, and
  masking of document fields.
- An audit log of every write datavim sent to SQL, Redis, Kafka and Elasticsearch servers (`<Space>a`).
- SSH tunnels (agent, keys, ProxyJump), production safeguards, manual transactions.
- Your own keys (`keymap.toml`) and colors (`[theme]`).

## Install

```sh
brew install shepherd-yun/tap/datavim
```

or with Go (the version in `go.mod`):

```sh
go install github.com/shepherd-yun/datavim/cmd/datavim@latest
```

Binaries for macOS and Linux are on the [releases page](https://github.com/shepherd-yun/datavim/releases).

## Configure

Create `~/.config/datavim/connections.toml` — see [docs/examples/connections.toml](docs/examples/connections.toml).
On first connect datavim asks for the password and can save it to the macOS Keychain.

Optional `~/.config/datavim/config.toml` (see [docs/examples/config.toml](docs/examples/config.toml)):

```toml
page_size        = 200
max_column_width = 40
max_rows         = 1000          # rows fetched per console query
compare_max_rows = 100000        # rows a data compare reads per side
editor           = "nvim"        # <space>v; default $VISUAL, $EDITOR, nvim
export_dir       = "~/Downloads"
icons            = "nerd"        # tree icons: "nerd" (needs a Nerd Font) or "plain"
```

The tree draws [Nerd Font](https://www.nerdfonts.com) icons (v3): the MySQL / PostgreSQL / Redis / Kafka logo
before each connection, and database, schema, table, folder and key icons below it. Without a
Nerd Font they show as boxes: set `icons = "plain"`.

No config yet? Start datavim and press `<space>n` to add a connection.

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
password is optional: datavim connects without one and asks only when the server answers
`NOAUTH`. `params`: `separator` groups the keys into folders (default `:`), `tls = "true"` and
`tls_skip_verify = "true"`. Expanding a db scans its keys with `SCAN` in batches of 1,000 (never
`KEYS`): a load stops after 10,000 keys, `↓ load more` goes on (at most 100,000 keys per db),
`Ctrl-c` stops it, and on prod connections datavim pauses between batches. `F` on a db, folder or
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
on; a failed command stops the rest. A reply shows as a value (JSON indented, fastjson's `28L` / `Set[…]` too; binary as hex; long
lines wrap; `yy` copies it as stored), a
table (arrays, field / value pairs of `HGETALL` or `CONFIG GET`, member / score pairs of
`ZRANGE … WITHSCORES`, the keys of a `SCAN`) or redis-cli's indented text; each command of a run gets a result
tab like in the SQL console and `g d` on a table cell opens that key. The console keeps its own connection, so `SELECT`
(the tab title shows the db) and `MULTI` / `WATCH` last; the status bar shows `TX n` or `WATCH`,
and closing the tab or quitting asks first. `Ctrl-c` cancels a blocking command such as
`BLPOP q 0`; that drops the connection, and the next command reconnects on the same db. Inside
`MULTI` or `WATCH` nothing reconnects: datavim says the transaction is lost. Typing completes
commands, subcommands and keys (the keys the tree scanned, plus a small `SCAN` for the prefix)
and shows the command's arguments. Known limits: other connection state (`CLIENT SETNAME` …) is
not restored after a reconnect, `SELECT` inside `MULTI` is refused, `:e` / `:w` do not work in a
Redis console, and `SUBSCRIBE`, `MONITOR`, `HELLO`, `RESET`, `CLIENT TRACKING` and the like are
not supported. Structure, compare and import are not available for Redis.

Redis keys are edited in their tab the way table data is: `i` / `Enter` edits a cell (a string
value: the cell editor, or `$EDITOR` for long values and values with newlines; `<Space>v` always
opens `$EDITOR`), `o` adds a field, element (`head` or `tail` in the index column says where it
goes), member or stream entry, `dd` deletes one, `u` / `U` undo, and `<Space>w` shows the checks
and commands before it submits. datavim `WATCH`es the key and checks that every value the
changes rely on (a field's value, the element at an index, a member's score) is still what was
loaded; if not, or if the key is written before `EXEC`, nothing is written. Redis does not roll
back: when a command fails inside `EXEC`, the ones before and after it are still applied, and
datavim says which one failed. Strings over 1 MB (only partly loaded) and binary values are not
edited in place; use the console. On a key tab `t` sets or removes the TTL, `r` renames
(`RENAMENX`, confirming before it overwrites an existing key) and `D` deletes (`UNLINK`); in the
tree `r` / `dd` on a key do the same, `o` creates a key, and `dd` on a folder or db deletes the
keys matching it (the prefix, or the db's filter): datavim counts them, lists the first 20 and
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

**Kafka** (Apache Kafka 2.8 to 4.x, Confluent, MSK, Redpanda): `driver = "kafka"`, `host` is a
comma-separated list of bootstrap brokers (`b1:9092,b2:9092`; port 9092 by default) and `database`
is not used:

```toml
[[connection]]
name   = "events"
driver = "kafka"
host   = "b1.internal:9092,b2.internal:9092"
user   = "reader"
env    = "prod"
params = { sasl = "scram-sha-512", tls = "true" }
```

`params`: `sasl` (`plain`, `scram-sha-256` or `scram-sha-512`; the password is asked for and
kept in the keychain like any other), `tls = "true"`, `tls_skip_verify`, `tls_ca`, `tls_cert` and
`tls_key` (PEM files, for mTLS), `client_id` (default `datavim`) and `show_internal = "true"`. The
form has `SASL` and `TLS` fields. Through an SSH tunnel every broker connection goes through the
bastion, including the brokers' advertised addresses, so the bastion must reach them.
`broker_map = "b-1.internal:9092=localhost:19092, …"` dials another address for an advertised
broker (port forwards, NAT).

`sasl = "oauth"` logs in with an OAuth client-credentials token: `oauth_token_url` and
`oauth_client_id` are required, `oauth_scope` is optional, and the client secret is asked for
the first time it is needed. `sasl = "aws-msk-iam"` signs in to MSK with IAM (`aws_region`,
`aws_profile`, TLS on by default): the credentials come from AWS's own chain (environment,
`~/.aws`, SSO, instance role) and datavim never stores AWS keys.

`schema_registry_url = "https://sr.internal:8081"` adds a Schema Registry (the Confluent API;
Redpanda's works too), with `schema_registry_user` for basic auth; its HTTP requests go through
the same SSH tunnel and use the TLS params (`tls_ca`, `tls_cert` / `tls_key`, `tls_skip_verify`). The registry password is asked for
when the registry first wants one. Secrets other than the connection password live in the
keychain as `<connection>#schema-registry` and `<connection>#oauth`, or come from
`DATAVIM_PASSWORD_<NAME>_SCHEMA_REGISTRY` / `DATAVIM_PASSWORD_<NAME>_OAUTH`.

`connect_url = "main=http://connect-1:8083, cdc=https://connect-2:8083"` adds Kafka Connect
clusters (one bare URL is the cluster `connect`), with `connect_user` for basic auth on all of
them; the password is asked for when a cluster first wants it and kept as
`<connection>#connect` (`DATAVIM_PASSWORD_<NAME>_CONNECT`). Their HTTP requests go through the
SSH tunnel too. The tree's Kafka Connect section lists the connectors (under each cluster when
there are several) with their state and running tasks, failed ones in red; `Enter` opens a
connector: Status, Tasks (`K` shows a task's trace) and Config, with passwords, secrets, tokens
and keys shown as `******`. `p` pauses or resumes it, `R` restarts it with its failed tasks (in
Tasks: the task under the cursor), and `<Space>v` edits the config in `$EDITOR` as JSON: the
cluster validates it first, the confirmation lists what changes, `******` keeps the old value,
and nothing is sent if someone changed the config meanwhile.

Expanding
the connection lists Brokers, Topics (folders by the name before the first `.` `-` `_`; `.`
shows the internal ones) and Consumer Groups, sorted by lag (`s` sorts by name), lagging groups
in red; `F` / `fx` filter topics or groups by name.

`Enter` on a topic opens its tab, `[` / `]` switch between Messages, Partitions, Config and
Consumers. Messages shows the newest 200 messages of all partitions by time (one partition: by
offset); moving past the first or last row loads 200 more per partition, and a view keeps at most
10,000. It reads `read_committed` (messages of aborted transactions are hidden); `I` switches to
`read_uncommitted`. Keys and values in the Schema Registry wire format are decoded to JSON; one
whose schema can't be read is shown as hex with the reason. `t` jumps: `latest`, `earliest`, `-500` (the last 500 of each partition), `p1:1180002`,
an offset of the current partition, `p1,p3` (only those partitions) or `@2026-10-20 09:12` /
`@09:12` / `@-15m`, with a preview as you type; an offset retention already deleted is said so
and offers the earliest one. `T` starts and pauses live tail (it stops while the tab is hidden).
`K` shows a message — key, headers and the value in auto / text / JSON / hex / base64 (`v`,
values over 1 MB keep their first 1 MB) — `yy` copies it as JSON (binary as base64), `yl` copies a
cell in full, `c` shows the headers column and `<Space>v` opens the value in `$EDITOR`. `F` filters
with `key = ord-1`, `value ~ sku`, `header.source = checkout`, `value.user.id = 42`, `partition`,
`offset`, `ts > 09:12`, `size > 1000`, joined with `and` / `or`; `ff` `f!` `fs` add the cell
under the cursor, `fx` clears; with a filter `n` / `N` search on past what is loaded (up to
100,000 messages, `Ctrl-c` stops). `<Space>xl` exports the messages as JSON Lines. `p` copies
the message under the cursor and `P` every shown message to a producer console (pick this or
another topic) so they can be sent again byte for byte; `X` deletes the messages up to the one
under the cursor.

In Partitions, `o` adds partitions and `X` deletes the messages before an offset. In Config, `i`
edits a value, `dd` goes back to the default, `u` / `U` undo, and `<Space>w` applies only the
changed configs (it gives up if someone else changed them since).

`Enter` on a group opens its lag (refreshed every 5 seconds while shown; `↑` marks a lag that
grew, `*` a partition never committed) and members. `gd` on a lag row opens the topic at the
group's committed offset — the message it is stuck on — and `gd` there goes back. `R` resets
offsets: this partition, the topic or every topic, to `next` (skip the stuck message),
`earliest`, `latest`, an offset, `@time` or `+N` / `-N`, with a preview of every partition. It
works only on an empty group (stop the consumers first) and gives up if the group changed since
the preview. `<Space>c` on a topic opens a producer console: each message is optional `key:`,
`partition:` and `header: name=value` lines, then the body; `key: null` is a null key, a body of
`null` a tombstone, and `===` separates messages. `key-encoding:` / `value-encoding: base64` (or
`hex`), `header-base64: name=<base64>` and `header-binary: <base64 name>,<base64 value>` (a
header name that isn't plain text or contains `=`) carry binary data, and `key-schema:` /
`value-schema: orders-value[@3][#shop.Order]` serialize the JSON body with that subject's
schema (the latest version by default; `#` picks a Protobuf message). `Enter` sends the message
under the cursor (or the selection), `R` all of them, and the result shows each partition and
offset. `:send orders.jsonl` sends a JSON Lines export after one confirmation (over 1,000
messages it shows the first five).

With a Schema Registry the tree also lists its subjects and their versions. `Enter` on one opens
a Schema tab: the versions (`Enter` shows one, `m` marks one and `C` diffs it with the marked or
the latest), `<Space>v` registers a new version written in `$EDITOR` after a compatibility
check, `c` sets the compatibility level and `dd` soft-deletes a version. `o` on Topics
creates a topic, `dd` on a topic deletes it (on an empty group: deletes the group; in a group's
lag `X` deletes its offsets on that topic), and `<Space>i` shows the cluster status (brokers,
under-replicated partitions).

`ACLs` under the connection lists the cluster's ACLs. `o` adds some (one per operation, after a
confirmation), `dd` deletes the one under the cursor once you type its principal (only that ACL:
every field has to match), and `gd` opens the topic or group a literal ACL is about.


**Elasticsearch** (7.10 to 9.x; OpenSearch 1.x / 2.x for the core features): `driver = "elasticsearch"`,
`host` is a comma-separated list of node URLs (`http://10.0.0.1:9200,10.0.0.2`; without a scheme
`params.tls` picks http or https, port 9200 by default) and `database` is not used. datavim talks
only to the addresses you list (no sniffing), trying the next one when a node cannot be reached.

```toml
# an intranet cluster without security
[[connection]]
name   = "es-dev"
driver = "elasticsearch"
host   = "172.16.96.161"

# Docker's ES 8.x: HTTPS and the elastic user (the password is asked for and kept in the keychain)
[[connection]]
name   = "es-local"
driver = "elasticsearch"
host   = "localhost"
user   = "elastic"
params = { tls = "true", tls_ca = "/path/to/http_ca.crt" }

# an API key instead of a password (asked for once, kept as <connection>#apikey)
[[connection]]
name   = "es-prod"
driver = "elasticsearch"
host   = "https://es-1.internal:9243,https://es-2.internal:9243"
env    = "prod"
params = { auth = "apikey" }
```

`params`: `tls = "true"`, `tls_skip_verify`, `tls_ca`, `tls_cert` / `tls_key` (PEM files),
`auth = "apikey"`, `path_prefix` (a cluster behind a proxy path), `show_system = "true"` (show
the `.` indices) and `page_size` (documents per page, 100). The form has `URLs`, `TLS` and `Auth`
fields. For a production cluster use an account with a read-only role.

The tree lists the cluster's version and health, then Indices (dated and rollover indices fold
into `logs-app-* (31)`, newest first; `.` shows the system indices, `s` sorts by name, documents or
size, `F` filters by name or `*` pattern), Aliases and Data Streams. `Enter` on one opens an Index
tab whose sections `[` / `]` switch: Documents, Mapping, Settings, Aliases and Stats.

Documents is a table of the hits, `_id` first (`_index` second on an alias or pattern), with up to
12 columns picked from the mapping, loading more as the cursor moves down (pages through a PIT, or
a scroll on 7.10 / 7.11 and OpenSearch, released when the tab closes). `F` takes a Lucene query
string (`status:paid AND user.name:"张三"`) or a `{…}` query DSL object, with field completion and
history; `ff` `f!` `fs` `f>` `f<` `fn` `fN` add the cell's value, `fx` clears. Hits are highlighted
(fields outside the columns show in a last `+` column), `s` sorts (a text field by its `.keyword`),
`K` shows the document (`za` folds, `n` / `N` move on), `yk` `ys` `yr` `yu` copy the `_id`, the
`_source`, console requests or curl (never a password or key), and `<Space>k` opens a document by
`_id`. `i` edits a field, `<Space>v` the whole `_source` in `$EDITOR`, `o` writes a new document,
`dd` marks one for deletion, `<Space>w` submits: every write carries `if_seq_no` /
`if_primary_term`, so a document changed since you read it is not overwritten. `X` deletes what
the filter matches (`_delete_by_query`, after a count). On the tree `o` creates an index, `dd`
deletes one (or a rollover group), and `a` opens, closes, refreshes, flushes, clears caches or
force-merges. Settings edit like a topic's configs; Aliases add (`o`) and remove (`dd`).

`<Space>c` opens the REST console: Kibana Dev Tools' format (`GET orders/_search` and a JSON body,
`"""` strings, `#` comments, NDJSON for `_bulk`), with completion of methods, indices, APIs,
query DSL keys and field names. `Enter` runs the request at the cursor, `R` all of them; each gets
a result tab with its status and time, as JSON or (`[` / `]`) as a table of the hits, a `_cat`
list or aggregation buckets (`gd` on an `_id` opens the document).

`<Space>i` on the connection opens the cluster tab, refreshed every 10 seconds while on screen;
`[` / `]` switch its sections. **Overview** has the version, health and totals (`Enter` on a
cluster that is not green opens Shards). **Nodes** lists heap, RAM, CPU, load, disk and shards,
marking a node at 85% heap or past the cluster's high disk watermark; `Enter` shows the node's
details. **Shards** lists the shards that are not started (`.` all of them, `F` filters by index
name or `*` pattern, `fx` clears); `Enter` on an unassigned one shows why (allocation explain).
**Allocation** is the disk of each node, **Tasks** the reindex, by-query, force merge, snapshot
and resize tasks with their progress (`dd` cancels one), **Pending** the queued cluster state
updates, and **Settings** the persistent and transient cluster settings (`.` shows the defaults;
`i` edits, `dd` removes, `<Space>w` sends them once you type the connection name).

The actions menu (`a`) of an index has `Reindex…`: destination, query, op type and conflicts;
a destination that does not exist can first be created with the source's mapping and settings.
`<Space>u` in Documents runs a painless script, written in `$EDITOR`, on every document the filter
matches (update by query, after a count); both run as tasks shown in Tasks. On Mapping, `o` adds
fields in `$EDITOR` (changing a field's type is refused: reindex instead), and `gd` on Stats opens
the index's shards. Index Templates in the tree lists the composable, legacy and component
templates: `Enter` shows one as JSON, `<Space>v` edits it in `$EDITOR`, `dd` deletes it.

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

## Server status

`<Space>i` on a MySQL, PostgreSQL, SQL Server or Oracle connection (or `Server status` in the
node menu of a connection or database) opens its server tab; `[` / `]` switch sections and
`<Space>r` reloads one.

- **Status** — version, uptime, role, connections against the limit, cache hit rate and the
  like, with sparklines of the load (QPS / TPS, running sessions, rows or network per second).
  It samples every 2 seconds, only while it is on screen.
- **Sessions** — who is connected, what they run and for how long, and whether they are in a
  transaction (idle sessions hidden; `a` shows them, `*` marks datavim's own). `c` cancels the
  session's query, `dd` disconnects it: refused on read-only connections, confirmed everywhere,
  with the connection name typed on prod; datavim checks that the id still names the same
  session first. SQL Server cannot cancel a query (only disconnect), nor can Oracle before 18c.
- **Locks** — who waits for a lock and who holds it, with both statements.
- **Slow queries** — the top statements by total time: `performance_schema` digests on MySQL,
  `pg_stat_statements` on PostgreSQL (when installed), `dm_exec_query_stats` on SQL Server,
  `v$sql` on Oracle.
- **Variables / Settings / Configuration / Parameters** — the server's settings; `F` filters by
  name.
- **Replication** — replicas and lag, PostgreSQL slots, availability groups, Data Guard.

A section the account may not read (MySQL `PROCESS`, SQL Server `VIEW SERVER STATE`, Oracle
`SELECT_CATALOG_ROLE`) says what is missing; the others still work. Passwords in the SQL shown
(`IDENTIFIED BY`, `PASSWORD`) are masked.

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

datavim reads `HostName`, `Port`, `User`, `IdentityFile`, `ProxyJump` (any number of hops)
and `UserKnownHostsFile`. It tries ssh-agent first, then your identity files, and asks
for a key's passphrase only when nothing else works (passphrases are kept in memory).
Unknown hosts show their fingerprint and are added to `known_hosts` once you type `yes`;
a changed host key is an error. `ProxyCommand` is not supported.

## Workspaces

A workspace is its own set of connections, say one for work and one for personal databases.
Each has its own `connections.toml`, keychain passwords, history and console drafts; settings,
theme and keys are shared.

```sh
datavim -w personal --create   # create it once
datavim -w personal            # or: DATAVIM_WORKSPACE=personal datavim
datavim --list-workspaces
```

Without `-w` datavim opens the `default` workspace, which is the files it has always used.
The workspace name shows in the status bar and the window title.

| | default | `personal` |
|---|---|---|
| connections | `~/.config/datavim/connections.toml` | `~/.config/datavim/workspaces/personal/connections.toml` |
| history, drafts | `~/.local/state/datavim/state.db` | `~/.local/state/datavim/workspaces/personal/state.db` |
| keychain account | `<connection>` | `personal/<connection>` |

To move connections into a workspace, create it, cut their `[[connection]]` blocks into its
`connections.toml`, and enter each password once more on first connect. Their history stays
in the default workspace.

With [direnv](https://direnv.net), `export DATAVIM_WORKSPACE=work` in a project's `.envrc`
picks the workspace by directory.

## Keys

| Key | Action |
|---|---|
| `j` `k` `h` `l`, `gg` `G`, `Ctrl-d` `Ctrl-u` | Move |
| `l` / `→` | Expand a node; a table or view shows its Columns, Keys, Foreign Keys and Indexes (retry a disconnected connection) |
| `Enter` | Open a table's data; on a column, key or index open the structure at it, on a foreign key go to the referenced table |
| `zo` `zc` `za` | Expand / collapse (on a column, its group) / toggle |
| `zO` `zC` | Expand everything loaded / collapse everything under the cursor |
| `zR` `zM` (or `Z`) `zx` | Expand everything loaded / collapse everything / collapse all but the path to the cursor |
| `h` | Collapse / go to parent |
| `Tab`, `Ctrl-h` `Ctrl-l` | Switch pane |
| `1`–`9` | Go to tab (in the console editor digits are counts: use `gt` / `gT` / `{n}gt`) |
| `H` `L`, `gT` `gt` | Previous / next tab, wrapping around (from any pane or tab) |
| `/` `n` `N` | Search in the tree or a result grid |
| `[` `]` | Previous / next page (data), section (structure); in console results, the statement's data / message |
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
| `Ctrl-j` / `Ctrl-k` | Console: results / editor |
| `H` / `L` | Console results: previous / next result tab — Summary (the message log), then one per statement of the last run |
| `}` | Console results: load more rows (also when the cursor reaches the last row of a truncated result) |
| `<Space>v` | Edit the console in `$EDITOR` |
| `<Space>h` | SQL history |
| `<Space>a` | Audit log of the writes datavim sent (`:audit <connection>` for one connection); `F` searches, `gd` opens the object |
| `<Space>y t/c/j/m/i` | Copy result as TSV / CSV / JSON / Markdown / INSERT; in a grid `y t/c/j/m/i` too |
| `<Space>yd` | Copy the DDL of the table under the cursor (tree, data or structure tab; there `yd` too); `y` in a routine's definition copies it |
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
| `+` / `=` | Full screen: the focused pane fills the screen (in a console, the editor or the results). It follows the focus and stays on while you switch tabs |
| `-` / `_` | Leave full screen |
| `<Space>l` | Locate the current tab in the tree: the table, or the database / schema of a console (also `:locate`) |
| `<Space>r` | Refresh the tree node or tab and reload the connection's metadata |
| `?` | Help for the focused pane: its own keys and the common global ones (`Tab` lists every global key) |
| `:q` / `:qa` | Close tab / quit |
| `:e <file>` | Open a `.sql` file in a console on the connection / database in context |
| `:w` / `:w <file>` / `:wq` | File console: save / save as (a plain console becomes a file console) / save and close; `:w!` overwrites a file changed on disk |
| `<Space>t` | Console: toggle manual commit (`TX n` in the status bar) |
| `:commit` / `:rollback` | Console: end the transaction |
| `:set noreadonly` / `:set readonly` | Unlock / lock the connection for this session |
| `:q!` / `:qa!` | Close / quit without asking about open transactions |
| `<Space><Space>` | Search tables, views, columns, routines, schemas and databases in every connection, loading the metadata of those not opened yet (`db.table.column`, `table.column` or any part of it narrows by parent, fuzzily, and opening a column puts the cursor on it; `@` searches only the connection in context; `Enter` opens, `Ctrl-o` opens the structure). `Tab` / `Shift-Tab` switch to the lines of the open consoles (`Enter` jumps to the line) or the SQL history (an empty query lists the recent statements, `Enter` inserts one as `<Space>h` does) |
| `<Space>f` | Recently opened tables, structures, consoles, files, Redis keys, Kafka topics and groups |
| `.` / `s` | Tree, on a Kafka connection: show / hide internal topics / sort consumer groups by lag or name |
| `t` / `T` / `c` | Kafka messages: jump to an offset, time or partition / live tail on, pause / headers column |
| `g d` | Kafka group tab: open the message the group is stuck on (in the messages, back to the group) |
| `R` | Kafka group tab: reset offsets |
| `<Space>x l` | Kafka messages: export as JSON Lines |
| `p` / `P` / `I` | Kafka messages: copy the message / every shown message to a producer console / read_committed or read_uncommitted |
| `X` | Kafka messages, partitions and group lag: delete messages up to this one / before an offset / the group's offsets on the topic |
| `o` | Kafka topic partitions: add partitions |
| `i` / `d d` / `<Space>w` | Kafka topic config: edit a value / back to the default / apply the changes |
| `m` / `C` / `c` | Kafka schema tab: mark a version / diff with the marked one or the latest / set the compatibility level |
| `<Space>v` / `d d` | Kafka schema tab: register a new version in `$EDITOR` / soft-delete the version |
| `:send <file>` | Kafka producer console: send a JSON Lines file |
| `o` / `d d` / `g d` / `F` | Kafka ACL tab: new ACLs / delete the ACL / open its topic or group / filter by principal, resource type or name (`fx` clears) |
| `p` / `R` / `<Space>v` | Kafka connector tab: pause or resume / restart (in Tasks: the task) / edit the config in `$EDITOR` |
| `yk` / `ys` / `yr` / `yu` | Elasticsearch documents: copy the `_id` / the `_source` / as console requests / as curl |
| `X` | Elasticsearch documents: delete every document the filter matches |
| `<Space>x n` | Elasticsearch documents: export as NDJSON |
| `<Space>u` | Elasticsearch documents: update every document the filter matches (painless in `$EDITOR`) |
| `o` / `g d` | Elasticsearch index tab: add mapping fields in `$EDITOR` / on Stats, open the index's shards |
| `Enter` / `.` / `F` / `d d` | Elasticsearch cluster tab: node details, why a shard is unassigned / all shards / filter shards / cancel the task |
| `i` / `d d` / `<Space>w` / `.` | Elasticsearch cluster settings: edit / back to the default / apply / show the defaults |
| `<Space>v` / `y y` | Elasticsearch template, ILM policy, pipeline and role tabs: edit in `$EDITOR` / copy the JSON |
| `<Space>p` | Elasticsearch documents: run an ingest pipeline on the selected document (`_simulate`) |
| `o` / `d d` | Elasticsearch tree: new snapshot (on a repository) or repository (on Snapshots) / delete a snapshot, repository, ILM policy or pipeline |
| `R` / `d d` / `<Space>r` | Elasticsearch snapshot tab: restore / delete / reload |
| `o` / `i` / `x` / `P` / `d d` | Elasticsearch cluster tab, Users: new user / change roles / enable or disable / change the password / delete (`Enter` on Roles opens the role) |
| `F` / `fx` | Tree, on a Redis db, folder or key: filter the keys with a pattern / clear the filter |
| `<Space>k` | Open a Redis key by name |
| `v` | Redis key tab: show a string as auto / text / JSON / hex |
| `<Space>v` | Redis key tab: edit the value under the cursor in `$EDITOR` |
| `t` / `r` / `D` | Redis key tab: set or remove the TTL / rename the key / delete it |
| `o` | Tree, on a Redis db, folder or key: new key |
| `g d` | Redis console results: open the key in the cell |
| `<Space>i` | Server tab: status, sessions, locks, … (Redis: status, slow log, clients) |
| `[` / `]` | Server tab: previous / next section |
| `dd` | Server tab: disconnect the session / kill the Redis client |
| `c` | SQL server tab, sessions: cancel the session's query |
| `a` | SQL server tab, sessions: show / hide idle sessions |
| `F` / `fx` | SQL server tab, settings: filter by name / clear the filter |
| `*` | Tree or Redis key tab: star / unstar the key |
| `<Space>F` | Starred Redis keys |
| `B` | Tree, on a Redis db or folder: find the biggest keys (`Enter` opens one) |
| `↓` / `↑`, `Ctrl-n` / `Ctrl-p`, `Ctrl-j` / `Ctrl-k`, `Tab` / `Shift-Tab` | Console, insert mode: next / previous completion (`Ctrl-n` / `Ctrl-p` open the popup when closed) |
| `Enter` / `Ctrl-y` | Console, insert mode: accept the completion (`Esc` closes the popup, a second `Esc` leaves insert mode) |

Filters made with `f…` are added with `AND`; `F` shows and edits the whole condition. Text inputs
take readline keys: `←` `→`, `Home` `End` / `Ctrl-a` `Ctrl-e`, `Alt-b` `Alt-f`, `Ctrl-w`, `Ctrl-u`, `Ctrl-k`.

Press `<Space>` and wait to see every leader key. These are the defaults; see Customize.

## Customize

**Keys** — `~/.config/datavim/keymap.toml` changes the leader and binds, rebinds or unbinds
keys per context ([example with every action](docs/examples/keymap.toml)):

```toml
leader = ","

[grid]
"S" = "sort.column"
"s" = ""
```

datavim checks the file at startup and names the binding that is unknown or conflicts.
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

**Masking Kafka messages** — `[[mask]]` tables in `config.toml` hide message fields on the
connections (by name, or every connection of an `env`) and topics they name:

```toml
[[mask]]
env = "prod"                       # or connections = ["prod-kafka"]
topics = ["orders", "payments.*"]  # globs; leave out for every topic
fields = ["value.customer.phone", "value.cards[].number", "value.*.email", "key.ssn"]
headers = ["authorization", "x-user-*"]
```

A field is `key` or `value` (all of it) or a JSON path below one: `.` between keys, `name[]`
for every element of an array, `[]` for every element of a top-level array, `*` for any key
(keys that contain `.` can't be named). Masked values show as `"******"` in the table, the
detail in every format (hex and base64 too), copies, exports, the file `<Space>v` opens and
what filters and searches match. A part a path reaches that is not JSON is masked whole, and a
Schema Registry value waits masked until it is decoded. The messages on the cluster are not
changed; masked messages can't be copied to a producer console (`p` / `P` leave them out).

**Masking Elasticsearch documents** — the same `[[mask]]` tables take `indices` and `_source` paths
(a rule is for Kafka or for Elasticsearch, not both):

```toml
[[mask]]
connections = ["es-prod"]          # or env = "prod"
indices = ["users*", "accounts"]   # globs; leave out for every index
fields = ["_source.password", "_source.cards[].number"]
```

Masked fields show as `"******"` in Documents, a document opened with `<Space>k`, the detail,
copies, exports and the console's `_search`, `_mget` and `GET <index>/_doc/<id>` responses (other
responses are shown as they come). A masked field can't be edited and a masked document can't be
rewritten in `$EDITOR`; changing another field of it sends only that field with `_update`. Only the
display changes: the console still sends what you write.

**Elasticsearch snapshots** — the Snapshots node lists each repository's snapshots when it is
expanded, without their size (reading it takes a request per snapshot, slow on S3); the snapshot's
tab shows it. A restore never restores the cluster's global state and refuses an index that exists
and is open: close or delete it, or restore under a rename.

**Ghostty** — map Cmd shortcuts to leader keys with
[docs/examples/ghostty.conf](docs/examples/ghostty.conf), e.g. `keybind = cmd+e=text:\x20e`.

## Console editor

The console is a vim editor that opens in NORMAL mode, so the leader and tab switching
work at once (`i` to type): NORMAL, INSERT, VISUAL (`v`) and V-LINE (`V`) modes;
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

datavim reads each connection's catalog (tables, views, columns, routines) in the
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
  `:set noreadonly` unlocks the connection until datavim exits; `connections.toml` is not changed.
  To change it for good, set `Read-only` in the new/edit connection form (`auto` follows `env`).
- SQL Server and Oracle have no read-only session, so there datavim is the only guard, and a stricter one:
  it reads every statement of a batch (T-SQL needs no semicolons) and blocks anything it does
  not know to be harmless — `EXEC` of any procedure but a few read-only system ones
  (`sp_help`, `sp_helptext`, `sp_columns`, `sp_who` …), `IF` blocks, `DBCC`, PL/SQL blocks, `CALL`
  and the like (`ALTER SESSION SET CURRENT_SCHEMA` / `NLS_…` are allowed). For a
  production server also use an account that can only read.
- Redis has no read-only session either: browsing sends only read commands, and in the console
  datavim alone enforces read-only — it refuses write, dangerous and unknown commands (module
  commands included). On prod, writes and unknown commands ask for `y`; dangerous commands
  (`FLUSHDB`, `FLUSHALL`, `CONFIG SET`, `CLIENT KILL`, `SHUTDOWN`, `DEBUG` …) ask on every
  connection and on prod want the connection name typed; `KEYS` is refused on a db of more than
  10,000 keys (use `SCAN` or the tree filter). Several lines are checked together before any
  runs, so `MULTI` does not get around these rules. Edits, TTL changes, renames and deletes from
  the key tab and the tree go through the same checks. For a production instance also use an
  ACL user that can only read (`+@read`).
- Kafka: browsing reads partitions directly and never joins a consumer group, commits offsets or
  creates a topic (sending to a missing topic is an error). Read-only connections refuse sending,
  creating and deleting topics and resetting offsets; on prod writes ask for `y` and resetting
  offsets wants the group name typed; deleting a topic wants its name typed on every connection.
  Offsets are reset only on an empty group, and not at all if the group changed since the preview.
  The same rules cover config changes, adding partitions and registering schemas (writes), and
  deleting messages, groups, group offsets and schema versions (dangerous: the topic, group or
  subject name is typed on every connection). Groups and their offsets are deleted only while
  the group is empty, checked again right before the request; schemas are only ever soft-deleted.
  Creating ACLs is a write; deleting one is dangerous (its principal is typed on every connection).
  Pausing, resuming and restarting connectors and tasks and changing a connector's config are
  writes too (`y` on every connection; refused when read-only).
  For a production cluster also use an account whose ACLs only allow reading.
- Elasticsearch: every request — the tabs' and the console's — is graded by method and path
  (and the body of `_bulk`, `_aliases`, `_delete_by_query`, `_settings`); a request datavim does not
  know counts as dangerous. Read-only connections refuse anything but reads. Writes ask for `y`
  (refresh, flush and clearing caches off prod go at once; on prod the request is shown); dangerous
  requests (deleting or closing an index, delete / update by query, force merge, cluster settings…)
  want the index name typed; a pattern, `_all`, several indices or an alias of several indices on a
  delete, close or delete by query is expanded first, shown, and sent with the expanded names (so
  an index made since is never touched) — on prod it is refused. A console run confirms its writes
  once and each dangerous request alone. Document writes carry `if_seq_no` / `if_primary_term`.
  Every read-only check runs in one place, the Doer the session hands out. Restoring a snapshot,
  deleting a snapshot, repository, ILM policy, pipeline, user or role, and registering a repository
  want the object's name typed. A new user's password and a password change are typed hidden and
  never shown in a confirmation, the history, the audit log or the log.
- `env = "prod"` frames the tab area in red and shows `[PROD]` in the status bar; every
  write or DDL statement on a prod connection asks for `y` first.
- `UPDATE` / `DELETE` without `WHERE` asks you to type the table name before it runs.
- In manual commit mode (`<Space>t`) quitting or closing a console with an open transaction
  asks first. If the connection drops during a transaction, datavim says the transaction is
  lost instead of reconnecting silently. Use `<Space>t` rather than `SET autocommit = 0`.
- Imports run in one transaction and write nothing unless every row goes in; on MySQL a table the import created is dropped again.
- Every write that reaches a server is kept in an audit log (`<Space>a`, in `state.db`, the latest
  50,000): SQL statements that may write, Redis write and unknown commands, Kafka and Schema
  Registry writes, Elasticsearch writes (method and path, never the body), with their result. It keeps SQL with passwords hidden and Redis commands as
  typed (passwords hidden), never statement parameters or Kafka message contents. Writes that
  read-only refused or that you cancelled are not in it.

## Development

```sh
make test               # unit tests
make test-integration   # needs Docker (OrbStack: `orb start`)
make lint
make snapshot           # build release archives locally with GoReleaser
make demo               # re-record docs/demo.gif (needs vhs and Docker)
```

Logs: `~/.local/state/datavim/datavim.log` (`--debug` for more detail).
See [CONTRIBUTING.md](CONTRIBUTING.md) before sending a pull request.

## License

MIT — see [LICENSE](LICENSE).
