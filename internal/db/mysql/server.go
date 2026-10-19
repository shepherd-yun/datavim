package mysql

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// server is the conn's db.ServerReader.
type server struct{ c *conn }

func (c *conn) Server() db.ServerReader { return server{c: c} }

// maxSQL is how much of a statement the server tab keeps.
const maxSQL = 1024

// version reads (once) whether the server has the 8.0.22 names:
// performance_schema.processlist, SHOW REPLICA STATUS, SHOW REPLICAS.
func (s server) version(ctx context.Context) (text string, modern bool, err error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if s.c.version == "" {
		rs, err := s.c.Query(ctx, "SELECT VERSION()")
		if err != nil {
			return "", false, unavailable(err)
		}
		s.c.version = rs.Rows[0][0].Text
	}
	return s.c.version, db.VersionAtLeast(s.c.version, 8, 0, 22), nil
}

// unavailable turns a privilege error into db.ErrUnavailable with the
// server's words.
func unavailable(err error) error {
	var me *gomysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1044, 1142, 1143, 1227, 1370:
			return db.Unavailable("permission denied: " + me.Message)
		case 1146:
			return db.Unavailable(me.Message)
		}
	}
	return err
}

func (s server) Info(ctx context.Context) (db.ServerInfo, error) {
	v, _, err := s.version(ctx)
	if err != nil {
		return db.ServerInfo{}, err
	}
	return db.ServerInfo{Version: v, SettingsTitle: "Variables", CanCancel: true}, nil
}

// named reads a two-column name/value result into a map.
func named(rs *db.ResultSet) map[string]string {
	m := make(map[string]string, len(rs.Rows))
	for _, r := range rs.Rows {
		m[strings.ToLower(r[0].Text)] = r[1].Text
	}
	return m
}

func num(m map[string]string, k string) float64 {
	f, _ := strconv.ParseFloat(m[k], 64)
	return f
}

func (s server) Sample(ctx context.Context) (db.ServerSample, error) {
	st, err := s.c.Query(ctx, "SHOW GLOBAL STATUS WHERE Variable_name IN ('Uptime', 'Threads_connected', "+
		"'Threads_running', 'Max_used_connections', 'Innodb_buffer_pool_read_requests', 'Innodb_buffer_pool_reads', "+
		"'Aborted_clients', 'Aborted_connects', 'Questions', 'Handler_commit', 'Slow_queries', 'Bytes_received', 'Bytes_sent')")
	if err != nil {
		return db.ServerSample{}, unavailable(err)
	}
	at := time.Now()
	vs, err := s.c.Query(ctx, "SELECT VERSION(), @@max_connections, @@read_only, @@innodb_buffer_pool_size")
	if err != nil {
		return db.ServerSample{}, unavailable(err)
	}
	m, v := named(st), vs.Rows[0]
	role := "primary"
	if rs, err := s.replicaStatus(ctx); err == nil && len(rs.Rows) > 0 {
		role = "replica of " + field(rs, rs.Rows[0], "Source_Host", "Master_Host") + ":" + field(rs, rs.Rows[0], "Source_Port", "Master_Port")
	}
	if v[2].Text == "1" {
		role += " · read-only"
	}
	maxConn, _ := strconv.ParseFloat(v[1].Text, 64)
	pool, _ := strconv.ParseFloat(v[3].Text, 64)
	facts := []db.Fact{
		{Label: "version", Text: v[0].Text},
		{Label: "uptime", Kind: db.FactDuration, Num: num(m, "uptime")},
		{Label: "role", Text: role},
		{Label: "connections", Kind: db.FactCount, Num: num(m, "threads_connected"), Of: maxConn},
		{Label: "running", Kind: db.FactCount, Num: num(m, "threads_running")},
		{Label: "peak", Kind: db.FactCount, Num: num(m, "max_used_connections")},
		{Label: "buffer pool", Kind: db.FactBytes, Num: pool},
	}
	if req := num(m, "innodb_buffer_pool_read_requests"); req > 0 {
		facts = append(facts, db.Fact{Label: "hit rate", Kind: db.FactPercent, Num: 100 * (1 - num(m, "innodb_buffer_pool_reads")/req)})
	}
	facts = append(facts, db.Fact{Label: "aborted", Text: fmt.Sprintf("clients %s · connects %s", m["aborted_clients"], m["aborted_connects"])})
	return db.ServerSample{
		Facts: facts,
		Metrics: []db.Metric{
			{Name: "QPS", Counter: true, Value: num(m, "questions")},
			{Name: "commits/s", Counter: true, Value: num(m, "handler_commit")},
			{Name: "running", Value: num(m, "threads_running")},
			{Name: "slow/s", Counter: true, Value: num(m, "slow_queries")},
			{Name: "net in", Counter: true, Value: num(m, "bytes_received"), Unit: db.UnitBytes},
			{Name: "net out", Counter: true, Value: num(m, "bytes_sent"), Unit: db.UnitBytes},
		},
		At: at,
	}, nil
}

