package legacymigrate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kingfs/Trajecta/internal/appdbmigrate"
)

func TestSnapshotDriftTablesAreDocumented(t *testing.T) {
	got := SnapshotDriftTables()
	want := []string{"upstream_models", "upstream_targets"}
	if len(got) != len(want) {
		t.Fatalf("SnapshotDriftTables() = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Fatalf("SnapshotDriftTables() = %v, want %v", got, want)
		}
		if reason := SnapshotDriftReason(name); strings.TrimSpace(reason) == "" {
			t.Errorf("SnapshotDriftReason(%q) is empty; the excuse must be explainable", name)
		}
	}
}

// TestToleratesMissingKeys pins that the tolerance is limited to the documented
// snapshot tables and is off unless the operator asks for it.
func TestToleratesMissingKeys(t *testing.T) {
	strict := CopyOptions{VerifyOnly: true}
	for _, table := range append(SnapshotDriftTables(), "logs", "semantic_nodes", "parse_jobs") {
		if strict.toleratesMissingKeys(table) {
			t.Errorf("toleratesMissingKeys(%q) = true without TolerateSnapshotDrift", table)
		}
	}

	tolerant := CopyOptions{VerifyOnly: true, TolerateSnapshotDrift: true}
	for _, table := range SnapshotDriftTables() {
		if !tolerant.toleratesMissingKeys(table) {
			t.Errorf("toleratesMissingKeys(%q) = false with TolerateSnapshotDrift", table)
		}
	}
	for _, table := range []string{"logs", "semantic_nodes", "parse_jobs", "trace_observations", "users"} {
		if tolerant.toleratesMissingKeys(table) {
			t.Errorf("toleratesMissingKeys(%q) = true; only snapshot tables may be excused", table)
		}
	}
}

