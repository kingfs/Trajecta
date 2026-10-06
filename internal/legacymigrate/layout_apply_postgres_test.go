package legacymigrate

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
)

// TestPostgresPathIndex exercises postgresPathIndex against a real Postgres
// server. It is skipped unless TRAJECTA_TEST_POSTGRES_DSN points at an instance
// the caller may create a scratch database on; the normal unit suite needs no
// database.
func TestPostgresPathIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Postgres integration test in short mode")
	}
	dsn := strings.TrimSpace(os.Getenv("TRAJECTA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TRAJECTA_TEST_POSTGRES_DSN to run the Postgres path index integration test")
	}
	ctx := context.Background()

	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	if err := admin.PingContext(ctx); err != nil {
		t.Fatalf("connect to %s: %v", dsn, err)
	}

	name := fmt.Sprintf("trajecta_layouttest_%d", os.Getpid())
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

	// An empty database must be refused with the schema hint.
	if _, err := openPostgresPathIndex(ctx, scratchDSN); err == nil || !strings.Contains(err.Error(), "server db migrate up") {
		t.Fatalf("openPostgresPathIndex() error = %v, want a schema hint", err)
	}

	scratch, err := sql.Open("postgres", scratchDSN)
	if err != nil {
		t.Fatalf("open scratch connection: %v", err)
	}
	defer scratch.Close()
	for _, statement := range []string{
		`CREATE TABLE logs (path character varying PRIMARY KEY, trace_id character varying NOT NULL)`,
		`CREATE TABLE upstream_exchanges (id bigserial PRIMARY KEY, cassette_path character varying)`,
		`CREATE TABLE overview_metric_bucket_members (path character varying PRIMARY KEY, bucket_id character varying)`,
		`INSERT INTO logs (path, trace_id) VALUES ('/app/data/traces/a/2026/01/02/x.http', 'trace-a')`,
		`INSERT INTO upstream_exchanges (cassette_path) VALUES ('/app/data/traces/a/2026/01/02/x.http')`,
		`INSERT INTO overview_metric_bucket_members (path, bucket_id) VALUES ('/app/data/traces/a/2026/01/02/x.http', 'b1')`,
		`INSERT INTO logs (path, trace_id) VALUES ('/app/data/traces/taken/2026/01/02/y.http', 'trace-taken')`,
	} {
		if _, err := scratch.ExecContext(ctx, statement); err != nil {
			t.Fatalf("setup %q: %v", statement, err)
		}
	}

	index, err := openPostgresPathIndex(ctx, scratchDSN)
	if err != nil {
		t.Fatalf("openPostgresPathIndex() error = %v", err)
	}
	defer index.Close()

	oldPath := "/app/data/traces/a/2026/01/02/x.http"
	newPath := "/app/data/traces/unknown-site/a/2026/01/02/x.http"
	refs, err := index.CountRefs(ctx, oldPath)
	if err != nil {
		t.Fatalf("CountRefs() error = %v", err)
	}
	if refs != (PathRefs{Logs: 1, Exchanges: 1, OverviewMembers: 1}) {
		t.Fatalf("CountRefs() = %+v, want one row in each table", refs)
	}

	refs, err = index.MovePath(ctx, oldPath, newPath)
	if err != nil {
		t.Fatalf("MovePath() error = %v", err)
	}
	if refs.Rows() != 3 {
		t.Fatalf("MovePath() = %+v, want three repointed rows", refs)
	}
	var traceID string
	if err := scratch.QueryRowContext(ctx, `SELECT trace_id FROM logs WHERE path = $1`, newPath).Scan(&traceID); err != nil {
		t.Fatalf("read repointed logs row: %v", err)
	}
	if traceID != "trace-a" {
		t.Fatalf("trace_id = %q, want it preserved", traceID)
	}
	for table, column := range map[string]string{
		"upstream_exchanges":             "cassette_path",
		"overview_metric_bucket_members": "path",
	} {
		var count int
		if err := scratch.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s = $1`, table, column), newPath).Scan(&count); err != nil {
			t.Fatalf("count %s rows: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("%s.%s rows at the new path = %d, want 1", table, column, count)
		}
	}

	// A target that another row already claims must fail the whole transaction.
	conflict := "/app/data/traces/taken/2026/01/02/y.http"
	if _, err := index.MovePath(ctx, newPath, conflict); err == nil || !strings.Contains(err.Error(), "already has a row") {
		t.Fatalf("MovePath() conflict error = %v, want a unique violation", err)
	}
	var rows int
	if err := scratch.QueryRowContext(ctx, `SELECT count(*) FROM upstream_exchanges WHERE cassette_path = $1`, conflict).Scan(&rows); err != nil {
		t.Fatalf("count conflicting exchange rows: %v", err)
	}
	refs, err = index.CountRefs(ctx, newPath)
	if err != nil {
		t.Fatalf("CountRefs() after conflict error = %v", err)
	}
	if rows != 0 || refs != (PathRefs{Logs: 1, Exchanges: 1, OverviewMembers: 1}) {
		t.Fatalf("a failed move changed rows: conflict rows = %d, refs = %+v", rows, refs)
	}
}

// dsnWithDatabase points a DSN at another database on the same server.
func dsnWithDatabase(dsn, database string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	parsed.Path = "/" + database
	return parsed.String(), nil
}
