package legacymigrate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/kingfs/Trajecta/internal/appdbmigrate"
)

// TestReconcileDerivedTraceIDsIntegration proves the repair that follows a merge
// which ran after the server had already re-indexed the cassettes: the derived
// tables still carry the id the legacy index recorded, so `logs.trace_id` no
// longer matches them.
//
// The repair must delete only the rows whose identity already exists under the
// current id and move every other orphan row onto it, and it must leave a row
// that references no reachable legacy trace exactly as it is.
//
// It is skipped unless TRAJECTA_TEST_POSTGRES_DSN points at an instance the
// caller may create a scratch database on.
func TestReconcileDerivedTraceIDsIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TRAJECTA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TRAJECTA_TEST_POSTGRES_DSN to run the reconciliation integration test")
	}
	ctx := context.Background()

	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	if err := admin.PingContext(ctx); err != nil {
		t.Fatalf("connect to %s: %v", dsn, err)
	}
	name := fmt.Sprintf("trajecta_reconciletest_%d", os.Getpid())
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
		t.Fatalf("drop scratch database: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() {
		defer admin.Close()
		if _, err := admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
			t.Logf("drop scratch database %s: %v", name, err)
		}
	})
	scratchDSN, err := dsnWithDatabase(dsn, name)
	if err != nil {
		t.Fatalf("build scratch dsn: %v", err)
	}
	if err := appdbmigrate.MigrateUp("postgres", scratchDSN, 0); err != nil {
		t.Fatalf("MigrateUp(postgres) error = %v", err)
	}

	pg, err := sql.Open("postgres", scratchDSN)
	if err != nil {
		t.Fatalf("open scratch connection: %v", err)
	}
	defer pg.Close()

	const (
		legacyID  = "00000e77-2a48-4403-8cbc-2dd8bc235fd8"
		currentID = "cd2b3137-0d42-4e26-b6bd-293dc7e9e670"
		cassette  = "/app/data/traces/aiapi.chaitin.net/gpt-5.5/2026/05/20/20260520_060431_204176325.http"
	)
	for _, statement := range []string{
		`CREATE TABLE node_cases (id bigserial PRIMARY KEY, trace_id text NOT NULL, node_id text NOT NULL, payload text NOT NULL, UNIQUE (trace_id, node_id))`,
		// The current index already knows the cassette under the derived id.
		fmt.Sprintf(`INSERT INTO logs (path, trace_id, mod_time_ns, file_size, version, recorded_at) VALUES ('%s', '%s', 1, 1, 'llm-proxy-v3', now())`, cassette, currentID),
		// The server re-parsed the trace: the same node ids exist under both ids.
		fmt.Sprintf(`INSERT INTO node_cases (trace_id, node_id, payload) VALUES ('%s', 'node-1', 'duplicate'), ('%s', 'node-2', 'duplicate')`, currentID, currentID),
		fmt.Sprintf(`INSERT INTO node_cases (trace_id, node_id, payload) VALUES ('%s', 'node-1', 'legacy'), ('%s', 'node-2', 'legacy'), ('%s', 'node-3', 'legacy-only')`, legacyID, legacyID, legacyID),
		// A trace whose legacy row is gone: this row is the only copy.
		`INSERT INTO node_cases (trace_id, node_id, payload) VALUES ('ffffffff-ffff-ffff-ffff-ffffffffffff', 'node-9', 'orphan')`,
	} {
		if _, err := pg.ExecContext(ctx, statement); err != nil {
			t.Fatalf("setup %q: %v", statement, err)
		}
	}

	// The legacy index still maps the old id to the cassette path. Only the
	// remaining orphan id needs a row there, but the mapping is built from it.
	dbPath := filepath.Join(t.TempDir(), "legacy.sqlite3")
	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open(sqlite) error = %v", err)
	}
	defer legacy.Close()
	for _, statement := range []string{
		`CREATE TABLE logs (path TEXT PRIMARY KEY, trace_id TEXT NOT NULL)`,
		fmt.Sprintf(`INSERT INTO logs (path, trace_id) VALUES ('%s', '%s')`, cassette, legacyID),
	} {
		if _, err := legacy.Exec(statement); err != nil {
			t.Fatalf("fixture %q: %v", statement, err)
		}
	}

	countRows := func(traceID string) int {
		t.Helper()
		var count int
		if err := pg.QueryRowContext(ctx, `SELECT count(*) FROM node_cases WHERE trace_id = $1`, traceID).Scan(&count); err != nil {
			t.Fatalf("count node_cases for %s: %v", traceID, err)
		}
		return count
	}

	// A dry run reports the same numbers an apply would use and writes nothing.
	dry, err := ReconcileDerivedTraceIDs(ctx, ReconcileOptions{
		SQLitePaths: []string{dbPath},
		PostgresDSN: scratchDSN,
	})
	if err != nil {
		t.Fatalf("ReconcileDerivedTraceIDs(dry) error = %v", err)
	}
	result := reconcileTestTable(t, dry, "node_cases")
	if result.OrphanIDs != 2 {
		t.Errorf("OrphanIDs = %d, want 2 (the legacy id and the unreachable id; the current id is indexed)", result.OrphanIDs)
	}
	if result.MappedIDs != 1 {
		t.Errorf("MappedIDs = %d, want 1", result.MappedIDs)
	}
	if result.DuplicateRows != 2 {
		t.Errorf("DuplicateRows = %d, want 2", result.DuplicateRows)
	}
	if result.UnresolvedRows != 1 {
		t.Errorf("UnresolvedRows = %d, want 1 (no legacy trace for the unmatched id)", result.UnresolvedRows)
	}
	if got := countRows(legacyID); got != 3 {
		t.Errorf("dry run changed node_cases: legacy rows = %d, want 3", got)
	}
	if len(result.IdentityKeys) != 1 || result.IdentityKeys[0] != "node_id" {
		t.Errorf("IdentityKeys = %v, want [node_id]", result.IdentityKeys)
	}
	if current := countRows(currentID); current != 2 {
		t.Errorf("current rows = %d, want 2 before the repair", current)
	}

	applied, err := ReconcileDerivedTraceIDs(ctx, ReconcileOptions{
		SQLitePaths: []string{dbPath},
		PostgresDSN: scratchDSN,
		Apply:       true,
	})
	if err != nil {
		t.Fatalf("ReconcileDerivedTraceIDs(apply) error = %v", err)
	}
	result = reconcileTestTable(t, applied, "node_cases")
	if result.DuplicateRows != 2 {
		t.Errorf("applied DuplicateRows = %d, want 2", result.DuplicateRows)
	}
	if result.RemappedRows != 1 {
		t.Errorf("applied RemappedRows = %d, want 1 (only node-3 had no counterpart)", result.RemappedRows)
	}
	// node-1 and node-2 were superseded duplicates, node-3 moved onto the
	// current id: the current id ends up with every node exactly once.
	if got := countRows(currentID); got != 3 {
		t.Errorf("current rows = %d, want 3 after the repair", got)
	}
	if got := countRows(legacyID); got != 0 {
		t.Errorf("legacy rows = %d, want 0 after the repair", got)
	}
	if applied.Unresolved() != 1 {
		t.Errorf("Unresolved() = %d, want 1", applied.Unresolved())
	}
	if applied.OK() {
		t.Error("report is OK although one orphan row references no reachable legacy trace")
	}
	// The unmatched row is the only copy of its trace, so it must survive.
	if got := countRows("ffffffff-ffff-ffff-ffff-ffffffffffff"); got != 1 {
		t.Errorf("unmatched rows = %d, want 1 (the last copy is never discarded)", got)
	}
}