// field is the value of the first of names that rs has, in row r.
func field(rs *db.ResultSet, r []db.Value, names ...string) string {
	for _, n := range names {
		for i, c := range rs.Columns {
			if strings.EqualFold(c.Name, n) {
				return r[i].Text
			}
		}
	}
	return ""
}

func (s server) replicaStatus(ctx context.Context) (*db.ResultSet, error) {
	_, modern, err := s.version(ctx)
	if err != nil {
		return nil, err
	}
	q := "SHOW SLAVE STATUS"
	if modern {
		q = "SHOW REPLICA STATUS"
	}
	rs, err := s.c.Query(ctx, q)
	return rs, unavailable(err)
}

// clip redacts a statement for display and keeps its first maxSQL runes.
func clip(sql string) string {
	sql = sqlx.RedactSQL(sql)
	if r := []rune(sql); len(r) > maxSQL {
		return string(r[:maxSQL])
	}
	return sql
}

func seconds(s string) string {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return ""
	}
	return db.FormatElapsed(time.Duration(n) * time.Second)
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
	_, modern, err := s.version(ctx)
	if err != nil {
		return db.SessionList{}, err
	}
	from := "information_schema.PROCESSLIST"
	if modern {
		from = "performance_schema.processlist"
	}
	rs, err := s.c.Query(ctx, "SELECT p.ID, p.USER, p.HOST, p.DB, p.COMMAND, p.TIME, p.STATE, p.INFO, "+
		"TIMESTAMPDIFF(SECOND, t.trx_started, NOW()) FROM "+from+" p "+
		"LEFT JOIN information_schema.INNODB_TRX t ON t.trx_mysql_thread_id = p.ID "+
		"WHERE p.COMMAND <> 'Daemon' AND p.USER <> 'system user' ORDER BY p.TIME DESC")
	if err != nil {
		return db.SessionList{}, unavailable(err)
	}
	self := map[string]bool{}
	if as, err := s.c.Query(ctx, "SELECT PROCESSLIST_ID FROM performance_schema.session_connect_attrs "+
		"WHERE ATTR_NAME = 'program_name' AND ATTR_VALUE = 'sqlvim'"); err == nil {
		for _, r := range as.Rows {
			self[r[0].Text] = true
		}
	}
	var list db.SessionList
	var rows [][]string
	for _, r := range rs.Rows {
		id, cmd, inTx := r[0].Text, r[4].Text, !r[8].Null
		list.Total++
		if cmd != "Sleep" {
			list.Active++
		} else if !inTx && !includeIdle {
			continue
		}
		state := cmd
		if cmd == "Query" && r[6].Text != "" {
			state = r[6].Text
		}
		trx := ""
		if inTx {
			trx = seconds(r[8].Text)
		}
		elapsed, sql := seconds(r[5].Text), clip(r[7].Text)
		rows = append(rows, []string{id, r[1].Text, r[2].Text, r[3].Text, state, elapsed, trx, sql})
		list.IDs = append(list.IDs, db.SessionID{ID: id})
		list.Info = append(list.Info, db.SessionSummary{User: r[1].Text, Host: r[2].Text, DB: r[3].Text, State: state, Elapsed: elapsed, SQL: sql})
		list.Self = append(list.Self, self[id])
	}
	list.Rows = textRows([]string{"id", "user", "host", "db", "state", "time", "trx", "sql"}, rows)
	return list, nil
}

