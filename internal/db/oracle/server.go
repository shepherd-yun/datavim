package oracle

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sijms/go-ora/v2/network"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// server is the conn's db.ServerReader. It reads the v$ views
// (SELECT_CATALOG_ROLE) and kills with ALTER SYSTEM; in a PDB it sees the
// PDB's sessions. No AWR or ASH: they need a license.
type server struct{ c *conn }

func (c *conn) Server() db.ServerReader { return server{c: c} }

// maxSQL is how much of a statement the server tab keeps.
const maxSQL = 1024

// unavailable turns a missing view or privilege into db.ErrUnavailable.
func unavailable(err error) error {
	var oe *network.OracleError
	if errors.As(err, &oe) {
		switch oe.ErrCode {
		case 942:
			return db.Unavailable("needs SELECT_CATALOG_ROLE: " + oe.ErrMsg)
		case 1031:
			return db.Unavailable("permission denied: " + oe.ErrMsg)
		}
	}
	return err
}

func (s server) query(ctx context.Context, sql string, args ...any) (*db.ResultSet, error) {
	rs, err := s.c.Query(ctx, sql, args...)
	return rs, unavailable(err)
}

// version is the server's version; product_component_version is readable
// without SELECT_CATALOG_ROLE.
func (s server) version(ctx context.Context) (string, error) {
	rs, err := s.query(ctx, "SELECT version FROM product_component_version WHERE product LIKE 'Oracle%' FETCH FIRST 1 ROWS ONLY")
	if err != nil {
		return "", err
	}
	if len(rs.Rows) == 0 {
		return "", errors.New("no Oracle version in product_component_version")
	}
	return rs.Rows[0][0].Text, nil
}

