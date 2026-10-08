package store

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/appdbmigrate"
)

// TestIsReadOnlyStatement pins the routing rule for the split read pool. The rule
// has to stay narrow: only a statement that cannot write may leave the write
// pool, because a write sent to the read pool would be a silent behaviour change
// while a read sent to the write pool only loses its statement timeout.
func TestIsReadOnlyStatement(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  bool
	}{
		{"plain select", "SELECT 1", true},
		{"lowercase select", "select count(*) from logs", true},
		{"leading newline and tabs", "\n\t\tSELECT recorded_at FROM logs WHERE path = ?", true},
		{"leading line comment", "-- a note\nSELECT 1", true},
		{"leading block comment", "/* a note */ SELECT 1", true},
		{"multiple comments", "-- one\n/* two */\n  select 1", true},
		{"values", "VALUES (1), (2)", true},
		{"parenthesised select keyword", "SELECT(1)", true},
		{"insert", "INSERT INTO logs (path) VALUES (?)", false},
		{"insert returning via query", "INSERT INTO logs (path) VALUES (?) RETURNING id", false},
		{"update returning via query", "\n\tUPDATE parse_jobs SET status = 'running' WHERE id IN (\n\t\tSELECT id FROM parse_jobs\n\t\tORDER BY updated_at ASC\n\t\tLIMIT ?\n\t\tFOR UPDATE SKIP LOCKED\n\t)\n\tRETURNING id", false},
		{"delete", "DELETE FROM trace_findings WHERE trace_id = ?", false},
		{"data modifying cte", "WITH moved AS (DELETE FROM a RETURNING id) SELECT * FROM moved", false},
		{"read only cte stays on the write pool", "WITH recent AS (SELECT 1) SELECT * FROM recent", false},
		{"ddl", "CREATE INDEX IF NOT EXISTS x ON logs (path)", false},
		{"pragma", "PRAGMA foreign_keys", false},
		{"begin", "BEGIN", false},
		{"empty", "", false},
		{"whitespace only", "   \n\t ", false},
		{"unterminated block comment", "/* never closed", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isReadOnlyStatement(testCase.query); got != testCase.want {
				t.Fatalf("isReadOnlyStatement(%q) = %v, want %v", testCase.query, got, testCase.want)
			}
		})
	}
}

// TestQueryPoolPicksTheReadPoolForReadsOnly checks the pool the wrapper picks,
// without a server: two distinguishable databases and pointer identity.
func TestQueryPoolPicksTheReadPoolForReadsOnly(t *testing.T) {
	write := &sql.DB{}
	read := &sql.DB{}
	db := &rebindingDB{DB: write, driver: "postgres", readDB: read}

	if got := db.queryPool("SELECT 1"); got != read {
		t.Fatalf("a SELECT went to %p, want the read pool %p", got, read)
	}
	for _, query := range []string{
		"INSERT INTO logs (path) VALUES (?) RETURNING id",
		"UPDATE parse_jobs SET status = 'running' RETURNING id",
		"WITH x AS (SELECT 1) SELECT * FROM x",
	} {
		if got := db.queryPool(query); got != write {
			t.Fatalf("%q went to %p, want the write pool %p", query, got, write)
		}
	}
}

// TestQueryPoolFallsBackWithoutAReadPool keeps the single-pool behaviour for
// SQLite and every caller that did not configure one.
func TestQueryPoolFallsBackWithoutAReadPool(t *testing.T) {
	write := &sql.DB{}
	db := &rebindingDB{DB: write, driver: "sqlite"}
	if got := db.queryPool("SELECT 1"); got != write {
		t.Fatalf("queryPool() = %p, want the write pool %p", got, write)
	}
}

