package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// server is the conn's db.ServerReader. Sessions, locks and settings are
// the whole cluster's; pg_stat_statements is read in the conn's database.
type server struct{ c *conn }

func (c *conn) Server() db.ServerReader { return server{c: c} }

// maxSQL is how much of a statement the server tab keeps.
const maxSQL = 1024

// unavailable turns a privilege or missing-object error into
// db.ErrUnavailable with the server's words.
func unavailable(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "42501":
			return db.Unavailable("permission denied: " + pe.Message)
		case "42P01", "42883", "55000":
			return db.Unavailable(pe.Message)
		}
	}
	return err
}

func (s server) query(ctx context.Context, sql string, args ...any) (*db.ResultSet, error) {
	rs, err := s.c.Query(ctx, sql, args...)
	return rs, unavailable(err)
}

func (s server) Info(ctx context.Context) (db.ServerInfo, error) {
	rs, err := s.query(ctx, "SELECT current_setting('server_version')")
	if err != nil {
		return db.ServerInfo{}, err
	}
	return db.ServerInfo{Version: "PostgreSQL " + rs.Rows[0][0].Text, SettingsTitle: "Settings", CanCancel: true}, nil
}

func num(v db.Value) float64 {
	f, _ := strconv.ParseFloat(v.Text, 64)
	return f
}

// seconds formats a number of seconds; NULL is "".
func seconds(v db.Value) string {
	if v.Null || v.Text == "" {
		return ""
	}
	return db.FormatElapsed(time.Duration(num(v) * float64(time.Second)))
}

const sampleSQL = `SELECT extract(epoch FROM now() - pg_postmaster_start_time())::text,
	pg_is_in_recovery()::int::text,
	(SELECT count(*) FROM pg_stat_replication)::text,
	current_setting('max_connections'),
	count(*)::text,
	count(*) FILTER (WHERE state = 'active')::text,
	count(*) FILTER (WHERE state LIKE 'idle in transaction%')::text,
	(SELECT sum(xact_commit + xact_rollback) FROM pg_stat_database)::text,
	(SELECT sum(tup_returned + tup_fetched) FROM pg_stat_database)::text,
	(SELECT sum(tup_inserted + tup_updated + tup_deleted) FROM pg_stat_database)::text,
	(SELECT sum(deadlocks) FROM pg_stat_database)::text,
	(SELECT sum(temp_files) || ' (' || pg_size_pretty(sum(temp_bytes)) || ')' FROM pg_stat_database),
	(SELECT blks_hit::text FROM pg_stat_database WHERE datname = current_database()),
	(SELECT blks_read::text FROM pg_stat_database WHERE datname = current_database()),
	pg_database_size(current_database())::text,
	(SELECT extract(epoch FROM now() - xact_start)::text FROM pg_stat_activity
		WHERE xact_start IS NOT NULL AND backend_type = 'client backend' ORDER BY xact_start LIMIT 1),
	(SELECT pid::text FROM pg_stat_activity
		WHERE xact_start IS NOT NULL AND backend_type = 'client backend' ORDER BY xact_start LIMIT 1)
FROM pg_stat_activity WHERE backend_type = 'client backend'`

func (s server) Sample(ctx context.Context) (db.ServerSample, error) {
	vs, err := s.query(ctx, "SELECT current_setting('server_version')")
	if err != nil {
		return db.ServerSample{}, err
	}
	rs, err := s.query(ctx, sampleSQL)
	if err != nil {
		return db.ServerSample{}, err
	}
	at := time.Now()
	r := rs.Rows[0]
	role := "primary"
	if r[1].Text == "1" {
		role = "standby"
	} else if n := r[2].Text; n != "0" {
		role += " · " + n + " replicas"
	}
	oldest := "—"
	if !r[15].Null {
		oldest = seconds(r[15]) + " (pid " + r[16].Text + ")"
	}
	facts := []db.Fact{
		{Label: "version", Text: "PostgreSQL " + vs.Rows[0][0].Text},
		{Label: "uptime", Kind: db.FactDuration, Num: num(r[0])},
		{Label: "role", Text: role},
		{Label: "connections", Kind: db.FactCount, Num: num(r[4]), Of: num(r[3])},
		{Label: "active", Kind: db.FactCount, Num: num(r[5])},
		{Label: "idle in tx", Kind: db.FactCount, Num: num(r[6])},
	}
	if hit, read := num(r[12]), num(r[13]); hit+read > 0 {
		facts = append(facts, db.Fact{Label: "cache hit", Kind: db.FactPercent, Num: 100 * hit / (hit + read)})
	}
	facts = append(facts,
		db.Fact{Label: "db size", Kind: db.FactBytes, Num: num(r[14])},
		db.Fact{Label: "deadlocks", Kind: db.FactCount, Num: num(r[10])},
		db.Fact{Label: "temp files", Text: r[11].Text},
		db.Fact{Label: "oldest tx", Text: oldest},
	)
	return db.ServerSample{
		Facts: facts,
		Metrics: []db.Metric{
			{Name: "TPS", Counter: true, Value: num(r[7])},
			{Name: "active", Value: num(r[5])},
			{Name: "rows read/s", Counter: true, Value: num(r[8])},
			{Name: "rows written/s", Counter: true, Value: num(r[9])},
		},
		At: at,
	}, nil
}

