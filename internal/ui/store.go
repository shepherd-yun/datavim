package ui

import "github.com/shepherd-yun/sqlvim/internal/history"

// Store keeps SQL history and console drafts; *history.Store implements it.
type Store interface {
	AddHistory(e history.Entry) error
	History(filter string, limit int) ([]history.Entry, error)
	SaveDraft(key, text string) error
	Draft(key string) (string, error)
}
