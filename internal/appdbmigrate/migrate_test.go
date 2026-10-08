package appdbmigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	entmigrations "github.com/kingfs/Trajecta/ent"

	_ "github.com/lib/pq"
)

func TestMigrateUpSQLiteUsesStoreInit(t *testing.T) {
	err := MigrateUp("sqlite", "ignored.sqlite3", 0)
	if !errors.Is(err, ErrSQLiteUsesStoreInit) {
		t.Fatalf("MigrateUp(sqlite) error = %v, want ErrSQLiteUsesStoreInit", err)
	}
}

func TestMigrateDownSQLiteUsesStoreInit(t *testing.T) {
	err := MigrateDown("sqlite", "ignored.sqlite3", 1, false)
	if !errors.Is(err, ErrSQLiteUsesStoreInit) {
		t.Fatalf("MigrateDown(sqlite) error = %v, want ErrSQLiteUsesStoreInit", err)
	}
}

func TestCheckStatusSQLiteReportsSchemaInitFallback(t *testing.T) {
	status, err := CheckStatus("sqlite", "ignored.sqlite3")
	if err != nil {
		t.Fatalf("CheckStatus(sqlite) error = %v", err)
	}
	if status.Driver != "sqlite" || status.Versioned || status.Available {
		t.Fatalf("CheckStatus(sqlite) = %+v, want non-versioned unavailable fallback", status)
	}
	if !strings.Contains(status.Message, "database file does not exist") || !strings.Contains(status.Message, ErrSQLiteUsesStoreInit.Error()) {
		t.Fatalf("CheckStatus(sqlite) message = %q, want fallback explanation", status.Message)
	}
	if status.Advice != SQLiteMigrationAdvice {
		t.Fatalf("CheckStatus(sqlite) advice = %q, want SQLiteMigrationAdvice", status.Advice)
	}
}

