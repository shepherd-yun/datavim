package dbtest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// ServerFixture is what the server status contract needs of an engine.
type ServerFixture struct {
	// SleepSQL runs for at least 30 seconds and contains the marker
	// sqlvim_contract_sleep, so Sessions can find the session running it.
	SleepSQL string
	// LockSQL updates one row of the standard fixture, so two sessions
	// running it inside a transaction block each other.
	LockSQL string
	// Begin starts a transaction; "" when the engine opens one implicitly.
	Begin string
	// SelfIDSQL returns the session's own id (as the first column of one
	// row): the sid alone on Oracle.
	SelfIDSQL string
	// Setting is a parameter Settings must list in its first column.
	Setting string
}

const sleepMarker = "sqlvim_contract_sleep"

// RunServerContract checks conn's ServerReader.
func RunServerContract(t *testing.T, conn db.Conn, f ServerFixture) {
	t.Helper()
	ctx := context.Background()
	srv := conn.Server()

	info, err := srv.Info(ctx)
	t.Run("info", func(t *testing.T) {
		if err != nil {
			t.Fatal(err)
		}
		if info.Version == "" || info.SettingsTitle == "" {
			t.Fatalf("info = %+v", info)
		}
	})

	t.Run("sample", func(t *testing.T) {
		a, err := srv.Sample(ctx)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		b, err := srv.Sample(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(a.Facts) == 0 || len(a.Metrics) == 0 {
			t.Fatalf("sample = %+v", a)
		}
		if len(a.Metrics) != len(b.Metrics) {
			t.Fatalf("metrics %d then %d", len(a.Metrics), len(b.Metrics))
		}
		for i, m := range a.Metrics {
			n := b.Metrics[i]
			if m.Name != n.Name {
				t.Errorf("metric %d: %q then %q", i, m.Name, n.Name)
			}
			if m.Counter && n.Value < m.Value {
				t.Errorf("counter %q went from %v to %v", m.Name, m.Value, n.Value)
			}
		}
		if !b.At.After(a.At) {
			t.Errorf("At %v then %v", a.At, b.At)
		}
	})

	// sleeping starts SleepSQL on a new session and waits until Sessions
	// lists it; done gets the query's error once it ends.
	sleeping := func(t *testing.T, qctx context.Context) (db.Session, db.SessionID, db.SessionList, <-chan error) {
		t.Helper()
		s, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		done := make(chan error, 1)
		go func() {
			_, err := s.Query(qctx, f.SleepSQL)
			done <- err
		}()
		deadline := time.Now().Add(10 * time.Second)
		for {
			list, err := srv.Sessions(ctx, false)
			if err != nil {
				t.Fatal(err)
			}
			if list.Rows == nil {
				t.Fatal("Sessions returned no rows")
			}
			if n := len(list.IDs); len(list.Info) != n || len(list.Self) != n || len(list.Rows.Rows) != n {
				t.Fatalf("rows %d, ids %d, info %d, self %d", len(list.Rows.Rows), n, len(list.Info), len(list.Self))
			}
			for i, in := range list.Info {
				if strings.Contains(in.SQL, sleepMarker) {
					return s, list.IDs[i], list, done
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("no session runs %q in %+v", f.SleepSQL, list.Info)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	waitDone := func(t *testing.T, done <-chan error) error {
		t.Helper()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("the query did not end")
			return nil
		}
	}

	t.Run("sessions", func(t *testing.T) {
		qctx, cancel := context.WithCancel(ctx)
		defer cancel()
		_, id, list, done := sleeping(t, qctx)
		for i, got := range list.IDs {
			if got == id && !list.Self[i] {
				t.Errorf("the contract's session is not marked Self")
			}
		}
		if list.Active < 1 || list.Total < list.Active {
			t.Errorf("total %d, active %d", list.Total, list.Active)
		}
		idle, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = idle.Close() }()
		if err := idle.Ping(ctx); err != nil {
			t.Fatal(err)
		}
		all, err := srv.Sessions(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		busy, err := srv.Sessions(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(all.IDs) <= len(busy.IDs) {
			t.Errorf("with idle %d sessions, without %d", len(all.IDs), len(busy.IDs))
		}
		cancel()
		_ = waitDone(t, done)
	})

	t.Run("cancel", func(t *testing.T) {
		qctx, cancel := context.WithCancel(ctx)
		defer cancel()
		s, id, _, done := sleeping(t, qctx)
		ok, err := srv.CancelQuery(ctx, id)
		if !info.CanCancel {
			if !errors.Is(err, db.ErrUnavailable) {
				t.Fatalf("CancelQuery without CanCancel: %v", err)
			}
			cancel()
			_ = waitDone(t, done)
			return
		}
		if err != nil || !ok {
			t.Fatalf("CancelQuery = %v, %v", ok, err)
		}
		_ = waitDone(t, done)
		if err := s.Ping(ctx); err != nil {
			t.Fatalf("the session after a cancel: %v", err)
		}
	})

	t.Run("kill", func(t *testing.T) {
		s, id, _, done := sleeping(t, ctx)
		ok, err := srv.KillSession(ctx, id)
		if err != nil || !ok {
			t.Fatalf("KillSession = %v, %v", ok, err)
		}
		_ = waitDone(t, done)
		if err := s.Ping(ctx); !errors.Is(err, db.ErrConnLost) {
			t.Fatalf("Ping after a kill: %v", err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			ok, err = srv.KillSession(ctx, id)
			if err != nil {
				t.Fatalf("KillSession of a gone session: %v", err)
			}
			if !ok {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the killed session is still there")
			}
			time.Sleep(200 * time.Millisecond)
		}
	})

	t.Run("session changed", func(t *testing.T) {
		qctx, cancel := context.WithCancel(ctx)
		defer cancel()
		s, id, _, done := sleeping(t, qctx)
		if id.Since == "" {
			cancel()
			_ = waitDone(t, done)
			t.Skip("ids are not reused")
		}
		stale := db.SessionID{ID: id.ID, Since: "1999-01-01T00:00:00Z"}
		if _, err := srv.KillSession(ctx, stale); !errors.Is(err, db.ErrSessionChanged) {
			t.Fatalf("KillSession(stale) = %v", err)
		}
		if _, err := srv.CancelQuery(ctx, stale); info.CanCancel && !errors.Is(err, db.ErrSessionChanged) {
			t.Fatalf("CancelQuery(stale) = %v", err)
		}
		cancel()
		_ = waitDone(t, done)
		if err := s.Ping(ctx); err != nil {
			t.Fatalf("the session was hit: %v", err)
		}
	})

	t.Run("locks", func(t *testing.T) {
		open := func() (db.Session, string) {
			s, err := conn.Session(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			rs, err := s.Query(ctx, f.SelfIDSQL)
			if err != nil || len(rs.Rows) != 1 {
				t.Fatalf("SelfIDSQL: %v", err)
			}
			if f.Begin != "" {
				if _, err := s.Exec(ctx, f.Begin); err != nil {
					t.Fatal(err)
				}
			}
			return s, rs.Rows[0][0].Text
		}
		a, aID := open()
		b, bID := open()
		if _, err := a.Exec(ctx, f.LockSQL); err != nil {
			t.Fatal(err)
		}
		// Runs before the sessions close, so a failure does not leave B
		// waiting forever (SQL Server has no lock timeout).
		rolledBack := false
		t.Cleanup(func() {
			if !rolledBack {
				_, _ = a.Exec(ctx, "ROLLBACK")
			}
		})
		done := make(chan error, 1)
		go func() {
			_, err := b.Exec(ctx, f.LockSQL)
			done <- err
		}()
		hasID := func(row []db.Value, id string) bool {
			for _, v := range row {
				if v.Text == id || strings.HasPrefix(v.Text, id+",") {
					return true
				}
			}
			return false
		}
		deadline := time.Now().Add(10 * time.Second)
		for found := false; !found; {
			rs, err := srv.Locks(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rs.Rows {
				if hasID(row, aID) && hasID(row, bID) {
					found = true
				}
			}
			if !found && time.Now().After(deadline) {
				t.Fatalf("no lock wait of %s on %s in %v", bID, aID, rs.Rows)
			}
			if !found {
				time.Sleep(200 * time.Millisecond)
			}
		}
		rolledBack = true
		if _, err := a.Exec(ctx, "ROLLBACK"); err != nil {
			t.Fatal(err)
		}
		if err := waitDone(t, done); err != nil {
			t.Fatalf("the waiter: %v", err)
		}
		if _, err := b.Exec(ctx, "ROLLBACK"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("settings", func(t *testing.T) {
		rs, err := srv.Settings(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rs.Rows {
			if strings.EqualFold(row[0].Text, f.Setting) {
				return
			}
		}
		t.Fatalf("no setting %q among %d", f.Setting, len(rs.Rows))
	})

	t.Run("top statements and replication", func(t *testing.T) {
		for name, read := range map[string]func() (*db.ResultSet, error){
			"TopStatements": func() (*db.ResultSet, error) { return srv.TopStatements(ctx, 50) },
			"Replication":   func() (*db.ResultSet, error) { return srv.Replication(ctx) },
		} {
			rs, err := read()
			switch {
			case errors.Is(err, db.ErrUnavailable):
				t.Logf("%s: %v", name, err)
			case err != nil:
				t.Errorf("%s: %v", name, err)
			case rs == nil || len(rs.Columns) == 0:
				t.Errorf("%s: no result", name)
			}
		}
	})
}
