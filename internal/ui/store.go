package ui

import "github.com/shepherd-yun/sqlvim/internal/history"

// Store keeps SQL history, console drafts, recently opened tabs, starred Redis keys and the audit log;
// *history.Store implements it.
type Store interface {
	AddHistory(e history.Entry) error
	History(filter string, limit int) ([]history.Entry, error)
	SaveDraft(key, text string) error
	Draft(key string) (string, error)
	AddRecent(r history.Recent) error
	Recent(limit int) ([]history.Recent, error)
	AddFavorite(f history.Favorite) error
	RemoveFavorite(f history.Favorite) error
	Favorites() ([]history.Favorite, error)
	AddAudit(e history.AuditEntry) error
	Audit(q history.AuditQuery) ([]history.AuditEntry, error)
}
