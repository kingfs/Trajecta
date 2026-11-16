package channel

import (
	"path/filepath"

	"github.com/kingfs/Trajecta/internal/store"
)

// newTestStore opens the local dev/test harness database through the production
// constructor with the sqlite driver named explicitly, replacing the retired
// directory-only store.New entry point.
func newTestStore(outputDir string) (*store.Store, error) {
	return store.NewWithDatabase(outputDir, "sqlite", filepath.Join(outputDir, "trace_index.sqlite3"), 4, 4)
}
