package ui

import (
	"context"
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/meta"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

// catalogTimeout bounds one catalog load.
const catalogTimeout = 60 * time.Second

type metaLoadedMsg struct {
	key  meta.Key
	gen  int
	snap *meta.Snapshot
	err  error
}

// metaStaleMsg says a console ran DDL, so the catalog of its database changed.
type metaStaleMsg struct{ conn, database string }

// metaKeyFor is the key of the catalog that covers database on conn: a
// MySQL catalog covers the server, a PostgreSQL or SQL Server one a single
// database, an Oracle one the connection's service.
func metaKeyFor(b Backend, conn, database string) (meta.Key, bool) {
	cfg, ok := b.Connection(conn)
	if !ok || cfg.Driver == "redis" || cfg.Driver == "kafka" {
		return meta.Key{}, false // redis keys and kafka topics have no catalog
	}
	f := sqlx.DialectOf(cfg.Driver).Features()
	if !f.Databases || !f.Schemas {
		return meta.Key{Conn: conn}, true
	}
	if database == "" {
		database = cfg.Database
	}
	if database == "" {
		database = f.FallbackDatabase
	}
	return meta.Key{Conn: conn, Database: database}, true
}

func loadMeta(b Backend, k meta.Key, gen int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), catalogTimeout)
		defer cancel()
		var snap *meta.Snapshot
		err := withConn(ctx, b, k.Conn, k.Database, func(c db.Conn) error {
			var err error
			snap, err = meta.Load(ctx, c.Meta(), k)
			return err
		})
		if err != nil {
			slog.Warn("load catalog", "conn", k.Conn, "database", k.Database, "err", err)
		}
		return metaLoadedMsg{key: k, gen: gen, snap: snap, err: err}
	}
}

// ensureMeta loads the catalog of database on conn in the background unless
// it was loaded (or tried) before. Failed loads wait for <space>r.
func (m *Model) ensureMeta(conn, database string) tea.Cmd {
	k, ok := metaKeyFor(m.backend, conn, database)
	if !ok {
		return nil
	}
	if _, st := m.catalogs.Get(k); st != meta.Missing {
		return nil
	}
	gen, ok := m.catalogs.Start(k)
	if !ok {
		return nil
	}
	return loadMeta(m.backend, k, gen)
}

// reloadMeta loads the catalog again; a load already running is superseded.
func (m *Model) reloadMeta(conn, database string) tea.Cmd {
	k, ok := metaKeyFor(m.backend, conn, database)
	if !ok {
		return nil
	}
	return loadMeta(m.backend, k, m.catalogs.Restart(k))
}

func (m *Model) metaLoaded(msg metaLoadedMsg) tea.Cmd {
	if !m.catalogs.Finish(msg.key, msg.gen, msg.snap, msg.err) {
		return nil
	}
	s, ok := m.overlay.(*overlay.Search)
	if !ok || s.Scope() != overlay.ScopeObjects {
		return nil
	}
	s.SetNote(m.searchNote())
	return m.runSearch(s.Requery())
}

// metaStale reloads a catalog that DDL changed, if it was ever loaded.
func (m *Model) metaStale(msg metaStaleMsg) tea.Cmd {
	k, ok := metaKeyFor(m.backend, msg.conn, msg.database)
	if !ok {
		return nil
	}
	if _, st := m.catalogs.Get(k); st == meta.Missing {
		return nil
	}
	return m.reloadMeta(msg.conn, msg.database)
}

// retryFailedMeta reloads the catalogs of conn whose last load failed, such
// as loads that ran before its password was entered.
func (m *Model) retryFailedMeta(conn string) tea.Cmd {
	var cmds []tea.Cmd
	for _, k := range m.catalogs.Failed(conn) {
		cmds = append(cmds, loadMeta(m.backend, k, m.catalogs.Restart(k)))
	}
	return tea.Batch(cmds...)
}