func (s server) Locks(ctx context.Context) (*db.ResultSet, error) {
	_, modern, err := s.version(ctx)
	if err != nil {
		return nil, err
	}
	q := "SELECT r.trx_mysql_thread_id, TIMESTAMPDIFF(SECOND, r.trx_wait_started, NOW()), b.trx_mysql_thread_id, " +
		"bp.COMMAND, TIMESTAMPDIFF(SECOND, b.trx_started, NOW()), CONCAT(l.lock_mode, ' ', l.lock_type, " +
		"IFNULL(CONCAT(' (', l.lock_index, ')'), '')), l.lock_table, r.trx_query, b.trx_query " +
		"FROM information_schema.INNODB_LOCK_WAITS w " +
		"JOIN information_schema.INNODB_TRX r ON r.trx_id = w.requesting_trx_id " +
		"JOIN information_schema.INNODB_TRX b ON b.trx_id = w.blocking_trx_id " +
		"JOIN information_schema.INNODB_LOCKS l ON l.lock_id = w.requested_lock_id " +
		"LEFT JOIN information_schema.PROCESSLIST bp ON bp.ID = b.trx_mysql_thread_id"
	if modern || db.VersionAtLeast(s.c.version, 8, 0, 0) {
		q = "SELECT r.trx_mysql_thread_id, TIMESTAMPDIFF(SECOND, r.trx_wait_started, NOW()), b.trx_mysql_thread_id, " +
			"bp.COMMAND, TIMESTAMPDIFF(SECOND, b.trx_started, NOW()), CONCAT(l.LOCK_MODE, ' ', l.LOCK_TYPE, " +
			"IFNULL(CONCAT(' (', l.INDEX_NAME, ')'), '')), CONCAT(l.OBJECT_SCHEMA, '.', l.OBJECT_NAME), r.trx_query, b.trx_query " +
			"FROM performance_schema.data_lock_waits w " +
			"JOIN information_schema.INNODB_TRX r ON r.trx_id = w.REQUESTING_ENGINE_TRANSACTION_ID " +
			"JOIN information_schema.INNODB_TRX b ON b.trx_id = w.BLOCKING_ENGINE_TRANSACTION_ID " +
			"JOIN performance_schema.data_locks l ON l.ENGINE_LOCK_ID = w.REQUESTING_ENGINE_LOCK_ID " +
			"LEFT JOIN information_schema.PROCESSLIST bp ON bp.ID = b.trx_mysql_thread_id"
	}
	rs, err := s.c.Query(ctx, q)
	if err != nil {
		return nil, unavailable(err)
	}
	rows := make([][]string, len(rs.Rows))
	for i, r := range rs.Rows {
		blocker := r[3].Text
		if blocker == "Sleep" {
			blocker = "idle in tx"
		}
		rows[i] = []string{
			r[0].Text, seconds(r[1].Text), r[2].Text, strings.TrimSpace(blocker + " " + seconds(r[4].Text)),
			r[5].Text, r[6].Text, clip(r[7].Text), clip(r[8].Text),
		}
	}
	return textRows([]string{"waiting", "wait", "blocked by", "blocker", "lock", "table", "waiting sql", "blocking sql"}, rows), nil
}

// picos formats a performance_schema timer value (picoseconds).
func picos(s string) string {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return ""
	}
	return db.FormatElapsed(time.Duration(f / 1000))
}

