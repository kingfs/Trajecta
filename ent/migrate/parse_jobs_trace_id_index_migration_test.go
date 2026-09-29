package entmigrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Postgres schema only ever had the status/updated_at index on parse_jobs,
// while every trace-by-trace lookup (the monitor's trace detail, the derived-id
// repair and the superseded-row guard) filters on trace_id. Without a
// trace_id-leading index those lookups sequential-scan the table once per trace.
func TestPostgresParseJobsTraceIDIndexMigrationContainsIndex(t *testing.T) {
	upMatches, err := filepath.Glob(filepath.Join("..", "postgres-migrations", "*_add_parse_jobs_trace_id_index.up.sql"))
	if err != nil {
		t.Fatalf("glob up migration: %v", err)
	}
	if len(upMatches) != 1 {
		t.Fatalf("parse_jobs trace id index up migrations = %v, want exactly one", upMatches)
	}
	downMatches, err := filepath.Glob(filepath.Join("..", "postgres-migrations", "*_add_parse_jobs_trace_id_index.down.sql"))
	if err != nil {
		t.Fatalf("glob down migration: %v", err)
	}
	if len(downMatches) != 1 {
		t.Fatalf("parse_jobs trace id index down migrations = %v, want exactly one", downMatches)
	}

	up, err := os.ReadFile(upMatches[0])
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	for _, want := range []string{
		`CREATE INDEX IF NOT EXISTS "parsejob_trace_id_status" ON "parse_jobs" ("trace_id", "status")`,
	} {
		if !strings.Contains(string(up), want) {
			t.Fatalf("migration %s missing %q", upMatches[0], want)
		}
	}

	down, err := os.ReadFile(downMatches[0])
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	for _, want := range []string{
		`DROP INDEX "parsejob_trace_id_status"`,
	} {
		if !strings.Contains(string(down), want) {
			t.Fatalf("migration %s missing %q", downMatches[0], want)
		}
	}
}
