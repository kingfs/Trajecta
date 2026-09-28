package legacymigrate

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// journalSuffixes are the files SQLite keeps next to a database.
var journalSuffixes = []string{"-wal", "-shm", "-journal"}

// ArchiveSQLiteDatabase renames a legacy database (and its journal siblings)
// so that the deployment stops looking like a SQLite deployment. The files are
// never deleted: they are moved to <name><suffix>.
func ArchiveSQLiteDatabase(path, suffix string) ([]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("archive %s: %w", path, err)
	}
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		suffix = ".migrated"
	}
	target := path + suffix
	if _, err := os.Stat(target); err == nil {
		target = fmt.Sprintf("%s.%s", target, time.Now().UTC().Format("20060102150405"))
	}
	if err := os.Rename(path, target); err != nil {
		return nil, fmt.Errorf("archive %s: %w", path, err)
	}
	moved := []string{fmt.Sprintf("%s -> %s", path, target)}
	for _, journal := range journalSuffixes {
		sibling := path + journal
		if _, err := os.Stat(sibling); err != nil {
			continue
		}
		journalTarget := target + journal
		if err := os.Rename(sibling, journalTarget); err != nil {
			return moved, fmt.Errorf("archive %s: %w", sibling, err)
		}
		moved = append(moved, fmt.Sprintf("%s -> %s", sibling, journalTarget))
	}
	return moved, nil
}