func (s server) TopStatements(ctx context.Context, n int) (*db.ResultSet, error) {
	on, err := s.c.Query(ctx, "SELECT @@performance_schema")
	if err != nil {
		return nil, unavailable(err)
	}
	if on.Rows[0][0].Text != "1" {
		return nil, db.Unavailable("performance_schema is off")
	}
	rs, err := s.c.Query(ctx, "SELECT DIGEST_TEXT, COUNT_STAR, SUM_TIMER_WAIT, AVG_TIMER_WAIT, SUM_ROWS_EXAMINED, "+
		"SUM_ROWS_SENT, SCHEMA_NAME FROM performance_schema.events_statements_summary_by_digest "+
		"WHERE DIGEST_TEXT IS NOT NULL ORDER BY SUM_TIMER_WAIT DESC LIMIT "+strconv.Itoa(n))
	if err != nil {
		return nil, unavailable(err)
	}
	rows := make([][]string, len(rs.Rows))
	for i, r := range rs.Rows {
		rows[i] = []string{clip(r[0].Text), r[1].Text, picos(r[2].Text), picos(r[3].Text), r[4].Text, r[5].Text, r[6].Text}
	}
	return textRows([]string{"statement", "calls", "total", "mean", "rows examined", "rows sent", "db"}, rows), nil
}

func (s server) Settings(ctx context.Context) (*db.ResultSet, error) {
	rs, err := s.c.Query(ctx, "SHOW GLOBAL VARIABLES")
	if err != nil {
		return nil, unavailable(err)
	}
	rs.Columns[0].Name, rs.Columns[1].Name = "name", "value"
	return rs, nil
}

func (s server) Replication(ctx context.Context) (*db.ResultSet, error) {
	_, modern, err := s.version(ctx)
	if err != nil {
		return nil, err
	}
	var rows [][]string
	rs, err := s.replicaStatus(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rs.Rows {
		f := func(names ...string) string { return field(rs, r, names...) }
		lag := f("Seconds_Behind_Source", "Seconds_Behind_Master")
		if lag != "" {
			lag = seconds(lag)
		}
		lastErr := f("Last_IO_Error")
		if e := f("Last_SQL_Error"); e != "" {
			lastErr = e
		}
		rows = append(rows, []string{
			"replica of", f("Source_Host", "Master_Host") + ":" + f("Source_Port", "Master_Port"),
			f("Replica_IO_Running", "Slave_IO_Running"), f("Replica_SQL_Running", "Slave_SQL_Running"),
			lag, lastErr, f("Executed_Gtid_Set"),
		})
	}
	q := "SHOW SLAVE HOSTS"
	if modern {
		q = "SHOW REPLICAS"
	}
	hosts, err := s.c.Query(ctx, q)
	if err != nil {
		return nil, unavailable(err)
	}
	for _, r := range hosts.Rows {
		rows = append(rows, []string{
			"source to", field(hosts, r, "Host") + ":" + field(hosts, r, "Port"), "", "", "", "",
			field(hosts, r, "Replica_UUID", "Slave_UUID"),
		})
	}
	return textRows([]string{"direction", "peer", "io", "sql", "lag", "last error", "gtid / uuid"}, rows), nil
}

func (s server) kill(ctx context.Context, what string, id db.SessionID) (bool, error) {
	n, err := strconv.ParseUint(id.ID, 10, 64)
	if err != nil {
		return false, fmt.Errorf("bad session id %q", id.ID)
	}
	_, err = s.c.Exec(ctx, what+strconv.FormatUint(n, 10))
	var me *gomysql.MySQLError
	if errors.As(err, &me) && me.Number == 1094 { // unknown thread id
		return false, nil
	}
	if err != nil {
		return false, unavailable(err)
	}
	return true, nil
}

func (s server) CancelQuery(ctx context.Context, id db.SessionID) (bool, error) {
	return s.kill(ctx, "KILL QUERY ", id)
}

func (s server) KillSession(ctx context.Context, id db.SessionID) (bool, error) {
	return s.kill(ctx, "KILL ", id)
}
