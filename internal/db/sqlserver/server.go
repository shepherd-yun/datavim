package sqlserver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// server is the conn's db.ServerReader. Every query runs on SQL Server
// 2014: no STRING_AGG, no Query Store, no dm_exec_input_buffer.
type server struct{ c *conn }

func (c *conn) Server() db.ServerReader { return server{c: c} }

// maxSQL is how much of a statement the server tab keeps.
const maxSQL = 1024

// unavailable turns a permission error into db.ErrUnavailable.
func unavailable(err error) error {
	var me mssql.Error
	if errors.As(err, &me) {
		switch me.Number {
		case 297, 300:
			return db.Unavailable("needs VIEW SERVER STATE: " + me.Message)
		case 229, 6102:
			return db.Unavailable("permission denied: " + me.Message)
		case 208:
			return db.Unavailable(me.Message)
		}
	}
	return err
}

func (s server) query(ctx context.Context, sql string, args ...any) (*db.ResultSet, error) {
	rs, err := s.c.Query(ctx, sql, args...)
	return rs, unavailable(err)
}

func (s server) Info(ctx context.Context) (db.ServerInfo, error) {
	rs, err := s.query(ctx, "SELECT CONVERT(nvarchar(128), SERVERPROPERTY('ProductVersion')), "+
		"CONVERT(nvarchar(128), SERVERPROPERTY('ProductLevel')), CONVERT(nvarchar(128), SERVERPROPERTY('Edition'))")
	if err != nil {
		return db.ServerInfo{}, err
	}
	r := rs.Rows[0]
	return db.ServerInfo{
		Version:       fmt.Sprintf("SQL Server %s %s · %s", r[0].Text, r[1].Text, r[2].Text),
		SettingsTitle: "Configuration",
	}, nil
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

const sampleSQL = `SELECT DATEDIFF(SECOND, i.sqlserver_start_time, SYSDATETIME()),
	(SELECT COUNT(*) FROM sys.dm_exec_sessions WHERE is_user_process = 1),
	(SELECT COUNT(*) FROM sys.dm_exec_requests r JOIN sys.dm_exec_sessions s ON s.session_id = r.session_id
		WHERE s.is_user_process = 1),
	(SELECT CONVERT(int, value_in_use) FROM sys.configurations WHERE name = 'user connections'),
	i.committed_kb, i.committed_target_kb
FROM sys.dm_os_sys_info i`

const countersSQL = `SELECT RTRIM(object_name), RTRIM(counter_name), RTRIM(instance_name), cntr_value
FROM sys.dm_os_performance_counters
WHERE counter_name IN ('Batch Requests/sec', 'Transactions/sec', 'User Connections',
	'Buffer cache hit ratio', 'Buffer cache hit ratio base', 'Page life expectancy')`

func (s server) Sample(ctx context.Context) (db.ServerSample, error) {
	info, err := s.Info(ctx)
	if err != nil {
		return db.ServerSample{}, err
	}
	rs, err := s.query(ctx, sampleSQL)
	if err != nil {
		return db.ServerSample{}, err
	}
	at := time.Now()
	cs, err := s.query(ctx, countersSQL)
	if err != nil {
		return db.ServerSample{}, err
	}
	counter := map[string]float64{}
	for _, r := range cs.Rows {
		obj, name, inst := r[0].Text, r[1].Text, r[2].Text
		switch {
		case name == "Transactions/sec" && inst != "_Total":
		case name == "Page life expectancy" && !strings.HasSuffix(obj, "Buffer Manager"):
		default:
			counter[name] = num(r[3])
		}
	}
	r := rs.Rows[0]
	role := "standalone"
	// Availability groups need Enterprise (or Basic AG on Standard); a
	// server without them answers with no row, an old one with an error.
	if ag, err := s.query(ctx, "SELECT TOP 1 role_desc FROM sys.dm_hadr_availability_replica_states WHERE is_local = 1"); err == nil && len(ag.Rows) > 0 {
		role = "availability group " + strings.ToLower(ag.Rows[0][0].Text)
	}
	facts := []db.Fact{
		{Label: "version", Text: info.Version},
		{Label: "uptime", Kind: db.FactDuration, Num: num(r[0])},
		{Label: "role", Text: role},
		{Label: "sessions", Kind: db.FactCount, Num: num(r[1]), Of: num(r[3])},
		{Label: "requests", Kind: db.FactCount, Num: num(r[2])},
	}
	if base := counter["Buffer cache hit ratio base"]; base > 0 {
		facts = append(facts, db.Fact{Label: "buffer hit", Kind: db.FactPercent, Num: 100 * counter["Buffer cache hit ratio"] / base})
	}
	facts = append(facts,
		db.Fact{Label: "page life", Text: strconv.FormatFloat(counter["Page life expectancy"], 'f', 0, 64) + " s"},
		db.Fact{Label: "memory", Kind: db.FactBytes, Num: num(r[4]) * 1024, Of: num(r[5]) * 1024},
	)
	return db.ServerSample{
		Facts: facts,
		Metrics: []db.Metric{
			{Name: "batch req/s", Counter: true, Value: counter["Batch Requests/sec"]},
			{Name: "tx/s", Counter: true, Value: counter["Transactions/sec"]},
			{Name: "requests", Value: num(r[2])},
			{Name: "connections", Value: counter["User Connections"]},
		},
		At: at,
	}, nil
}

// clip redacts a statement for display and keeps its first maxSQL runes.
func clip(sql string) string {
	sql = sqlx.RedactSQL(sql)
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

// loginTime is how a session's login_time is compared: ISO 8601 text.
const loginTime = "CONVERT(varchar(30), s.login_time, 126)"

func (s server) Sessions(ctx context.Context, includeIdle bool) (db.SessionList, error) {
	rs, err := s.query(ctx, `SELECT s.session_id, s.login_name, s.host_name, s.program_name,
		DB_NAME(COALESCE(r.database_id, s.database_id)), COALESCE(r.status, s.status), r.wait_type,
		DATEDIFF(SECOND, COALESCE(r.start_time, s.last_request_start_time), GETDATE()),
		s.open_transaction_count, t.text, `+loginTime+`, CASE WHEN r.session_id IS NULL THEN 0 ELSE 1 END
		FROM sys.dm_exec_sessions s
		LEFT JOIN sys.dm_exec_requests r ON r.session_id = s.session_id
		OUTER APPLY (SELECT TOP 1 most_recent_sql_handle h FROM sys.dm_exec_connections WHERE session_id = s.session_id) c
		OUTER APPLY sys.dm_exec_sql_text(COALESCE(r.sql_handle, c.h)) t
		WHERE s.is_user_process = 1
		ORDER BY CASE WHEN r.session_id IS NULL THEN 1 ELSE 0 END, r.start_time`)
	if err != nil {
		return db.SessionList{}, err
	}
	var list db.SessionList
	var rows [][]string
	for _, r := range rs.Rows {
		active, inTx := r[11].Text == "1", r[8].Text != "0"
		list.Total++
		if active {
			list.Active++
		} else if !inTx && !includeIdle {
			continue
		}
		state := r[5].Text
		if !active && inTx {
			state = "idle in tx"
		}
		elapsed, sql := seconds(r[7]), clip(r[9].Text)
		rows = append(rows, []string{r[0].Text, r[1].Text, r[2].Text, r[3].Text, r[4].Text, state, r[6].Text, elapsed, r[8].Text, sql})
		list.IDs = append(list.IDs, db.SessionID{ID: r[0].Text, Since: r[10].Text})
		list.Info = append(list.Info, db.SessionSummary{User: r[1].Text, Host: r[2].Text, DB: r[4].Text, State: state, Elapsed: elapsed, SQL: sql})
		list.Self = append(list.Self, r[3].Text == "sqlvim")
	}
	list.Rows = textRows([]string{"id", "login", "host", "program", "db", "status", "wait", "time", "open tran", "sql"}, rows)
	return list, nil
}

func (s server) Locks(ctx context.Context) (*db.ResultSet, error) {
	rs, err := s.query(ctx, `SELECT r.session_id, r.wait_time / 1000.0, r.blocking_session_id, bs.status,
		DATEDIFF(SECOND, COALESCE(br.start_time, bs.last_request_start_time), GETDATE()), bs.open_transaction_count,
		r.wait_type + ' ' + r.wait_resource, DB_NAME(r.database_id), wt.text, bt.text
		FROM sys.dm_exec_requests r
		JOIN sys.dm_exec_sessions bs ON bs.session_id = r.blocking_session_id
		LEFT JOIN sys.dm_exec_requests br ON br.session_id = r.blocking_session_id
		OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) wt
		OUTER APPLY (SELECT TOP 1 most_recent_sql_handle h FROM sys.dm_exec_connections WHERE session_id = r.blocking_session_id) bc
		OUTER APPLY sys.dm_exec_sql_text(COALESCE(br.sql_handle, bc.h)) bt
		WHERE r.blocking_session_id > 0
		ORDER BY r.wait_time DESC`)
	if err != nil {
		return nil, err
	}
	rows := make([][]string, len(rs.Rows))
	for i, r := range rs.Rows {
		blocker := r[3].Text
		if blocker == "sleeping" && r[5].Text != "0" {
			blocker = "idle in tx"
		}
		rows[i] = []string{r[0].Text, seconds(r[1]), r[2].Text, blocker + " " + seconds(r[4]), r[6].Text, r[7].Text, clip(r[8].Text), clip(r[9].Text)}
	}
	return textRows([]string{"waiting", "wait", "blocked by", "blocker", "lock", "db", "waiting sql", "blocking sql"}, rows), nil
}

// micros formats a number of microseconds.
func micros(v db.Value) string {
	return db.FormatElapsed(time.Duration(num(v) * float64(time.Microsecond)))
}

func (s server) TopStatements(ctx context.Context, n int) (*db.ResultSet, error) {
	rs, err := s.query(ctx, `SELECT TOP (@p1)
		SUBSTRING(t.text, qs.statement_start_offset / 2 + 1, (CASE qs.statement_end_offset WHEN -1 THEN DATALENGTH(t.text)
			ELSE qs.statement_end_offset END - qs.statement_start_offset) / 2 + 1),
		qs.execution_count, qs.total_elapsed_time, qs.total_elapsed_time / qs.execution_count,
		qs.total_logical_reads, qs.total_rows, DB_NAME(t.dbid)
		FROM sys.dm_exec_query_stats qs CROSS APPLY sys.dm_exec_sql_text(qs.sql_handle) t
		ORDER BY qs.total_elapsed_time DESC`, n)
	if err != nil {
		return nil, err
	}
	rows := make([][]string, len(rs.Rows))
	for i, r := range rs.Rows {
		rows[i] = []string{clip(r[0].Text), r[1].Text, micros(r[2]), micros(r[3]), r[4].Text, r[5].Text, r[6].Text}
	}
	return textRows([]string{"statement", "calls", "total", "mean", "logical reads", "rows", "db"}, rows), nil
}

func (s server) Settings(ctx context.Context) (*db.ResultSet, error) {
	rs, err := s.query(ctx, `SELECT name, CONVERT(nvarchar(64), value_in_use), CONVERT(nvarchar(64), value),
		CASE WHEN is_dynamic = 1 THEN '' ELSE 'yes' END
		FROM sys.configurations ORDER BY name`)
	if err != nil {
		return nil, err
	}
	for i, n := range []string{"name", "in use", "configured", "needs restart"} {
		rs.Columns[i].Name = n
	}
	return rs, nil
}

func (s server) Replication(ctx context.Context) (*db.ResultSet, error) {
	rs, err := s.query(ctx, `SELECT ag.name, ar.replica_server_name, ars.role_desc, ars.synchronization_health_desc,
		DB_NAME(drs.database_id), drs.synchronization_state_desc,
		CONVERT(varchar(20), drs.log_send_queue_size), CONVERT(varchar(20), drs.redo_queue_size)
		FROM sys.dm_hadr_database_replica_states drs
		JOIN sys.availability_replicas ar ON ar.replica_id = drs.replica_id
		JOIN sys.availability_groups ag ON ag.group_id = drs.group_id
		LEFT JOIN sys.dm_hadr_availability_replica_states ars ON ars.replica_id = ar.replica_id
		ORDER BY ag.name, ar.replica_server_name`)
	if err != nil {
		return nil, err
	}
	for i, n := range []string{"group", "replica", "role", "health", "db", "state", "send queue KB", "redo queue KB"} {
		rs.Columns[i].Name = n
	}
	return rs, nil
}

// CancelQuery is not possible: SQL Server's KILL ends the session.
func (server) CancelQuery(context.Context, db.SessionID) (bool, error) {
	return false, db.Unavailable("cancelling a query is not supported on SQL Server")
}

func (s server) KillSession(ctx context.Context, id db.SessionID) (bool, error) {
	n, err := strconv.Atoi(id.ID)
	if err != nil {
		return false, fmt.Errorf("bad session id %q", id.ID)
	}
	rs, err := s.query(ctx, "SELECT "+loginTime+" FROM sys.dm_exec_sessions s WHERE s.session_id = @p1", n)
	switch {
	case err != nil:
		return false, err
	case len(rs.Rows) == 0:
		return false, nil
	case rs.Rows[0][0].Text != id.Since:
		return false, db.ErrSessionChanged
	}
	_, err = s.c.Exec(ctx, "KILL "+strconv.Itoa(n))
	var me mssql.Error
	if errors.As(err, &me) && me.Number == 6106 { // not an active process
		return false, nil
	}
	if err != nil {
		return false, unavailable(err)
	}
	return true, nil
}
