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

	"github.com/lib/pq"

	"github.com/kingfs/Trajecta/internal/appdbmigrate"
)

func TestScheduleWavesOrdersDependencies(t *testing.T) {
	tables := []string{"logs", "api_tokens", "users", "channel_models"}
	waves := scheduleWaves(tables, legacyTableDependencies)
	if len(waves) != 2 {
		t.Fatalf("scheduleWaves() = %v, want 2 waves", waves)
	}
	waveOf := map[string]int{}
	for index, wave := range waves {
		for _, table := range wave {
			waveOf[table] = index
		}
	}
	if waveOf["users"] != 0 {
		t.Errorf("users landed in wave %d, want 0", waveOf["users"])
	}
	if waveOf["api_tokens"] != 1 {
		t.Errorf("api_tokens landed in wave %d, want 1 (it references users)", waveOf["api_tokens"])
	}
	for _, table := range []string{"logs", "channel_models"} {
		if waveOf[table] != 0 {
			t.Errorf("%s landed in wave %d, want 0", table, waveOf[table])
		}
	}
}

func TestScheduleWavesHandlesCyclesAndEmptyInput(t *testing.T) {
	if waves := scheduleWaves(nil, nil); len(waves) != 0 {
		t.Errorf("scheduleWaves(nil) = %v, want no waves", waves)
	}
	cycle := map[string][]string{"a": {"b"}, "b": {"a"}}
	waves := scheduleWaves([]string{"a", "b"}, cycle)
	if len(waves) != 1 || len(waves[0]) != 2 {
		t.Errorf("scheduleWaves(cycle) = %v, want a single wave with both tables", waves)
	}
}

func TestBuildInsert(t *testing.T) {
	got := buildInsert("logs", []string{"path", "trace_id"}, 2)
	want := `INSERT INTO "logs" ("path","trace_id") VALUES ($1,$2),($3,$4) ON CONFLICT DO NOTHING`
	if got != want {
		t.Errorf("buildInsert() = %q, want %q", got, want)
	}
	if got := buildInsert(`we"ird`, []string{`co"l`}, 1); !strings.Contains(got, `"we""ird"`) || !strings.Contains(got, `"co""l"`) {
		t.Errorf("buildInsert() = %q, want escaped identifiers", got)
	}
}

func TestQuoteHelpers(t *testing.T) {
	if got := quoteIdent("logs"); got != `"logs"` {
		t.Errorf("quoteIdent(logs) = %q", got)
	}
	if got := quoteIdent(`we"ird`); got != `"we""ird"` {
		t.Errorf("quoteIdent() = %q, want escaped quotes", got)
	}
	if got := quoteQualified("public.logs"); got != `"public"."logs"` {
		t.Errorf("quoteQualified(public.logs) = %q", got)
	}
	if got := quoteQualified("logs"); got != `"logs"` {
		t.Errorf("quoteQualified(logs) = %q", got)
	}
}

func TestContainsFold(t *testing.T) {
	values := []string{"logs", " Users "}
	if !containsFold(values, "users") || !containsFold(values, "LOGS") {
		t.Error("containsFold() = false, want a case-insensitive match")
	}
	if containsFold(values, "api_tokens") {
		t.Error("containsFold() = true for a missing value")
	}
}

func TestRowKey(t *testing.T) {
	got := rowKey([]string{"path", "trace_id"}, []any{"/a.http", "trace-1"})
	if got != "path=/a.http,trace_id=trace-1" {
		t.Errorf("rowKey() = %q", got)
	}
	if got := rowKey([]string{"a", "b", "c", "d"}, []any{1, 2, 3, 4}); got != "a=1,b=2,c=3" {
		t.Errorf("rowKey() = %q, want at most three columns", got)
	}
}

