package store

import "path/filepath"

// newSQLiteTestStore opens the local dev/test harness database through the
// production constructor with the sqlite driver named explicitly, replacing the
// retired directory-only New entry point.
func newSQLiteTestStore(outputDir string) (*Store, error) {
	return NewWithDatabase(outputDir, "sqlite", filepath.Join(outputDir, "trace_index.sqlite3"), 4, 4)
}