// reconcileTestTable finds one table in a reconciliation report.
func reconcileTestTable(t *testing.T, report *ReconcileReport, table string) DerivedTableResult {
	t.Helper()
	for _, result := range report.Tables {
		if result.Table == table {
			if result.Err != "" {
				t.Fatalf("reconciliation of %s failed: %s", table, result.Err)
			}
			return result
		}
	}
	t.Fatalf("reconciliation report has no entry for %s", table)
	return DerivedTableResult{}
}

// TestDuplicateDeleteStatement pins the shape of the two statements the repair
// relies on: the identity comparison must survive NULL values, and a table whose
// key is only the trace id has to treat every row as the same identity.
func TestDuplicateDeleteStatement(t *testing.T) {
	withIdentity := duplicateDeleteStatement("semantic_nodes", []string{"node_id"}, true)
	for _, want := range []string{"count(*)", "semantic_nodes", "unnest($1::text[], $2::text[])", `c."node_id" IS NOT DISTINCT FROM d."node_id"`} {
		if !strings.Contains(withIdentity, want) {
			t.Errorf("count statement %q does not contain %q", withIdentity, want)
		}
	}
	withoutIdentity := duplicateDeleteStatement("trace_observations", nil, false)
	if !strings.Contains(withoutIdentity, "AND EXISTS (SELECT 1 FROM") || !strings.Contains(withoutIdentity, "AND TRUE") {
		t.Errorf("delete statement for a trace-id-only key = %q, want an unconditional identity match", withoutIdentity)
	}
	if !strings.HasPrefix(withoutIdentity, "DELETE FROM") {
		t.Errorf("delete statement = %q, want a DELETE", withoutIdentity)
	}
}
