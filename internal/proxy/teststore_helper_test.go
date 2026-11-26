package proxy

import (
	"path/filepath"

	"github.com/kingfs/Trajecta/internal/auth"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/store"
)

// newTestStore opens the production SQLite constructor with the historical
// test defaults (explicit driver, trace_index.sqlite3, 4/4 pool).
func newTestStore(dir string) (*store.Store, error) {
	return store.NewWithDatabase(dir, "sqlite", filepath.Join(dir, "trace_index.sqlite3"), 4, 4)
}

// newAnonymousTestHandler opts isolated proxy tests into the explicit
// test-only anonymous verifier. Production handlers must always use a real
// token verifier; nil intentionally fails closed.
func newAnonymousTestHandler(cfg *config.Config, st *store.Store) (*Handler, error) {
	return NewHandlerWithAuth(cfg, st, nil, auth.AllowAnonymousForTest())
}