func TestCheckStatusSQLiteReportsApplicationSchemaMarker(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "trace_index.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open(sqlite) error = %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE logs (path TEXT PRIMARY KEY)`,
		`CREATE TABLE responses (id TEXT PRIMARY KEY)`,
		`CREATE TABLE response_items (id TEXT PRIMARY KEY)`,
		`CREATE TABLE request_audits (id TEXT PRIMARY KEY)`,
		`CREATE TABLE execution_events (id TEXT PRIMARY KEY)`,
		`CREATE TABLE upstream_exchanges (id TEXT PRIMARY KEY)`,
		`CREATE TABLE tool_call_audits (id TEXT PRIMARY KEY)`,
		`CREATE TABLE session_summaries (session_id TEXT PRIMARY KEY)`,
		`CREATE TABLE overview_metric_buckets (bucket_start datetime NOT NULL, bucket_size_seconds INTEGER NOT NULL, PRIMARY KEY (bucket_start, bucket_size_seconds))`,
		`CREATE TABLE overview_metric_bucket_members (path TEXT PRIMARY KEY)`,
		`CREATE TABLE app_schema_status (
			namespace TEXT PRIMARY KEY,
			version INTEGER NOT NULL,
			mode TEXT NOT NULL,
			source TEXT NOT NULL,
			updated_at datetime NOT NULL
		)`,
		`INSERT INTO app_schema_status (namespace, version, mode, source, updated_at)
		 VALUES ('application', 1, 'schema-init', 'internal/store raw DDL startup initialization', CURRENT_TIMESTAMP)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("db.Exec(%q) error = %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error = %v", err)
	}

	status, err := CheckStatus("sqlite", dbPath)
	if err != nil {
		t.Fatalf("CheckStatus(sqlite) error = %v", err)
	}
	if status.Driver != "sqlite" || status.Versioned || !status.Available || !status.RequiredTablesPresent {
		t.Fatalf("CheckStatus(sqlite) = %+v, want readable non-versioned application schema", status)
	}
	if status.SchemaMarker != "app_schema_status" || status.SchemaMarkerVersion != 1 || len(status.MissingTables) != 0 {
		t.Fatalf("sqlite marker status = %+v", status)
	}
	if !strings.Contains(status.Message, "marker version 1") || !strings.Contains(status.Message, ErrSQLiteUsesStoreInit.Error()) {
		t.Fatalf("CheckStatus(sqlite) message = %q, want marker and fallback explanation", status.Message)
	}
}

func TestCheckStatusSQLiteReportsLegacySchemaWithoutMarker(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "trace_index.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open(sqlite) error = %v", err)
	}
	for _, table := range sqliteApplicationRequiredTables {
		if _, err := db.Exec(`CREATE TABLE ` + table + ` (id TEXT PRIMARY KEY)`); err != nil {
			t.Fatalf("create table %q error = %v", table, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error = %v", err)
	}

	status, err := CheckStatus("sqlite", dbPath)
	if err != nil {
		t.Fatalf("CheckStatus(sqlite) error = %v", err)
	}
	if !status.Available || !status.RequiredTablesPresent || status.SchemaMarker != "" || status.SchemaMarkerVersion != 0 {
		t.Fatalf("legacy sqlite status = %+v, want available schema without marker", status)
	}
	if !strings.Contains(status.Message, "app_schema_status marker is missing") || !strings.Contains(status.Message, "compatible legacy startup-schema database") {
		t.Fatalf("legacy sqlite message = %q", status.Message)
	}
	if status.Advice != SQLiteMigrationAdvice {
		t.Fatalf("legacy sqlite advice = %q", status.Advice)
	}
}

func TestCheckStatusSQLiteReportsMissingRequiredTablesAdvice(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "trace_index.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open(sqlite) error = %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE logs (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create partial sqlite schema error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error = %v", err)
	}

	status, err := CheckStatus("sqlite", dbPath)
	if err != nil {
		t.Fatalf("CheckStatus(sqlite) error = %v", err)
	}
	if status.Available || status.RequiredTablesPresent || len(status.MissingTables) == 0 {
		t.Fatalf("partial sqlite status = %+v, want unavailable with missing tables", status)
	}
	if !strings.Contains(status.Message, "required tables are incomplete") || !strings.Contains(status.Message, "startup schema fallback must initialize or repair") {
		t.Fatalf("partial sqlite message = %q", status.Message)
	}
	if status.Advice != SQLiteMigrationAdvice {
		t.Fatalf("partial sqlite advice = %q", status.Advice)
	}
}

func TestCheckStatusPostgresRequiresDSN(t *testing.T) {
	_, err := CheckStatus("postgres", "")
	if err == nil {
		t.Fatalf("CheckStatus(postgres empty dsn) error = nil")
	}
	if !strings.Contains(err.Error(), "postgres application database dsn is required") {
		t.Fatalf("CheckStatus(postgres empty dsn) error = %q", err.Error())
	}
}

func TestMigrateUpPostgresRequiresDSN(t *testing.T) {
	err := MigrateUp("postgresql", "", 0)
	if err == nil {
		t.Fatalf("MigrateUp(postgresql empty dsn) error = nil")
	}
	if !strings.Contains(err.Error(), "postgres application database dsn is required") {
		t.Fatalf("MigrateUp(postgresql empty dsn) error = %q", err.Error())
	}
}

func TestMigrateDownPostgresRequiresDSN(t *testing.T) {
	err := MigrateDown("postgresql", "", 1, false)
	if err == nil {
		t.Fatalf("MigrateDown(postgresql empty dsn) error = nil")
	}
	if !strings.Contains(err.Error(), "postgres application database dsn is required") {
		t.Fatalf("MigrateDown(postgresql empty dsn) error = %q", err.Error())
	}
}

func TestMigrateUpRejectsUnsupportedDriver(t *testing.T) {
	err := MigrateUp("mysql", "mysql://example", 0)
	if err == nil {
		t.Fatalf("MigrateUp(mysql) error = nil")
	}
	if !strings.Contains(err.Error(), `application database driver "mysql" is not supported`) {
		t.Fatalf("MigrateUp(mysql) error = %q", err.Error())
	}
}

func TestMigrateDownRejectsUnsupportedDriver(t *testing.T) {
	err := MigrateDown("mysql", "mysql://example", 1, false)
	if err == nil {
		t.Fatalf("MigrateDown(mysql) error = nil")
	}
	if !strings.Contains(err.Error(), `application database driver "mysql" is not supported`) {
		t.Fatalf("MigrateDown(mysql) error = %q", err.Error())
	}
}

func TestMigrateUpPostgresIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Postgres integration test in short mode")
	}
	dsn := strings.TrimSpace(os.Getenv("TRAJECTA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TRAJECTA_TEST_POSTGRES_DSN to a disposable Postgres test database DSN")
	}

	if err := MigrateUp("postgres", dsn, 0); err != nil {
		t.Fatalf("MigrateUp(postgres) error = %v", err)
	}
	if err := MigrateUp("postgres", dsn, 0); err != nil {
		t.Fatalf("MigrateUp(postgres idempotent) error = %v", err)
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open(postgres) error = %v", err)
	}
	defer db.Close()

	for _, table := range []string{
		"schema_migrations",
		"analysis_jobs",
		"analysis_runs",
		"channel_configs",
		"channel_models",
		"channel_probe_runs",
		"dataset_examples",
		"datasets",
		"eval_runs",
		"execution_events",
		"experiment_runs",
		"logs",
		"model_catalog",
		"model_aliases",
		"parse_jobs",
		"parser_versions",
		"request_audits",
		"response_items",
		"responses",
		"scores",
		"semantic_nodes",
		"system_events",
		"tool_call_audits",
		"trace_findings",
		"trace_observations",
		"upstream_exchanges",
		"upstream_models",
		"upstream_targets",
	} {
		t.Run(table, func(t *testing.T) {
			var exists bool
			if err := db.QueryRow(`SELECT EXISTS (
				SELECT 1
				FROM information_schema.tables
				WHERE table_schema = 'public' AND table_name = $1
			)`, table).Scan(&exists); err != nil {
				t.Fatalf("query table %q error = %v", table, err)
			}
			if !exists {
				t.Fatalf("table %q does not exist", table)
			}
		})
	}

	var version uint
	var dirty bool
	if err := db.QueryRow(`SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&version, &dirty); err != nil {
		t.Fatalf("query schema_migrations error = %v", err)
	}
	if version == 0 {
		t.Fatalf("schema_migrations version = 0, want applied version")
	}
	if dirty {
		t.Fatalf("schema_migrations dirty = true")
	}

	status, err := CheckStatus("postgres", dsn)
	if err != nil {
		t.Fatalf("CheckStatus(postgres) error = %v", err)
	}
	if !status.Versioned || !status.Available || status.Version != version || status.Dirty {
		t.Fatalf("CheckStatus(postgres) = %+v, want available clean version %d", status, version)
	}
}

