package appdb

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/auth"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
	_ "modernc.org/sqlite"
)

func TestOpenAuthStoreAutoMigrateSQLiteCreatesAuthSchemaAndInitUser(t *testing.T) {
	dir := t.TempDir()
	autoMigrate := true
	cfg := &config.Config{}
	cfg.Trace.OutputDir = dir
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = filepath.Join(dir, "control.sqlite3")
	cfg.Database.AutoMigrate = &autoMigrate

	st, err := OpenAuthStore(cfg)
	if err != nil {
		t.Fatalf("OpenAuthStore() error = %v", err)
	}
	defer st.Close()

	if _, err := st.CreateUser(context.Background(), "admin", "correct horse battery staple"); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if err := st.VerifyPassword(context.Background(), "admin", "correct horse battery staple"); err != nil {
		t.Fatalf("VerifyPassword() error = %v", err)
	}
}

func TestOpenAuthStoreAutoMigratePostgresSkipsAuthMigrator(t *testing.T) {
	origMigrate := AuthMigrateDatabaseUp
	origOpen := AuthOpenDatabase
	t.Cleanup(func() {
		AuthMigrateDatabaseUp = origMigrate
		AuthOpenDatabase = origOpen
	})

	var migrated bool
	var openedDriver string
	var openedDSN string
	var openedMaxOpen int
	var openedMaxIdle int
	AuthMigrateDatabaseUp = func(driver string, dsn string, steps int) error {
		migrated = true
		return nil
	}
	AuthOpenDatabase = func(driver string, dsn string, maxOpenConns int, maxIdleConns int) (*auth.Store, error) {
		openedDriver = driver
		openedDSN = dsn
		openedMaxOpen = maxOpenConns
		openedMaxIdle = maxIdleConns
		return &auth.Store{}, nil
	}

	autoMigrate := true
	cfg := &config.Config{}
	cfg.Database.Driver = "postgresql"
	cfg.Database.DSN = "postgres://user:pass@example.invalid/trajecta?sslmode=disable"
	cfg.Database.MaxOpenConns = 7
	cfg.Database.MaxIdleConns = 3
	cfg.Database.AutoMigrate = &autoMigrate

	st, err := OpenAuthStore(cfg)
	if err != nil {
		t.Fatalf("OpenAuthStore() error = %v", err)
	}
	defer st.Close()

	if migrated {
		t.Fatalf("Postgres auth store called auth migrator; application migration should own auth tables")
	}
	if openedDriver != "postgres" || openedDSN != cfg.Database.DSN || openedMaxOpen != 7 || openedMaxIdle != 3 {
		t.Fatalf("OpenDatabase args = driver=%q dsn=%q maxOpen=%d maxIdle=%d", openedDriver, openedDSN, openedMaxOpen, openedMaxIdle)
	}
}

func TestOpenApplicationDatabaseAutoMigrateFalseDoesNotCreateSchema(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "trace_index.sqlite3")
	autoMigrate := false
	cfg := &config.Config{}
	cfg.Trace.OutputDir = dir
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = dbPath
	cfg.Database.AutoMigrate = &autoMigrate

	st, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer st.Close()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'logs'`).Scan(&count); err != nil {
		t.Fatalf("query sqlite_master error = %v", err)
	}
	if count != 0 {
		t.Fatalf("logs table count = %d, want 0 with auto_migrate=false", count)
	}
}

func TestOpenApplicationDatabasePassesSessionSummaryReadConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Trace.OutputDir = dir
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = filepath.Join(dir, "trace_index.sqlite3")
	useSummaryRead := true
	cfg.Database.UseSessionSummaryRead = &useSummaryRead

	st, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer st.Close()

	writeCLISessionSummaryTestLog(t, st, dir, "config-summary-read.http", "sess-config-summary-read", time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC), http.StatusOK, 12)
	// Settle the write path first: the poisoned value below is what proves the
	// read came from the summary table, so nothing may rewrite it afterwards.
	st.FlushDerivedRefresh()
	db, err := sql.Open("sqlite", filepath.Join(dir, "trace_index.sqlite3"))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE session_summaries SET request_count = 7 WHERE session_id = ?`, "sess-config-summary-read"); err != nil {
		t.Fatalf("poison session_summaries error = %v", err)
	}
	page, err := st.ListSessionPage(1, 10, store.ListFilter{})
	if err != nil {
		t.Fatalf("ListSessionPage() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].RequestCount != 7 {
		t.Fatalf("ListSessionPage() = %+v, want summary read request_count 7", page.Items)
	}
}

// writeSessionSummaryTestLog seeds one recorded trace with a session id, so the
// session_summaries read path has something to return.
func writeCLISessionSummaryTestLog(t *testing.T, st *store.Store, dir string, name string, sessionID string, recordedAt time.Time, statusCode int, totalTokens int) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:     name,
			Time:          recordedAt,
			Model:         "gpt-test",
			Provider:      "openai_compatible",
			Operation:     "responses",
			Endpoint:      "/v1/responses",
			URL:           "/v1/responses",
			Method:        "POST",
			StatusCode:    statusCode,
			DurationMs:    100,
			TTFTMs:        10,
			ClientIP:      "127.0.0.1",
			ContentLength: 4,
		},
		Usage: recordfile.UsageInfo{
			TotalTokens: totalTokens,
		},
	}
	if err := st.UpsertLogWithGrouping(path, header, store.GroupingInfo{
		SessionID:     sessionID,
		SessionSource: "header.session_id",
	}); err != nil {
		t.Fatalf("UpsertLogWithGrouping(%q) error = %v", path, err)
	}
}