func TestOpenReadPoolIsSkippedForSQLite(t *testing.T) {
	pool, err := openReadPool("sqlite", "", ":memory:", t.TempDir(), DatabaseOptions{
		ReadMaxOpenConns:     4,
		ReadStatementTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("openReadPool(sqlite) error = %v", err)
	}
	if pool != nil {
		_ = pool.Close()
		t.Fatal("openReadPool(sqlite) opened a pool, want nil: SQLite serialises writers at the file level")
	}
}

func TestOpenReadPoolIsSkippedWithoutConfiguration(t *testing.T) {
	pool, err := openReadPool("postgres", "postgres://example/db", "", t.TempDir(), DatabaseOptions{})
	if err != nil {
		t.Fatalf("openReadPool() error = %v", err)
	}
	if pool != nil {
		_ = pool.Close()
		t.Fatal("openReadPool() opened a pool without a configured size or timeout")
	}
}

func TestDSNWithConnectionOption(t *testing.T) {
	got, err := dsnWithConnectionOption("postgres://u:p@h:5432/db?sslmode=disable", "statement_timeout", "60000")
	if err != nil {
		t.Fatalf("dsnWithConnectionOption() error = %v", err)
	}
	for _, want := range []string{"sslmode=disable", "statement_timeout=60000", "postgres://u:p@h:5432/db"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dsnWithConnectionOption() = %q, want it to contain %q", got, want)
		}
	}
	// A key/value DSN cannot be edited safely, and the error must not leak the
	// password.
	if _, err := dsnWithConnectionOption("host=h user=u password=secret dbname=db", "statement_timeout", "1"); err == nil {
		t.Fatal("dsnWithConnectionOption() accepted a key/value DSN, want an error")
	} else if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error %q leaks the password", err)
	}
}

// TestPostgresReadPoolAppliesStatementTimeoutAndLeavesWritesAlone is the part
// that only a server can prove: the option really reaches the read connections,
// and a writing statement issued through Query - the shape the job claims use -
// still runs on the pool that has no timeout.
func TestPostgresReadPoolAppliesStatementTimeoutAndLeavesWritesAlone(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TRAJECTA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TRAJECTA_TEST_POSTGRES_DSN to a disposable Postgres test database DSN")
	}
	if err := appdbmigrate.MigrateUp("postgres", dsn, 0); err != nil {
		t.Fatalf("MigrateUp(postgres) error = %v", err)
	}

	dir := t.TempDir()
	st, err := NewWithDatabaseOptions(dir, "postgres", dsn, 4, 4, DatabaseOptions{
		AutoMigrate:          false,
		ReadMaxOpenConns:     2,
		ReadMaxIdleConns:     2,
		ReadStatementTimeout: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWithDatabaseOptions(postgres) error = %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Logf("Close(postgres store) error = %v", err)
		}
	})
	if st.db.readDB == nil {
		t.Fatal("the store has no read pool")
	}

	// A read that outlives the timeout is cancelled by the server.
	var slept string
	err = st.db.QueryRow(`SELECT pg_sleep(2)`).Scan(&slept)
	if err == nil {
		t.Fatal("a 2s read succeeded on a pool configured with a 250ms statement_timeout")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "statement timeout") &&
		!strings.Contains(strings.ToLower(err.Error()), "canceling statement") {
		t.Fatalf("read error = %v, want a statement timeout", err)
	}

	// The write pool is untouched by that setting: a writing statement issued
	// through Query (the claim shape) completes even though it takes longer than
	// the read timeout.
	var one int
	if err := st.db.QueryRow(`SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("plain read through the write pool error = %v", err)
	}
	var written int
	if err := st.db.QueryRow(`INSERT INTO parse_jobs (trace_id, status, attempts, created_at, updated_at)
		VALUES (?, 'queued', 0, ?, ?) RETURNING 1`,
		"read-pool-test-"+time.Now().UTC().Format("20060102150405.000000000"),
		time.Now().UTC(), time.Now().UTC()).Scan(&written); err != nil {
		t.Fatalf("INSERT ... RETURNING through the write pool error = %v", err)
	}
}
