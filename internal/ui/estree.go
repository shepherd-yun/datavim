package ui

// isES reports an elasticsearch connection.
func isES(b Backend, conn string) bool {
	cfg, ok := b.Connection(conn)
	return ok && cfg.Driver == "elasticsearch"
}
