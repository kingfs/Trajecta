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
		// upstream_exchanges.trace_id is the recorder request id, a different id
		// space: the repair must never touch it even when the value looks like a
		// legacy trace id.
		fmt.Sprintf(`INSERT INTO upstream_exchanges (id, trace_id, cassette_path) VALUES ('exchange-1', '%s', '%s')`, legacyID, cassette),
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
	// upstream_exchanges keeps the recorder request id, which shares neither the
	// table nor the id space of logs.trace_id.
	var exchangeTrace, exchangePath string
	if err := pg.QueryRowContext(ctx, `SELECT trace_id, cassette_path FROM upstream_exchanges WHERE id = 'exchange-1'`).Scan(&exchangeTrace, &exchangePath); err != nil {
		t.Fatalf("read upstream_exchanges row: %v", err)
	}
	if exchangeTrace != legacyID || exchangePath != cassette {
		t.Errorf("upstream_exchanges row = (%q, %q), want (%q, %q)", exchangeTrace, exchangePath, legacyID, cassette)
	}
	for _, entry := range applied.Tables {
		if entry.Table == "upstream_exchanges" || entry.Table == "logs" {
			t.Errorf("report covers %s, which does not store a logs.trace_id", entry.Table)
		}
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
// relies on, and the comparison that decides whether the identity match can use
// an index: a NOT NULL column must use `=` because `IS NOT DISTINCT FROM` is not
// an index condition, which makes the planner read every row of the mapped trace
// for every candidate row. A nullable column keeps the NULL-safe form.
func TestDuplicateDeleteStatement(t *testing.T) {
	withIdentity := duplicateDeleteStatement("semantic_nodes", []identityColumn{{Name: "node_id", NotNull: true}}, true)
	for _, want := range []string{"count(*)", "semantic_nodes", "unnest($1::text[], $2::text[])", `c."node_id" = d."node_id"`} {
		if !strings.Contains(withIdentity, want) {
			t.Errorf("count statement %q does not contain %q", withIdentity, want)
		}
	}
	nullableIdentity := duplicateDeleteStatement("semantic_nodes", []identityColumn{{Name: "node_id"}}, true)
	if !strings.Contains(nullableIdentity, `c."node_id" IS NOT DISTINCT FROM d."node_id"`) {
		t.Errorf("nullable identity statement %q does not keep the NULL-safe comparison", nullableIdentity)
	}
	withoutIdentity := duplicateDeleteStatement("trace_observations", nil, false)
	if !strings.Contains(withoutIdentity, "AND EXISTS (SELECT 1 FROM") || !strings.Contains(withoutIdentity, "AND TRUE") {
		t.Errorf("delete statement for a trace-id-only key = %q, want an unconditional identity match", withoutIdentity)
	}
	if !strings.HasPrefix(withoutIdentity, "DELETE FROM") {
		t.Errorf("delete statement = %q, want a DELETE", withoutIdentity)
	}
	if got := sampleLimit(0); got != defaultMaxSamples {
		t.Errorf("sampleLimit(0) = %d, want %d", got, defaultMaxSamples)
	}
	if got := sampleLimit(-3); got != defaultMaxSamples {
		t.Errorf("sampleLimit(-3) = %d, want %d", got, defaultMaxSamples)
	}
	if got := sampleLimit(2500); got != 2500 {
		t.Errorf("sampleLimit(2500) = %d, want 2500", got)
	}
	// Without any table to check, the statement must not delete anything.
	unguarded := supersededPruneStatement(nil)
	if !strings.Contains(unguarded, "AND FALSE") {
		t.Errorf("prune statement without tables = %q, want an unconditional no-op guard", unguarded)
	}
}

// TestReconcileSupersededIndexRowsIntegration proves the second repair a layout
// move can need: a `logs` row whose cassette moved away while the same recording
// stayed indexed at the path it moved to. The donor's derived rows belong to the
// surviving trace id, and the donor row is removed only once nothing derives
// from it any more. A row whose cassette exists nowhere is kept, because it may
// be the only trace of that request.
func TestReconcileSupersededIndexRowsIntegration(t *testing.T) {
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
	name := fmt.Sprintf("trajecta_supersededtest_%d", os.Getpid())
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

	dir := t.TempDir()
	const (
		survivorID = "cd2b3137-0d42-4e26-b6bd-293dc7e9e670"
		donorID    = "00000e77-2a48-4403-8cbc-2dd8bc235fd8"
		orphanID   = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	)
	relDir := filepath.Join("unknown-site", "model-x", "2026", "01", "02")
	if err := os.MkdirAll(filepath.Join(dir, relDir), 0o755); err != nil {
		t.Fatalf("create cassette directory: %v", err)
	}
	cassette := filepath.Join(dir, relDir, "20260102_000000_1.http")
	if err := os.WriteFile(cassette, []byte("# trajecta/v3\n# meta: {}\n"), 0o644); err != nil {
		t.Fatalf("write cassette: %v", err)
	}
	// The donor kept the pre-move directory of the very same recording.
	donorPath := filepath.Join(dir, "model-x", "2026", "01", "02", "20260102_000000_1.http")
	orphanPath := filepath.Join(dir, "gone", "2026", "01", "02", "20260102_000000_2.http")

	for _, statement := range []string{
		`CREATE TABLE node_cases (id bigserial PRIMARY KEY, trace_id text NOT NULL, node_id text NOT NULL, payload text NOT NULL, UNIQUE (trace_id, node_id))`,
		fmt.Sprintf(`INSERT INTO logs (path, trace_id, mod_time_ns, file_size, version, recorded_at) VALUES ('%s', '%s', 1, 1, 'llm-proxy-v3', now())`, cassette, survivorID),
		fmt.Sprintf(`INSERT INTO logs (path, trace_id, mod_time_ns, file_size, version, recorded_at) VALUES ('%s', '%s', 1, 1, 'llm-proxy-v3', now())`, donorPath, donorID),
		fmt.Sprintf(`INSERT INTO logs (path, trace_id, mod_time_ns, file_size, version, recorded_at) VALUES ('%s', '%s', 1, 1, 'llm-proxy-v3', now())`, orphanPath, orphanID),
		fmt.Sprintf(`INSERT INTO node_cases (trace_id, node_id, payload) VALUES ('%s', 'node-1', 'survivor')`, survivorID),
		fmt.Sprintf(`INSERT INTO node_cases (trace_id, node_id, payload) VALUES ('%s', 'node-1', 'donor duplicate'), ('%s', 'node-2', 'donor only')`, donorID, donorID),
	} {
		if _, err := pg.ExecContext(ctx, statement); err != nil {
			t.Fatalf("setup %q: %v", statement, err)
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
	rowExists := func(path string) bool {
		t.Helper()
		var count int
		if err := pg.QueryRowContext(ctx, `SELECT count(*) FROM logs WHERE path = $1`, path).Scan(&count); err != nil {
			t.Fatalf("count logs for %s: %v", path, err)
		}
		return count > 0
	}

	dry, err := ReconcileDerivedTraceIDs(ctx, ReconcileOptions{PostgresDSN: scratchDSN, DataRoot: dir})
	if err != nil {
		t.Fatalf("ReconcileDerivedTraceIDs(dry) error = %v", err)
	}
	if dry.Superseded == nil {
		t.Fatal("report has no superseded index section although a data root was given")
	}
	if dry.Superseded.Checked != 3 || dry.Superseded.MissingFiles != 2 {
		t.Errorf("checked/missing = %d/%d, want 3/2", dry.Superseded.Checked, dry.Superseded.MissingFiles)
	}
	if dry.Superseded.Superseded != 1 || dry.Superseded.DonorIDs != 1 {
		t.Errorf("superseded/donors = %d/%d, want 1/1", dry.Superseded.Superseded, dry.Superseded.DonorIDs)
	}
	if dry.Superseded.OrphanFiles != 1 {
		t.Errorf("orphan files = %d, want 1 (the row whose cassette exists nowhere)", dry.Superseded.OrphanFiles)
	}
	if dry.Superseded.DerivedDuplicates != 1 || dry.Superseded.DerivedRemapped != 1 {
		t.Errorf("derived duplicate/remapped = %d/%d, want 1/1", dry.Superseded.DerivedDuplicates, dry.Superseded.DerivedRemapped)
	}
	if dry.Superseded.PrunePending != 1 {
		t.Errorf("prune pending = %d, want 1", dry.Superseded.PrunePending)
	}
	if !rowExists(donorPath) || !rowExists(orphanPath) {
		t.Error("the dry run deleted trace index rows")
	}
	if got := countRows(donorID); got != 2 {
		t.Errorf("donor rows = %d, want 2 before the repair", got)
	}

	applied, err := ReconcileDerivedTraceIDs(ctx, ReconcileOptions{
		PostgresDSN:              scratchDSN,
		DataRoot:                 dir,
		Apply:                    true,
		PruneSupersededIndexRows: true,
	})
	if err != nil {
		t.Fatalf("ReconcileDerivedTraceIDs(apply) error = %v", err)
	}
	if applied.Superseded == nil || applied.Superseded.Pruned != 1 {
		t.Errorf("pruned = %v, want 1", applied.Superseded)
	}
	if rowExists(donorPath) {
		t.Error("the superseded trace index row survived the prune")
	}
	if !rowExists(cassette) {
		t.Error("the surviving trace index row was pruned")
	}
	if !rowExists(orphanPath) {
		t.Error("a trace index row whose cassette exists nowhere must be kept")
	}
	if got := countRows(survivorID); got != 2 {
		t.Errorf("survivor rows = %d, want 2 (its own node and the donor's unique node)", got)
	}
	if got := countRows(donorID); got != 0 {
		t.Errorf("donor rows = %d, want 0 after the repair", got)
	}
}