// clip redacts a statement for display and keeps its first maxSQL runes.
func clip(sql string) string {
	sql = sqlx.RedactSQL(sql, sqlx.Postgres)
	if r := []rune(sql); len(r) > maxSQL {
		return string(r[:maxSQL])
	}
	return sql
}

func textRows(cols []string, rows [][]string) *db.ResultSet {
	rs := &db.ResultSet{Columns: make([]db.ColumnInfo, len(cols)), Rows: make([][]db.Value, len(rows))}
	for i, c := range cols {
		rs.Columns[i] = db.ColumnInfo{Name: c}
	}
	for i, r := range rows {
		rs.Rows[i] = make([]db.Value, len(r))
		for j, v := range r {
			rs.Rows[i][j] = db.Value{Text: v}
		}
	}
	return rs
}

func (s server) Sessions(ctx context.Context, includeIdle bool) (db.SessionList, error) {
	rs, err := s.query(ctx, `SELECT pid::text, usename, application_name, client_addr::text, datname, state,
		wait_event_type || ': ' || wait_event, extract(epoch FROM now() - query_start)::text,
		extract(epoch FROM now() - xact_start)::text, query, backend_start::text
		FROM pg_stat_activity WHERE backend_type = 'client backend'
		ORDER BY state = 'active' DESC, query_start NULLS LAST`)
	if err != nil {
		return db.SessionList{}, err
	}
	var list db.SessionList
	var rows [][]string
	for _, r := range rs.Rows {
		state := r[5].Text
		list.Total++
		if state == "active" {
			list.Active++
		}
		if state == "idle" && !includeIdle {
			continue
		}
		elapsed, sql := seconds(r[7]), clip(r[9].Text)
		rows = append(rows, []string{r[0].Text, r[1].Text, r[2].Text, r[3].Text, r[4].Text, state, r[6].Text, elapsed, seconds(r[8]), sql})
		list.IDs = append(list.IDs, db.SessionID{ID: r[0].Text, Since: r[10].Text})
		list.Info = append(list.Info, db.SessionSummary{User: r[1].Text, Host: r[3].Text, DB: r[4].Text, State: state, Elapsed: elapsed, SQL: sql})
		list.Self = append(list.Self, r[2].Text == "sqlvim")
	}
	list.Rows = textRows([]string{"pid", "user", "application", "client", "db", "state", "wait", "time", "xact", "query"}, rows)
	return list, nil
}

func (s server) Locks(ctx context.Context) (*db.ResultSet, error) {
	rs, err := s.query(ctx, `SELECT w.pid::text, extract(epoch FROM now() - w.query_start)::text, b.pid::text, b.state,
		extract(epoch FROM now() - coalesce(b.xact_start, b.query_start))::text,
		l.locktype || ' ' || l.mode, l.relation::regclass::text, w.query, b.query
		FROM pg_stat_activity w
		CROSS JOIN LATERAL unnest(pg_blocking_pids(w.pid)) AS bp(pid)
		JOIN pg_stat_activity b ON b.pid = bp.pid
		LEFT JOIN LATERAL (SELECT * FROM pg_locks WHERE pid = w.pid AND NOT granted LIMIT 1) l ON true
		ORDER BY w.query_start`)
	if err != nil {
		return nil, err
	}
	rows := make([][]string, len(rs.Rows))
	for i, r := range rs.Rows {
		rows[i] = []string{r[0].Text, seconds(r[1]), r[2].Text, r[3].Text + " " + seconds(r[4]), r[5].Text, r[6].Text, clip(r[7].Text), clip(r[8].Text)}
	}
	return textRows([]string{"waiting", "wait", "blocked by", "blocker", "lock", "relation", "waiting query", "blocking query"}, rows), nil
}

// millis formats a number of milliseconds.
func millis(v db.Value) string {
	return db.FormatElapsed(time.Duration(num(v) * float64(time.Millisecond)))
}