// TestParseJobsDedupMigrationPlansAnAntiJoin gates the statement in
// 20261006000000_unique_parse_jobs_trace_id.up.sql, which every startup that has
// not applied it yet has to run before it can serve.
//
// The statement that shipped first was
//
//	DELETE FROM "parse_jobs"
//	WHERE "id" NOT IN (SELECT MAX("id") FROM "parse_jobs" GROUP BY "trace_id");
//
// It is correct, and on a dev-sized database it is fast, because PostgreSQL
// folds a small `NOT IN` subquery into a hashed SubPlan. It stops being fast
// once that SubPlan's result no longer fits the hash budget: PostgreSQL then
// keeps the SubPlan and re-evaluates it once per outer row. On the deployment
// that hit this, 221,353 grouped ids were materialised to a 12 MB temp file and
// rescanned for each of 419,160 rows - estimated cost 11,312,529,913, one core
// pegged, the startup migration blocked on it, and not one log line emitted
// because migrations run before the first one.
//
// Neither a row count nor a duration catches that: at 300 rows the same
// statement still picks a `hashed SubPlan` and looks healthy, which is exactly
// how it passed review. The shape is what differs, and an anti join has no
// SubPlan node at all - at any size, under any work_mem - so the shape is what
// this gate asserts, alongside the deduplication result itself.
func TestParseJobsDedupMigrationPlansAnAntiJoin(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Postgres integration test in short mode")
	}
	dsn := strings.TrimSpace(os.Getenv("TRAJECTA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TRAJECTA_TEST_POSTGRES_DSN to a disposable Postgres test database DSN")
	}
	statement := parseJobsDedupStatement(t)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open(postgres) error = %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("db.Conn error = %v", err)
	}
	defer conn.Close()

	// A schema of its own, so the gate neither reads nor writes the tables the
	// rest of the integration suite migrates into the same database.
	schema := fmt.Sprintf("trajecta_parse_jobs_dedup_%d", time.Now().UnixNano())
	if _, err := conn.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatalf("create schema error = %v", err)
	}
	defer func() {
		if _, err := conn.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`); err != nil {
			t.Errorf("drop schema error = %v", err)
		}
	}()
	if _, err := conn.ExecContext(ctx, `SET search_path TO "`+schema+`"`); err != nil {
		t.Fatalf("set search_path error = %v", err)
	}

	// The shape the migration meets: an identity primary key, and the duplicate
	// trace ids the recorder enqueue and the observation result both wrote. 2,000
	// rows over 500 traces leaves four rows per trace to collapse.
	for _, seed := range []string{
		`CREATE TABLE parse_jobs (
			id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			trace_id varchar NOT NULL
		)`,
		`INSERT INTO parse_jobs (trace_id)
			SELECT 'trace-' || (x % 500) FROM generate_series(1, 2000) x`,
		`ANALYZE parse_jobs`,
		// The survivors the statement is supposed to leave, captured before it
		// runs: the newest row per trace.
		`CREATE TEMP TABLE expected_survivors AS
			SELECT trace_id, MAX(id) AS keep_id FROM parse_jobs GROUP BY trace_id`,
	} {
		if _, err := conn.ExecContext(ctx, seed); err != nil {
			t.Fatalf("seed %q error = %v", seed, err)
		}
	}

	plan := explainPlan(t, ctx, conn, statement)
	if strings.Contains(plan, "SubPlan") {
		t.Fatalf("the dedup migration keeps a per-row SubPlan, which PostgreSQL rescans for every row of parse_jobs once the grouped ids no longer fit the hash budget; plan:\n%s", plan)
	}

	if _, err := conn.ExecContext(ctx, statement); err != nil {
		t.Fatalf("run dedup statement error = %v", err)
	}

	var rows, traces int
	if err := conn.QueryRowContext(ctx, `SELECT count(*), count(DISTINCT trace_id) FROM parse_jobs`).Scan(&rows, &traces); err != nil {
		t.Fatalf("count deduplicated rows error = %v", err)
	}
	if rows != 500 || traces != 500 {
		t.Fatalf("after dedup parse_jobs holds %d rows over %d traces, want 500 over 500", rows, traces)
	}

	// Symmetric difference against the survivors captured before the statement:
	// a row that should have gone but stayed, or one that should have stayed but
	// went, shows up here. Both writers have always resolved a repeated trace id
	// by keeping the newest row.
	var drift int
	if err := conn.QueryRowContext(ctx, `
		SELECT count(*) FROM (
			(SELECT trace_id, id FROM parse_jobs
			 EXCEPT SELECT trace_id, keep_id FROM expected_survivors)
			UNION ALL
			(SELECT trace_id, keep_id FROM expected_survivors
			 EXCEPT SELECT trace_id, id FROM parse_jobs)
		) drift
	`).Scan(&drift); err != nil {
		t.Fatalf("compare survivors error = %v", err)
	}
	if drift != 0 {
		t.Fatalf("dedup kept a different row set than the newest row per trace: %d rows differ", drift)
	}
}

// parseJobsDedupStatement returns the first statement of the dedup migration
// straight out of the embedded migrations, so the gate cannot drift from the
// SQL that actually runs at startup.
func parseJobsDedupStatement(t *testing.T) string {
	t.Helper()
	name := postgresMigrationRoot + "/20261006000000_unique_parse_jobs_trace_id.up.sql"
	body, err := fs.ReadFile(entmigrations.PostgresMigrations, name)
	if err != nil {
		t.Fatalf("read embedded migration %s error = %v", name, err)
	}
	// Drop the line comments first: the explanatory comment above the statement
	// names `NOT IN`, and the statement has to be cut at its own terminator.
	var sqlText strings.Builder
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		sqlText.WriteString(line)
		sqlText.WriteString("\n")
	}
	end := strings.Index(sqlText.String(), ";")
	if end < 0 {
		t.Fatalf("migration %s has no statement terminator", name)
	}
	statement := strings.TrimSpace(sqlText.String()[:end])
	if !strings.Contains(statement, "parse_jobs") {
		t.Fatalf("migration %s first statement = %q, want one touching parse_jobs", name, statement)
	}
	return statement
}

func explainPlan(t *testing.T, ctx context.Context, conn *sql.Conn, statement string) string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "EXPLAIN "+statement)
	if err != nil {
		t.Fatalf("EXPLAIN error = %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan line error = %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows error = %v", err)
	}
	return plan.String()
}
