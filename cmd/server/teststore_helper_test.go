package main

import (
	"path/filepath"

	"github.com/kingfs/Trajecta/internal/store"
)

// newTestStore opens the production SQLite constructor with the historical
// test defaults (explicit driver, trace_index.sqlite3, 4/4 pool).
func newTestStore(dir string) (*store.Store, error) {
	return store.NewWithDatabase(dir, "sqlite", filepath.Join(dir, "trace_index.sqlite3"), 4, 4)
}