func TestIsRecoverableRowError(t *testing.T) {
	if !isRecoverableRowError(&pq.Error{Code: "23505"}) {
		t.Error("a unique violation must be recoverable per row")
	}
	if !isRecoverableRowError(&pq.Error{Code: "22001"}) {
		t.Error("a data exception must be recoverable per row")
	}
	if isRecoverableRowError(&pq.Error{Code: "08006"}) {
		t.Error("a connection failure must not be treated as a row error")
	}
	if isRecoverableRowError(&pq.Error{Code: ""}) {
		t.Error("an empty code must not be recoverable")
	}
	if isRecoverableRowError(fmt.Errorf("plain error")) {
		t.Error("a non-pq error must not be recoverable")
	}
	if !isUniqueViolation(&pq.Error{Code: "23505"}) || isUniqueViolation(&pq.Error{Code: "22001"}) {
		t.Error("isUniqueViolation() must only match 23505")
	}
}

func TestDiscoverLegacySQLiteFiles(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, "llm_tracelab.sqlite3")
	newer := filepath.Join(dir, "trajecta.sqlite3")
	other := filepath.Join(dir, "unrelated.sqlite3")
	for _, path := range []string{older, newer, other} {
		if err := os.WriteFile(path, []byte("sqlite"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}

	found := DiscoverLegacySQLiteFiles([]string{dir, dir, ""})
	if len(found) != 2 {
		t.Fatalf("DiscoverLegacySQLiteFiles() = %+v, want 2 candidates", found)
	}
	if found[0].Path != newer {
		t.Errorf("first candidate = %s, want the newest database %s", found[0].Path, newer)
	}
	if found[1].Path != older {
		t.Errorf("second candidate = %s, want %s", found[1].Path, older)
	}
	if found[0].Size != int64(len("sqlite")) {
		t.Errorf("Size = %d, want %d", found[0].Size, len("sqlite"))
	}
	if len(DiscoverLegacySQLiteFiles(nil)) != 0 {
		t.Error("DiscoverLegacySQLiteFiles(nil) must not find anything")
	}
}

// TestMergeSQLiteIntoPostgresIntegration needs a disposable Postgres database.
// Set TRAJECTA_TEST_POSTGRES_DSN to run it; it applies the application schema
// and merges a synthetic legacy database.
func TestMergeSQLiteIntoPostgresIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TRAJECTA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TRAJECTA_TEST_POSTGRES_DSN to a disposable Postgres test database DSN")
	}
	if err := appdbmigrate.MigrateUp("postgres", dsn, 0); err != nil {
		t.Fatalf("MigrateUp(postgres) error = %v", err)
	}

	suffix := strings.ReplaceAll(t.Name(), "/", "_") + "_" + time.Now().UTC().Format("20060102150405.000000000")
	dbPath := filepath.Join(t.TempDir(), "legacy.sqlite3")
	source, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open(sqlite) error = %v", err)
	}
	defer source.Close()
	statements := []string{
		`CREATE TABLE logs (path TEXT PRIMARY KEY, trace_id TEXT NOT NULL, mod_time_ns INTEGER NOT NULL DEFAULT 0,
			file_size INTEGER NOT NULL DEFAULT 0, version TEXT NOT NULL DEFAULT '', request_id TEXT NOT NULL DEFAULT '',
			recorded_at datetime NOT NULL, model TEXT NOT NULL DEFAULT '', is_stream numeric NOT NULL DEFAULT 0,
			status_code INTEGER NOT NULL DEFAULT 0, prompt_tokens INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE parser_versions (parser TEXT NOT NULL, version TEXT NOT NULL, created_at datetime NOT NULL,
			PRIMARY KEY (parser, version))`,
		`CREATE TABLE schema_migrations (version TEXT NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := source.Exec(statement); err != nil {
			t.Fatalf("create fixture table: %v", err)
		}
	}
	inserts := []struct {
		statement string
		args      []any
	}{
		{`INSERT INTO logs (path, trace_id, version, recorded_at, model, is_stream, prompt_tokens) VALUES (?,?,?,?,?,?,?)`,
			[]any{"/vault/" + suffix + "a.http", "trace-" + suffix + "-a", "LLM_PROXY_V3", "2026-04-27 12:56:02.167889738 +0000 UTC m=+0.095068526", "gpt-x", 1, 11}},
		{`INSERT INTO logs (path, trace_id, version, recorded_at, model, is_stream) VALUES (?,?,?,?,?,?)`,
			[]any{"/vault/" + suffix + "b.http", "trace-" + suffix + "-b", "LLM_PROXY_V3", "2025-12-23T12:17:14.088863521Z", "gpt-y", 0}},
		{`INSERT INTO parser_versions (parser, version, created_at) VALUES (?,?,?)`,
			[]any{"openai-" + suffix, "1.0", "2026-01-02 03:04:05"}},
		{`INSERT INTO schema_migrations (version) VALUES (?)`, []any{"20260101000000"}},
	}
	for _, insert := range inserts {
		if _, err := source.Exec(insert.statement, insert.args...); err != nil {
			t.Fatalf("insert fixture row: %v", err)
		}
	}

	report, err := MergeSQLiteIntoPostgres(context.Background(), CopyOptions{
		SQLitePaths: []string{dbPath},
		PostgresDSN: dsn,
		BatchSize:   10,
		Workers:     2,
		VerifyKeys:  true,
		Analyze:     false,
		OpenMode:    OpenModeAuto,
	})
	if err != nil {
		t.Fatalf("MergeSQLiteIntoPostgres() error = %v", err)
	}
	if !report.OK() {
		t.Fatalf("report is not OK: %+v", report)
	}
	if report.Copied != 3 {
		t.Errorf("Copied = %d, want 3 (two logs and one parser version)", report.Copied)
	}
	if report.Duplicate != 0 || report.Failed != 0 || report.Missing != 0 {
		t.Errorf("report = %+v, want no duplicates, failures or missing keys", report)
	}

	pg, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open(postgres) error = %v", err)
	}
	defer pg.Close()
	var (
		recordedAt time.Time
		isStream   bool
	)
	row := pg.QueryRow(`SELECT recorded_at, is_stream FROM logs WHERE trace_id = $1`, "trace-"+suffix+"-a")
	if err := row.Scan(&recordedAt, &isStream); err != nil {
		t.Fatalf("Scan(logs) error = %v", err)
	}
	want := time.Date(2026, 4, 27, 12, 56, 2, 167889738, time.UTC).Round(time.Microsecond)
	if !recordedAt.UTC().Equal(want) {
		t.Errorf("recorded_at = %s, want %s (the monotonic suffix must be stripped and Postgres rounds to microseconds)", recordedAt.UTC(), want)
	}
	if !isStream {
		t.Error("is_stream = false, want true")
	}

	// The merge must be idempotent and must not create duplicate rows.
	second, err := MergeSQLiteIntoPostgres(context.Background(), CopyOptions{
		SQLitePaths: []string{dbPath},
		PostgresDSN: dsn,
		BatchSize:   10,
		Workers:     2,
		VerifyKeys:  true,
		OpenMode:    OpenModeAuto,
	})
	if err != nil {
		t.Fatalf("MergeSQLiteIntoPostgres(second) error = %v", err)
	}
	if second.Copied != 0 || second.Duplicate != 3 {
		t.Errorf("second run = %+v, want 0 copied and 3 duplicates", second)
	}
	if !second.OK() {
		t.Errorf("second run is not OK: %+v", second)
	}
	var count int
	if err := pg.QueryRow(`SELECT count(*) FROM logs WHERE trace_id LIKE $1`, "trace-"+suffix+"-%").Scan(&count); err != nil {
		t.Fatalf("Scan(count) error = %v", err)
	}
	if count != 2 {
		t.Errorf("logs rows = %d, want 2", count)
	}

	// VerifyOnly must confirm the rows are present without writing anything.
	verify, err := MergeSQLiteIntoPostgres(context.Background(), CopyOptions{
		SQLitePaths: []string{dbPath},
		PostgresDSN: dsn,
		BatchSize:   10,
		Workers:     2,
		VerifyOnly:  true,
		OpenMode:    OpenModeAuto,
	})
	if err != nil {
		t.Fatalf("MergeSQLiteIntoPostgres(verify only) error = %v", err)
	}
	if !verify.OK() || verify.TablesSkip != 0 {
		t.Errorf("verify only report = %+v, want every table verified", verify)
	}
}