// TestSnapshotDriftToleranceIntegration proves the archive gate's behaviour on a
// scratch Postgres database: rows the running server replaced in a runtime
// snapshot table refuse the archive by default, and are excused (with the count
// reported) only when TolerateSnapshotDrift is set, while authoritative tables
// stay strictly verified either way.
//
// It is skipped unless TRAJECTA_TEST_POSTGRES_DSN points at an instance the
// caller may create a scratch database on; the normal unit suite needs no
// database.
func TestSnapshotDriftToleranceIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TRAJECTA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TRAJECTA_TEST_POSTGRES_DSN to run the snapshot drift integration test")
	}
	ctx := context.Background()

	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	if err := admin.PingContext(ctx); err != nil {
		t.Fatalf("connect to %s: %v", dsn, err)
	}

	name := fmt.Sprintf("trajecta_snapshottest_%d", os.Getpid())
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

	suffix := strings.ReplaceAll(t.Name(), "/", "_") + "_" + time.Now().UTC().Format("20060102150405.000000000")
	upstreamIDs := []string{"snapshot-a-" + suffix, "snapshot-b-" + suffix}
	dbPath := filepath.Join(t.TempDir(), "legacy.sqlite3")
	source, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open(sqlite) error = %v", err)
	}
	defer source.Close()
	for _, statement := range []string{
		`CREATE TABLE logs (path TEXT PRIMARY KEY, trace_id TEXT NOT NULL, mod_time_ns INTEGER NOT NULL DEFAULT 0,
			file_size INTEGER NOT NULL DEFAULT 0, version TEXT NOT NULL DEFAULT '', request_id TEXT NOT NULL DEFAULT '',
			recorded_at datetime NOT NULL, model TEXT NOT NULL DEFAULT '', is_stream numeric NOT NULL DEFAULT 0,
			status_code INTEGER NOT NULL DEFAULT 0, prompt_tokens INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE upstream_targets (id TEXT PRIMARY KEY, base_url TEXT NOT NULL DEFAULT '',
			last_refresh_status TEXT NOT NULL DEFAULT '')`,
	} {
		if _, err := source.Exec(statement); err != nil {
			t.Fatalf("create fixture table: %v", err)
		}
	}
	for _, insert := range []struct {
		statement string
		args      []any
	}{
		{`INSERT INTO logs (path, trace_id, version, recorded_at, model, is_stream, prompt_tokens) VALUES (?,?,?,?,?,?,?)`,
			[]any{"/vault/" + suffix + ".http", "trace-" + suffix, "LLM_PROXY_V3", "2026-04-27 12:56:02.167889738 +0000 UTC", "gpt-x", 1, 11}},
		{`INSERT INTO upstream_targets (id, base_url, last_refresh_status) VALUES (?,?,?)`,
			[]any{upstreamIDs[0], "https://example.invalid/a", "ok"}},
		{`INSERT INTO upstream_targets (id, base_url, last_refresh_status) VALUES (?,?,?)`,
			[]any{upstreamIDs[1], "https://example.invalid/b", "ok"}},
	} {
		if _, err := source.Exec(insert.statement, insert.args...); err != nil {
			t.Fatalf("insert fixture row: %v", err)
		}
	}

	merge := func(tolerate, verifyOnly bool) *CopyReport {
		t.Helper()
		report, err := MergeSQLiteIntoPostgres(ctx, CopyOptions{
			SQLitePaths:           []string{dbPath},
			PostgresDSN:           scratchDSN,
			BatchSize:             10,
			Workers:               2,
			VerifyOnly:            verifyOnly,
			VerifyKeys:            true,
			Analyze:               false,
			TolerateSnapshotDrift: tolerate,
			OpenMode:              OpenModeAuto,
		})
		if err != nil {
			t.Fatalf("MergeSQLiteIntoPostgres(tolerate=%v, verifyOnly=%v) error = %v", tolerate, verifyOnly, err)
		}
		return report
	}

	applied := merge(false, false)
	if !applied.OK() {
		t.Fatalf("initial merge report is not OK: %+v", applied)
	}
	if applied.Copied != 3 {
		t.Fatalf("Copied = %d, want 3", applied.Copied)
	}

	// The running server replaces a snapshot table wholesale (store.ReplaceUpstreamModels),
	// so the legacy keys disappear from Postgres even though nothing was lost.
	admin2, err := sql.Open("postgres", scratchDSN)
	if err != nil {
		t.Fatalf("open scratch connection: %v", err)
	}
	defer admin2.Close()
	if _, err := admin2.ExecContext(ctx, `DELETE FROM upstream_targets WHERE id = $1 OR id = $2`, upstreamIDs[0], upstreamIDs[1]); err != nil {
		t.Fatalf("simulate snapshot replacement: %v", err)
	}

	strict := merge(false, true)
	if strict.OK() {
		t.Fatalf("strict verification accepted missing snapshot keys: %+v", strict)
	}
	if strict.Missing != 2 {
		t.Errorf("strict Missing = %d, want 2 (the two replaced upstream_targets rows)", strict.Missing)
	}
	if strict.Tolerated != 0 {
		t.Errorf("strict Tolerated = %d, want 0", strict.Tolerated)
	}
	if status := snapshotTestTable(t, strict, "upstream_targets").Status; status != "partial" {
		t.Errorf("strict upstream_targets status = %q, want partial", status)
	}
	if status := snapshotTestTable(t, strict, "logs").Status; status != "verified" {
		t.Errorf("strict logs status = %q, want verified", status)
	}

	tolerant := merge(true, true)
	if !tolerant.OK() {
		t.Fatalf("tolerant verification refused snapshot drift: %+v", tolerant)
	}
	if tolerant.Missing != 0 {
		t.Errorf("tolerant Missing = %d, want 0", tolerant.Missing)
	}
	if tolerant.Tolerated != 2 {
		t.Errorf("tolerant Tolerated = %d, want 2", tolerant.Tolerated)
	}
	snapshot := snapshotTestTable(t, tolerant, "upstream_targets")
	if snapshot.Status != "verified" {
		t.Errorf("tolerant upstream_targets status = %q, want verified", snapshot.Status)
	}
	if snapshot.ToleratedKeys != 2 {
		t.Errorf("tolerant upstream_targets ToleratedKeys = %d, want 2", snapshot.ToleratedKeys)
	}
	if snapshot.Reason == "" {
		t.Error("tolerant upstream_targets has no reason recorded")
	}
	if status := snapshotTestTable(t, tolerant, "logs").Status; status != "verified" {
		t.Errorf("tolerant logs status = %q, want verified", status)
	}
}

func snapshotTestTable(t *testing.T, report *CopyReport, table string) TableResult {
	t.Helper()
	for _, database := range report.Databases {
		for _, result := range database.Tables {
			if result.Table == table {
				return result
			}
		}
	}
	t.Fatalf("table %s is missing from the report", table)
	return TableResult{}
}
