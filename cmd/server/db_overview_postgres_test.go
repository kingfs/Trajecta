package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/appdbmigrate"
	"github.com/kingfs/Trajecta/internal/store"
)

// TestDBSummaryRebuildOverviewRepairsDriftPostgres runs the overview repair gate on the
// Postgres engine.
//
// The SQLite gate covers the logic, but the two drivers disagree about the SQL that actually
// reaches the database: the store holds SQLite and Postgres forms of the same statements and
// hands them to the driver by name, so a statement that only parses on one engine passes the
// SQLite gate and fails in production. This test seeds the aggregate, removes a member row out
// of band and repairs it against a scratch Postgres database, so the Postgres dialect of
// OverviewMetricRebuildStats and RebuildOverviewMetricBuckets is exercised rather than assumed.
//
// It runs against a database it creates and drops itself, so TRAJECTA_TEST_POSTGRES_DSN may
// point at any disposable server.
func TestDBSummaryRebuildOverviewRepairsDriftPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Postgres integration test in short mode")
	}
	dsn := strings.TrimSpace(os.Getenv("TRAJECTA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TRAJECTA_TEST_POSTGRES_DSN to a disposable Postgres test database DSN")
	}

	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	if err := admin.Ping(); err != nil {
		t.Fatalf("connect to %s: %v", dsn, err)
	}
	scratchName := fmt.Sprintf("trajecta_overviewtest_%d", os.Getpid())
	if _, err := admin.Exec(`DROP DATABASE IF EXISTS ` + scratchName + ` WITH (FORCE)`); err != nil {
		t.Fatalf("drop scratch database: %v", err)
	}
	if _, err := admin.Exec(`CREATE DATABASE ` + scratchName); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	// The scratch database is dropped and the admin connection is closed in one cleanup:
	// registered defers run before t.Cleanup, so closing the connection with a defer left the
	// database behind every run.
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP DATABASE IF EXISTS ` + scratchName + ` WITH (FORCE)`); err != nil {
			t.Logf("drop scratch database %s: %v", scratchName, err)
		}
		_ = admin.Close()
	})
	scratchDSN, err := postgresDSNWithDatabase(dsn, scratchName)
	if err != nil {
		t.Fatalf("build scratch dsn: %v", err)
	}
	if err := appdbmigrate.MigrateUp("postgres", scratchDSN, 0); err != nil {
		t.Fatalf("MigrateUp(postgres) error = %v", err)
	}

	dir := t.TempDir()
	st, err := store.NewWithDatabaseOptions(dir, "postgres", scratchDSN, 4, 4, store.DatabaseOptions{AutoMigrate: false})
	if err != nil {
		t.Fatalf("NewWithDatabaseOptions(postgres) error = %v", err)
	}
	at := time.Date(2026, 7, 7, 11, 0, 0, 0, time.UTC)
	writeCLISessionSummaryTestLog(t, st, dir, "overview-pg-a.http", "sess-overview-pg-a", at, http.StatusOK, 10)
	writeCLISessionSummaryTestLog(t, st, dir, "overview-pg-b.http", "sess-overview-pg-b", at.Add(3*time.Minute), http.StatusOK, 20)
	st.FlushDerivedRefresh()

	scratch, err := sql.Open("postgres", scratchDSN)
	if err != nil {
		t.Fatalf("open scratch connection: %v", err)
	}
	defer scratch.Close()
	assertCounts := func(t *testing.T, stage string, buckets int, members int) {
		t.Helper()
		var gotBuckets, gotMembers int
		if err := scratch.QueryRow(`SELECT COUNT(*) FROM overview_metric_buckets`).Scan(&gotBuckets); err != nil {
			t.Fatalf("%s: count buckets error = %v", stage, err)
		}
		if err := scratch.QueryRow(`SELECT COUNT(*) FROM overview_metric_bucket_members`).Scan(&gotMembers); err != nil {
			t.Fatalf("%s: count members error = %v", stage, err)
		}
		if gotBuckets != buckets || gotMembers != members {
			t.Fatalf("%s: overview rows = %d buckets / %d members, want %d / %d", stage, gotBuckets, gotMembers, buckets, members)
		}
	}
	assertCounts(t, "after seeding", 1, 2)

	if _, err := scratch.Exec(`DELETE FROM overview_metric_bucket_members WHERE path LIKE '%overview-pg-b.http'`); err != nil {
		t.Fatalf("delete member error = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := scratch.Close(); err != nil {
		t.Fatalf("scratch.Close() error = %v", err)
	}

	configPath := filepath.Join(dir, "config.yaml")
	configBody := `
trace:
  output_dir: ` + strconv.Quote(dir) + `