func (s server) TopStatements(ctx context.Context, n int) (*db.ResultSet, error) {
	ext, err := s.query(ctx, "SELECT (current_setting('server_version_num')::int >= 130000)::text FROM pg_extension WHERE extname = 'pg_stat_statements'")
	if err != nil {
		return nil, err
	}
	if len(ext.Rows) == 0 {
		return nil, db.Unavailable(fmt.Sprintf("pg_stat_statements is not installed in database %q", s.c.database))
	}
	total, mean := "total_time", "mean_time"
	if ext.Rows[0][0].Text == "true" {
		total, mean = "total_exec_time", "mean_exec_time"
	}
	rs, err := s.query(ctx, `SELECT s.query, s.calls::text, s.`+total+`::text, s.`+mean+`::text, s.rows::text, d.datname
		FROM pg_stat_statements s LEFT JOIN pg_database d ON d.oid = s.dbid
		ORDER BY s.`+total+` DESC LIMIT $1`, n)
	if err != nil {
		return nil, err
	}
	rows := make([][]string, len(rs.Rows))
	for i, r := range rs.Rows {
		rows[i] = []string{clip(r[0].Text), r[1].Text, millis(r[2]), millis(r[3]), r[4].Text, r[5].Text}
	}
	return textRows([]string{"query", "calls", "total", "mean", "rows", "db"}, rows), nil
}

func (s server) Settings(ctx context.Context) (*db.ResultSet, error) {
	rs, err := s.query(ctx, `SELECT name, current_setting(name), source, CASE WHEN pending_restart THEN 'yes' ELSE '' END
		FROM pg_settings ORDER BY name`)
	if err != nil {
		return nil, err
	}
	for i, n := range []string{"name", "setting", "source", "pending restart"} {
		rs.Columns[i].Name = n
	}
	return rs, nil
}

func (s server) Replication(ctx context.Context) (*db.ResultSet, error) {
	rs, err := s.query(ctx, `SELECT 'standby', coalesce(application_name, '') || ' ' || coalesce(client_addr::text, 'local'),
		state, sync_state, extract(epoch FROM write_lag)::text, extract(epoch FROM flush_lag)::text,
		extract(epoch FROM replay_lag)::text, NULL
		FROM pg_stat_replication
		UNION ALL
		SELECT 'upstream', coalesce(sender_host, '') || ':' || coalesce(sender_port::text, ''), status, NULL, NULL, NULL,
		extract(epoch FROM now() - pg_last_xact_replay_timestamp())::text, NULL
		FROM pg_stat_wal_receiver
		UNION ALL
		SELECT 'slot', slot_name || ' (' || slot_type || ')', CASE WHEN active THEN 'active' ELSE 'inactive' END,
		NULL, NULL, NULL, NULL,
		CASE WHEN pg_is_in_recovery() THEN NULL ELSE pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)) END
		FROM pg_replication_slots`)
	if err != nil {
		return nil, err
	}
	rows := make([][]string, len(rs.Rows))
	for i, r := range rs.Rows {
		rows[i] = []string{r[0].Text, r[1].Text, r[2].Text, r[3].Text, seconds(r[4]), seconds(r[5]), seconds(r[6]), r[7].Text}
	}
	return textRows([]string{"kind", "peer", "state", "sync", "write lag", "flush lag", "replay lag", "retained wal"}, rows), nil
}

// signal runs fn (pg_cancel_backend or pg_terminate_backend) on the
// session if it is still the one id names.
func (s server) signal(ctx context.Context, fn string, id db.SessionID) (bool, error) {
	pid, err := strconv.Atoi(id.ID)
	if err != nil {
		return false, fmt.Errorf("bad session id %q", id.ID)
	}
	rs, err := s.query(ctx, "SELECT "+fn+"(pid)::text FROM pg_stat_activity WHERE pid = $1 AND backend_start::text = $2", pid, id.Since)
	if err != nil {
		return false, err
	}
	if len(rs.Rows) == 0 {
		gone, err := s.query(ctx, "SELECT 1 FROM pg_stat_activity WHERE pid = $1", pid)
		if err != nil {
			return false, err
		}
		if len(gone.Rows) > 0 {
			return false, db.ErrSessionChanged
		}
		return false, nil
	}
	return rs.Rows[0][0].Text == "true", nil
}

func (s server) CancelQuery(ctx context.Context, id db.SessionID) (bool, error) {
	return s.signal(ctx, "pg_cancel_backend", id)
}

func (s server) KillSession(ctx context.Context, id db.SessionID) (bool, error) {
	return s.signal(ctx, "pg_terminate_backend", id)
}
