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

// TestSecondaryUniqueKeyVerificationIntegration proves that the archive gate
// mirrors the copy's real skip semantics. The merge inserts with
// ON CONFLICT DO NOTHING, so a legacy row is skipped as soon as any unique key
// matches; a running server that created the row first therefore leaves the
// legacy row absent under a regenerated surrogate primary key while the data is
// present. The gate must find it through the natural unique key, report it as a
// secondary-key match, and still refuse for rows that are genuinely absent. It
// must also ignore partial and expression unique indexes.
//
// It is skipped unless TRAJECTA_TEST_POSTGRES_DSN points at an instance the
// caller may create a scratch database on.
func TestSecondaryUniqueKeyVerificationIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Postgres integration test in short mode")
	}
	dsn := strings.TrimSpace(os.Getenv("TRAJECTA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TRAJECTA_TEST_POSTGRES_DSN to run the secondary unique key integration test")
	}
	ctx := context.Background()

	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	if err := admin.PingContext(ctx); err != nil {
		t.Fatalf("connect to %s: %v", dsn, err)
	}
	name := fmt.Sprintf("trajecta_keytest_%d", os.Getpid())
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
	for _, statement := range []string{
		`CREATE TABLE drift_cases (id bigint PRIMARY KEY, natural_key text NOT NULL, payload text NOT NULL)`,
		`CREATE UNIQUE INDEX drift_cases_natural_key ON drift_cases (natural_key)`,
		// A partial unique index and an expression unique index must both be
		// ignored: neither can serve as a general existence key.
		`CREATE UNIQUE INDEX drift_cases_partial ON drift_cases (payload) WHERE payload <> ''`,
		`CREATE UNIQUE INDEX drift_cases_expression ON drift_cases (lower(natural_key))`,
		// The running server owns the row already, under a regenerated key.
		`INSERT INTO drift_cases (id, natural_key, payload) VALUES (1, 'admin', 'server-owned')`,
	} {
		if _, err := pg.ExecContext(ctx, statement); err != nil {
			t.Fatalf("setup %q: %v", statement, err)
		}
	}

	dbPath := filepath.Join(t.TempDir(), "legacy.sqlite3")
	source, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open(sqlite) error = %v", err)
	}
	defer source.Close()
	for _, statement := range []string{
		`CREATE TABLE drift_cases (id INTEGER PRIMARY KEY, natural_key TEXT NOT NULL, payload TEXT NOT NULL)`,
		`INSERT INTO drift_cases (id, natural_key, payload) VALUES (4294967297, 'admin', 'legacy')`,
	} {
		if _, err := source.Exec(statement); err != nil {
			t.Fatalf("fixture %q: %v", statement, err)
		}
	}

	verify := func() *CopyReport {
		t.Helper()
		report, err := MergeSQLiteIntoPostgres(ctx, CopyOptions{
			SQLitePaths: []string{dbPath},
			PostgresDSN: scratchDSN,
			VerifyOnly:  true,
			OpenMode:    OpenModeAuto,
		})
		if err != nil {
			t.Fatalf("MergeSQLiteIntoPostgres(VerifyOnly) error = %v", err)
		}
		return report
	}

	matched := verify()
	result := snapshotTestTable(t, matched, "drift_cases")
	if result.MissingKeys != 0 {
		t.Errorf("MissingKeys = %d, want 0 (the row exists under natural_key)", result.MissingKeys)
	}
	if result.AltKeyMatched != 1 {
		t.Errorf("AltKeyMatched = %d, want 1", result.AltKeyMatched)
	}
	if !matched.OK() {
		t.Errorf("report is not OK although only a secondary key matched: %+v", matched)
	}
	wantKeys := []string{"primary key (id)", "unique (natural_key)"}
	if strings.Join(result.UniqueKeys, "|") != strings.Join(wantKeys, "|") {
		t.Errorf("UniqueKeys = %v, want %v (partial and expression indexes must be ignored)", result.UniqueKeys, wantKeys)
	}
	if len(result.SampleAltKeyMatches) == 0 || !strings.Contains(result.SampleAltKeyMatches[0], "unique (natural_key)") {
		t.Errorf("SampleAltKeyMatches = %v, want an entry naming the natural key", result.SampleAltKeyMatches)
	}

	// A row that exists under no key must still fail the gate.
	if _, err := source.Exec(`INSERT INTO drift_cases (id, natural_key, payload) VALUES (999, 'ghost', 'absent')`); err != nil {
		t.Fatalf("insert ghost row: %v", err)
	}
	gapped := verify()
	result = snapshotTestTable(t, gapped, "drift_cases")
	if result.MissingKeys != 1 {
		t.Errorf("MissingKeys = %d, want 1 (the ghost row is absent under every key)", result.MissingKeys)
	}
	if result.AltKeyMatched != 1 {
		t.Errorf("AltKeyMatched = %d, want 1", result.AltKeyMatched)
	}
	if result.Status != "partial" {
		t.Errorf("status = %q, want partial", result.Status)
	}
	if gapped.OK() {
		t.Error("report is OK although a source row is absent under every unique key")
	}
}