database:
  driver: postgres
  dsn: ` + strconv.Quote(scratchDSN) + `
`
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	// The counters have to come from the Postgres dialect, so read them before the rebuild
	// touches anything and check them against the engine directly.
	var dryRunOut bytes.Buffer
	if code := runDBSummaryRebuildOverviewWithOptions(dbSummaryRebuildOptions{
		configPath: configPath,
		dryRun:     true,
		format:     "json",
		stdout:     &dryRunOut,
	}); code != 0 {
		t.Fatalf("dry-run runDBSummaryRebuildOverviewWithOptions() = %d, output=%s", code, dryRunOut.String())
	}
	var dryEnvelope cliEnvelope
	if err := json.Unmarshal(dryRunOut.Bytes(), &dryEnvelope); err != nil {
		t.Fatalf("json.Unmarshal(dry-run) error = %v; output=%s", err, dryRunOut.String())
	}
	dryResult, ok := dryEnvelope.Result.(map[string]any)
	if !ok {
		t.Fatalf("dry-run result = %#v, want map", dryEnvelope.Result)
	}
	if dryResult["candidate_count"].(float64) != 2 || dryResult["buckets_before"].(float64) != 1 || dryResult["members_before"].(float64) != 1 {
		t.Fatalf("dry-run result on Postgres = %#v, want 2 candidates / 1 bucket / 1 member", dryResult)
	}

	var rebuildOut bytes.Buffer
	if code := runDBSummaryRebuildOverviewWithOptions(dbSummaryRebuildOptions{
		configPath: configPath,
		format:     "json",
		stdout:     &rebuildOut,
	}); code != 0 {
		t.Fatalf("rebuild runDBSummaryRebuildOverviewWithOptions() = %d, output=%s", code, rebuildOut.String())
	}
	var rebuildEnvelope cliEnvelope
	if err := json.Unmarshal(rebuildOut.Bytes(), &rebuildEnvelope); err != nil {
		t.Fatalf("json.Unmarshal(rebuild) error = %v; output=%s", err, rebuildOut.String())
	}
	rebuildResult, ok := rebuildEnvelope.Result.(map[string]any)
	if !ok {
		t.Fatalf("rebuild result = %#v, want map", rebuildEnvelope.Result)
	}
	if rebuildResult["mutated"] != true || rebuildResult["members_after"].(float64) != 2 {
		t.Fatalf("rebuild result on Postgres = %#v, want mutated true and both members restored", rebuildResult)
	}

	reopened, err := sql.Open("postgres", scratchDSN)
	if err != nil {
		t.Fatalf("reopen scratch connection: %v", err)
	}
	defer reopened.Close()
	var restored int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM overview_metric_bucket_members`).Scan(&restored); err != nil {
		t.Fatalf("count restored members error = %v", err)
	}
	if restored != 2 {
		t.Fatalf("members after rebuild = %d, want 2", restored)
	}
	// The rebuilt aggregate must also agree with the rows it summarizes.
	var mismatched int
	if err := reopened.QueryRow(`
		SELECT COUNT(*) FROM (
			SELECT b.bucket_start, b.bucket_size_seconds, b.request_count, b.total_tokens,
			       COALESCE(SUM(m.request_count), 0) AS member_requests,
			       COALESCE(SUM(m.total_tokens), 0) AS member_tokens
			FROM overview_metric_buckets b
			LEFT JOIN overview_metric_bucket_members m
			  ON m.bucket_start = b.bucket_start AND m.bucket_size_seconds = b.bucket_size_seconds
			GROUP BY 1, 2, 3, 4
		) t
		WHERE t.request_count <> t.member_requests OR t.total_tokens <> t.member_tokens`).Scan(&mismatched); err != nil {
		t.Fatalf("compare buckets with members error = %v", err)
	}
	if mismatched != 0 {
		t.Fatalf("%d buckets disagree with their members after the Postgres rebuild", mismatched)
	}
}

// postgresDSNWithDatabase points a DSN at another database, in both the URL form
// (postgres://user@host:5432/db?sslmode=disable) and the keyword form
// (host=... dbname=...), because both are valid values for TRAJECTA_TEST_POSTGRES_DSN.
func postgresDSNWithDatabase(dsn string, database string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	if parsed.Host != "" {
		parsed.Path = "/" + database
		return parsed.String(), nil
	}
	fields := strings.Fields(dsn)
	replaced := false
	for i, field := range fields {
		if strings.HasPrefix(field, "dbname=") {
			fields[i] = "dbname=" + database
			replaced = true
		}
	}
	if !replaced {
		fields = append(fields, "dbname="+database)
	}
	return strings.Join(fields, " "), nil
}