func (s server) Info(ctx context.Context) (db.ServerInfo, error) {
	v, err := s.version(ctx)
	if err != nil {
		return db.ServerInfo{}, err
	}
	return db.ServerInfo{Version: "Oracle " + v, SettingsTitle: "Parameters", CanCancel: db.VersionAtLeast(v, 18, 0, 0)}, nil
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

const sampleSQL = `SELECT ROUND((SYSDATE - i.startup_time) * 86400),
	(SELECT database_role || ' · ' || open_mode FROM v$database),
	(SELECT COUNT(*) FROM v$session WHERE type = 'USER'),
	(SELECT COUNT(*) FROM v$session WHERE type = 'USER' AND status = 'ACTIVE'),
	(SELECT value FROM v$parameter WHERE name = 'sessions'),
	(SELECT SUM(value) FROM v$sga),
	(SELECT value FROM v$pgastat WHERE name = 'total PGA allocated'),
	i.version
FROM v$instance i`

func (s server) Sample(ctx context.Context) (db.ServerSample, error) {
	rs, err := s.query(ctx, sampleSQL)
	if err != nil {
		return db.ServerSample{}, err
	}
	at := time.Now()
	st, err := s.query(ctx, `SELECT name, value FROM v$sysstat WHERE name IN ('user calls', 'execute count',
		'user commits', 'user rollbacks', 'db block gets from cache', 'consistent gets from cache', 'physical reads cache')`)
	if err != nil {
		return db.ServerSample{}, err
	}
	stat := map[string]float64{}
	for _, r := range st.Rows {
		stat[r[0].Text] = num(r[1])
	}
	r := rs.Rows[0]
	facts := []db.Fact{
		{Label: "version", Text: "Oracle " + r[7].Text},
		{Label: "uptime", Kind: db.FactDuration, Num: num(r[0])},
		{Label: "role", Text: strings.ToLower(r[1].Text)},
		{Label: "sessions", Kind: db.FactCount, Num: num(r[2]), Of: num(r[4])},
		{Label: "active", Kind: db.FactCount, Num: num(r[3])},
	}
	if gets := stat["db block gets from cache"] + stat["consistent gets from cache"]; gets > 0 {
		facts = append(facts, db.Fact{Label: "buffer hit", Kind: db.FactPercent, Num: 100 * (1 - stat["physical reads cache"]/gets)})
	}
	facts = append(facts,
		db.Fact{Label: "SGA", Kind: db.FactBytes, Num: num(r[5])},
		db.Fact{Label: "PGA", Kind: db.FactBytes, Num: num(r[6])},
	)
	return db.ServerSample{
		Facts: facts,
		Metrics: []db.Metric{
			{Name: "user calls/s", Counter: true, Value: stat["user calls"]},
			{Name: "executions/s", Counter: true, Value: stat["execute count"]},
			{Name: "commits/s", Counter: true, Value: stat["user commits"] + stat["user rollbacks"]},
			{Name: "active", Value: num(r[3])},
		},
		At: at,
	}, nil
}

// clip redacts a statement for display and keeps its first maxSQL runes.
func clip(sql string) string {
	sql = sqlx.RedactSQL(sql, sqlx.Oracle)
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

// sqlOf is the text of the statement session alias a runs, or ran last.
func sqlOf(a string) string {
	return `(SELECT MAX(q.sql_text) FROM v$sql q WHERE q.sql_id = NVL(` + a + `.sql_id, ` + a + `.prev_sql_id)
		AND q.child_number = CASE WHEN ` + a + `.sql_id IS NULL THEN ` + a + `.prev_child_number ELSE ` + a + `.sql_child_number END)`
}

func (s server) Sessions(ctx context.Context, includeIdle bool) (db.SessionList, error) {
	rs, err := s.query(ctx, `SELECT s.sid || ',' || s.serial#, s.username, s.machine, s.program, s.schemaname, s.status,
		s.event, s.last_call_et, CASE WHEN s.taddr IS NULL THEN 0 ELSE 1 END, `+sqlOf("s")+`
		FROM v$session s WHERE s.type = 'USER'
		ORDER BY CASE s.status WHEN 'ACTIVE' THEN 0 ELSE 1 END, s.last_call_et DESC`)
	if err != nil {
		return db.SessionList{}, err
	}
	var list db.SessionList
	var rows [][]string
	for _, r := range rs.Rows {
		status, inTx := strings.ToLower(r[5].Text), r[8].Text == "1"
		list.Total++
		if status == "active" {
			list.Active++
		} else if !inTx && !includeIdle {
			continue
		}
		if status == "inactive" && inTx {
			status = "idle in tx"
		}
		elapsed, sql := seconds(r[7]), clip(r[9].Text)
		rows = append(rows, []string{r[0].Text, r[1].Text, r[2].Text, r[3].Text, r[4].Text, status, r[6].Text, elapsed, sql})
		list.IDs = append(list.IDs, db.SessionID{ID: r[0].Text})
		list.Info = append(list.Info, db.SessionSummary{User: r[1].Text, Host: r[2].Text, DB: r[4].Text, State: status, Elapsed: elapsed, SQL: sql})
		list.Self = append(list.Self, r[3].Text == "datavim")
	}
	list.Rows = textRows([]string{"sid,serial#", "user", "machine", "program", "schema", "status", "event", "time", "sql"}, rows)
	return list, nil
}

func (s server) Locks(ctx context.Context) (*db.ResultSet, error) {
	rs, err := s.query(ctx, `SELECT w.sid || ',' || w.serial#, w.wait_time_micro / 1e6, b.sid || ',' || b.serial#,
		b.status, b.last_call_et, CASE WHEN b.taddr IS NULL THEN 0 ELSE 1 END, w.event,
		(SELECT o.owner || '.' || o.object_name FROM all_objects o WHERE o.object_id = w.row_wait_obj#),
		`+sqlOf("w")+`, `+sqlOf("b")+`
		FROM v$session w JOIN v$session b ON b.sid = w.blocking_session
		WHERE w.blocking_session IS NOT NULL
		ORDER BY w.wait_time_micro DESC`)
	if err != nil {
		return nil, err
	}
	rows := make([][]string, len(rs.Rows))
	for i, r := range rs.Rows {
		blocker := strings.ToLower(r[3].Text)
		if blocker == "inactive" && r[5].Text == "1" {
			blocker = "idle in tx"
		}
		rows[i] = []string{r[0].Text, seconds(r[1]), r[2].Text, blocker + " " + seconds(r[4]), r[6].Text, r[7].Text, clip(r[8].Text), clip(r[9].Text)}
	}
	return textRows([]string{"waiting", "wait", "blocked by", "blocker", "event", "object", "waiting sql", "blocking sql"}, rows), nil
}

// micros formats a number of microseconds.
func micros(v db.Value) string {
	return db.FormatElapsed(time.Duration(num(v) * float64(time.Microsecond)))
}

func (s server) TopStatements(ctx context.Context, n int) (*db.ResultSet, error) {
	rs, err := s.query(ctx, `SELECT sql_text, executions, elapsed_time, ROUND(elapsed_time / NULLIF(executions, 0)),
		buffer_gets, rows_processed, parsing_schema_name
		FROM v$sql ORDER BY elapsed_time DESC FETCH FIRST :1 ROWS ONLY`, n)
	if err != nil {
		return nil, err
	}
	rows := make([][]string, len(rs.Rows))
	for i, r := range rs.Rows {
		rows[i] = []string{clip(r[0].Text), r[1].Text, micros(r[2]), micros(r[3]), r[4].Text, r[5].Text, r[6].Text}
	}
	return textRows([]string{"statement", "calls", "total", "mean", "buffer gets", "rows", "schema"}, rows), nil
}

func (s server) Settings(ctx context.Context) (*db.ResultSet, error) {
	rs, err := s.query(ctx, "SELECT name, value, isdefault, issys_modifiable FROM v$parameter ORDER BY name")
	if err != nil {
		return nil, err
	}
	for i, n := range []string{"name", "value", "default", "modifiable"} {
		rs.Columns[i].Name = n
	}
	return rs, nil
}

func (s server) Replication(ctx context.Context) (*db.ResultSet, error) {
	rs, err := s.query(ctx, `SELECT 'stat', name, value || NVL2(unit, ' (' || unit || ')', ''), time_computed FROM v$dataguard_stats
		UNION ALL
		SELECT 'destination', dest_name, status, NVL(error, database_mode) FROM v$archive_dest_status
		WHERE status <> 'INACTIVE' AND type <> 'LOCAL'`)
	if err != nil {
		return nil, err
	}
	for i, n := range []string{"kind", "name", "value", "detail"} {
		rs.Columns[i].Name = n
	}
	return rs, nil
}

var sessionIDPattern = regexp.MustCompile(`^(\d+),(\d+)$`)

// alter runs ALTER SYSTEM <verb> '<sid,serial#>'<suffix> on a live session.
func (s server) alter(ctx context.Context, verb, suffix string, id db.SessionID) (bool, error) {
	m := sessionIDPattern.FindStringSubmatch(id.ID)
	if m == nil {
		return false, fmt.Errorf("bad session id %q", id.ID)
	}
	rs, err := s.query(ctx, "SELECT 1 FROM v$session WHERE sid = :1 AND serial# = :2 AND status <> 'KILLED'", m[1], m[2])
	if err != nil {
		return false, err
	}
	if len(rs.Rows) == 0 {
		return false, nil
	}
	_, err = s.c.Exec(ctx, "ALTER SYSTEM "+verb+" '"+id.ID+"'"+suffix)
	var oe *network.OracleError
	if errors.As(err, &oe) {
		switch oe.ErrCode {
		case 30: // session does not exist
			return false, nil
		case 31: // marked for kill
			return true, nil
		}
	}
	if err != nil {
		return false, unavailable(err)
	}
	return true, nil
}

func (s server) CancelQuery(ctx context.Context, id db.SessionID) (bool, error) {
	v, err := s.version(ctx)
	if err != nil {
		return false, err
	}
	if !db.VersionAtLeast(v, 18, 0, 0) {
		return false, db.Unavailable("cancelling a query needs Oracle 18c or later")
	}
	return s.alter(ctx, "CANCEL SQL", "", id)
}

func (s server) KillSession(ctx context.Context, id db.SessionID) (bool, error) {
	return s.alter(ctx, "KILL SESSION", " IMMEDIATE", id)
}
