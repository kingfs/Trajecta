package monitor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/appdbmigrate"
	"github.com/kingfs/Trajecta/internal/store"
)

// TestPostgresSystemDatabaseAPI exercises the database panel against a real
// Postgres server. It is skipped unless TRAJECTA_TEST_POSTGRES_DSN points at an
// instance the caller may create a scratch database on, which is the same
// convention every other Postgres integration test in the repository uses.
//
// It never touches an existing database: the scratch database is created for
// this test and dropped afterwards.
func TestPostgresSystemDatabaseAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Postgres integration test in short mode")
	}
	dsn := strings.TrimSpace(os.Getenv("TRAJECTA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TRAJECTA_TEST_POSTGRES_DSN to run the Postgres system-panel integration test")
	}
	ctx := context.Background()

	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		t.Fatalf("connect to the Postgres test instance: %v", err)
	}

	name := fmt.Sprintf("trajecta_systemtest_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
		_ = admin.Close()
		t.Fatalf("drop scratch database: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+name); err != nil {
		_ = admin.Close()
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() {
		defer admin.Close()
		if _, err := admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
			t.Logf("drop scratch database %s: %v", name, err)
		}
	})

	scratchDSN, err := systemPostgresDSNWithDatabase(dsn, name)
	if err != nil {
		t.Fatalf("build scratch dsn: %v", err)
	}
	if err := appdbmigrate.MigrateUp("postgres", scratchDSN, 0); err != nil {
		t.Fatalf("MigrateUp(scratch) error = %v", err)
	}
	st, err := store.NewWithDatabaseOptions(t.TempDir(), "postgres", scratchDSN, 4, 4, store.DatabaseOptions{AutoMigrate: false})
	if err != nil {
		t.Fatalf("NewWithDatabaseOptions(postgres) error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	manager := testJWTManager(t, time.Hour)
	token := systemTestToken(t, manager, 1, "admin", "admin")
	mux := http.NewServeMux()
	RegisterRoutes(mux, st, RouteOptions{MonitorAuthVerifier: manager, MonitorJWT: manager})

	rr := systemTestRequest(mux, "/api/system/db", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/system/db = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var payload store.SystemDatabaseSnapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode database payload: %v", err)
	}
	if !payload.Supported || payload.Unsupported {
		t.Fatalf("supported/unsupported = %v/%v, want a supported Postgres snapshot: %+v", payload.Supported, payload.Unsupported, payload.Warnings)
	}
	if len(payload.Warnings) > 0 {
		t.Logf("snapshot warnings: %v", payload.Warnings)
	}
	if payload.Driver != "postgres" {
		t.Fatalf("driver = %q, want postgres", payload.Driver)
	}
	if payload.Server == nil || strings.TrimSpace(payload.Server.Version) == "" {
		t.Fatalf("server version was not reported: %+v", payload.Server)
	}
	if payload.Database == nil || payload.Database.Name != name {
		t.Fatalf("database counters = %+v, want the scratch database %q", payload.Database, name)
	}
	if payload.Database.SizeBytes <= 0 {
		t.Fatalf("database size = %d, want a positive size", payload.Database.SizeBytes)
	}
	if payload.Database.CacheHitRatio < 0 || payload.Database.CacheHitRatio > 1 {
		t.Fatalf("cache hit ratio = %v, want a 0..1 fraction", payload.Database.CacheHitRatio)
	}
	if payload.Activity == nil {
		t.Fatal("activity was not reported")
	}
	if payload.Activity.Sessions < 1 {
		t.Fatalf("sessions = %d, want at least this test's connection", payload.Activity.Sessions)
	}
	if len(payload.Activity.ByState) == 0 {
		t.Fatal("activity by state is empty")
	}
	if len(payload.Relations) == 0 {
		t.Fatal("no relations were reported for a migrated database")
	}
	if payload.Indexes == nil {
		t.Fatal("index statistics were not reported")
	}
	if payload.Checkpointer == nil {
		t.Fatalf("checkpointer statistics were not reported: %+v", payload.Warnings)
	}
	if payload.Checkpointer.Source == "" {
		t.Fatalf("checkpointer source is empty: %+v", payload.Checkpointer)
	}
	settings := map[string]string{}
	for _, setting := range payload.Settings {
		settings[setting.Name] = setting.Value
	}
	for _, want := range []string{"shared_buffers", "work_mem", "effective_cache_size", "random_page_cost", "max_connections"} {
		if _, ok := settings[want]; !ok {
			t.Fatalf("setting %q missing from the snapshot: %+v", want, settings)
		}
	}
	if settings["shared_buffers"] == "" {
		t.Fatalf("shared_buffers has an empty value: %+v", payload.Settings)
	}
	// SET LOCAL inside the read-only transaction must have been accepted; a
	// warning here means the page's own timeout is not actually applied.
	for _, warning := range payload.Warnings {
		if strings.Contains(warning, "statement timeout") {
			t.Fatalf("the read-only transaction could not set a statement timeout: %s", warning)
		}
	}
}

// systemPostgresDSNWithDatabase points a DSN at another database on the same
// server. It is a local copy of the helper the legacymigrate tests use, because
// that one lives in a test file of another package.
func systemPostgresDSNWithDatabase(dsn string, database string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	parsed.Path = "/" + database
	return parsed.String(), nil
}
