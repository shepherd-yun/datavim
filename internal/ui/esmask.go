package ui

import "github.com/shepherd-yun/datavim/internal/esx"

// esMaskerFor is the Masker of the Elasticsearch tabs on conn (spec §8.4):
// documents are masked as they arrive, before anything shows or copies
// them.
func (m *Model) esMaskerFor(conn string) *esx.Masker {
	cfg, _ := m.backend.Connection(conn)
	return esx.NewMasker(m.masks, conn, string(cfg.Env))
}
