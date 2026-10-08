package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kingfs/Trajecta/internal/appdbmigrate"
)

// fixedConnector hands out one prepared connection, which is all the wrapper
// tests below need.
type fixedConnector struct {
	conn driver.Conn
	drv  driver.Driver
}

func (c fixedConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c fixedConnector) Driver() driver.Driver                        { return c.drv }

// beginLessConn exposes only driver.Conn, standing in for a driver that
// predates driver.ConnBeginTx by hiding the optional interfaces of a real
// connection behind an interface field.
type beginLessConn struct {
	driver.Conn
}

// TestObservedConnectionKeepsTransactionSupportOfTheWrappedDriver pins the
// wrapper's contract: it must not downgrade a driver that can begin a
// transaction with a context, and it must not claim that ability for a driver
// that cannot, because database/sql's own fallback also checks the context and
// reports the non-default-isolation and read-only errors.
func TestObservedConnectionKeepsTransactionSupportOfTheWrappedDriver(t *testing.T) {
	dsn := sqliteDSN(filepath.Join(t.TempDir(), "begin.sqlite3"))
	base, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open(sqlite) error = %v", err)
	}
	if err := base.Ping(); err != nil {
		_ = base.Close()
		t.Fatalf("ping sqlite: %v", err)
	}
	raw, err := observedConnector(base.Driver(), dsn)
	if err != nil {
		_ = base.Close()
		t.Fatalf("observedConnector() error = %v", err)
	}
	driverImpl := base.Driver()
	_ = base.Close()

	conn, err := (&slowQueryConnector{base: raw, collector: newSlowQueryCollector()}).Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	beginner, ok := conn.(driver.ConnBeginTx)
	if !ok {
		t.Fatal("the observed connection dropped driver.ConnBeginTx for a driver that implements it")
	}
	tx, err := beginner.BeginTx(context.Background(), driver.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	// The same wrapper over a connection without ConnBeginTx.
	hidden, err := raw.Connect(context.Background())
	if err != nil {
		t.Fatalf("raw Connect() error = %v", err)
	}
	plainConnector := fixedConnector{conn: beginLessConn{Conn: hidden}, drv: driverImpl}
	plain, err := (&slowQueryConnector{base: plainConnector, collector: newSlowQueryCollector()}).Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if _, ok := plain.(driver.ConnBeginTx); ok {
		t.Fatal("the observed connection claims driver.ConnBeginTx for a driver that does not implement it")
	}

	// End to end: database/sql keeps its own refusal, rather than the wrapper
	// inventing one.
	db := sql.OpenDB(&slowQueryConnector{base: plainConnector, collector: newSlowQueryCollector()})
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("BeginTx(read-only) error = %v, want database/sql's own refusal", err)
	}
}

// observedSQLiteDB opens a SQLite database whose connections record into an
// isolated collector, so the collector tests do not depend on the process-wide
// switch or on other tests running at the same time.
func observedSQLiteDB(t *testing.T, collector *slowQueryCollector) *sql.DB {
	t.Helper()

	dsn := sqliteDSN(filepath.Join(t.TempDir(), "observed.sqlite3"))
	base, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open(sqlite) error = %v", err)
	}
	connector, err := observedConnector(base.Driver(), dsn)
	if err != nil {
		_ = base.Close()
		t.Fatalf("observedConnector() error = %v", err)
	}
	_ = base.Close()
	db := sql.OpenDB(&slowQueryConnector{base: connector, collector: collector})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSlowQueryCollectorRecordsNothingWhenDisabled(t *testing.T) {
	collector := newSlowQueryCollector() // threshold 0 == off, the default
	if collector.enabled() {
		t.Fatal("a fresh collector is armed; the debug switch must default to off")
	}
	db := observedSQLiteDB(t, collector)

	// Deliberately expensive so the test cannot pass merely because the
	// statement was too fast to cross the threshold.
	var count int
	if err := db.QueryRow(`WITH RECURSIVE counter(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM counter WHERE x < 200000) SELECT count(*) FROM counter`).Scan(&count); err != nil {
		t.Fatalf("slow query error = %v", err)
	}
	if count != 200000 {
		t.Fatalf("recursive count = %d, want 200000", count)
	}
	if got := collector.snapshot(0); len(got) != 0 {
		t.Fatalf("disabled collector recorded %d statements: %+v", len(got), got)
	}
}

func TestSlowQueryCollectorSkipsStatementsUnderTheThreshold(t *testing.T) {
	collector := newSlowQueryCollector()
	collector.setThreshold(time.Hour)
	db := observedSQLiteDB(t, collector)

	var value int
	if err := db.QueryRow(`SELECT 1`).Scan(&value); err != nil {
		t.Fatalf("query error = %v", err)
	}
	if got := collector.snapshot(0); len(got) != 0 {
		t.Fatalf("collector recorded a statement under the threshold: %+v", got)
	}
}

func TestSlowQueryCollectorRecordsSanitizedStatement(t *testing.T) {
	collector := newSlowQueryCollector()
	collector.setThreshold(time.Nanosecond)
	db := observedSQLiteDB(t, collector)

	const statement = "SELECT   1,\n\t'api_key=supersecretvalue', 'sk-abcdefghijklmnopqrstuvwxyz'"
	var first int
	var second, third string
	if err := db.QueryRow(statement).Scan(&first, &second, &third); err != nil {
		t.Fatalf("query error = %v", err)
	}
	if first != 1 || second != "api_key=supersecretvalue" || third != "sk-abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("query returned unexpected values: %d %q %q", first, second, third)
	}

	items := collector.snapshot(0)
	if len(items) != 1 {
		t.Fatalf("recorded %d statements, want 1: %+v", len(items), items)
	}
	item := items[0]
	if item.Operation != "query" {
		t.Fatalf("operation = %q, want query", item.Operation)
	}
	if item.At.IsZero() {
		t.Fatal("recorded statement has no timestamp")
	}
	if item.DurationMs <= 0 {
		t.Fatalf("duration_ms = %v, want a positive duration", item.DurationMs)
	}
	if strings.Contains(item.Statement, "supersecretvalue") || strings.Contains(item.Statement, "sk-abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("recorded statement kept credential material: %q", item.Statement)
	}
	if !strings.Contains(item.Statement, "REDACTED") {
		t.Fatalf("recorded statement was not redacted: %q", item.Statement)
	}
	if strings.ContainsAny(item.Statement, "\n\t") || strings.Contains(item.Statement, "  ") {
		t.Fatalf("recorded statement kept raw whitespace: %q", item.Statement)
	}
	if !strings.HasPrefix(item.Statement, "SELECT 1, ") {
		t.Fatalf("recorded statement = %q, want the collapsed SQL", item.Statement)
	}
}

func TestSlowQueryCollectorRecordsExecAsExec(t *testing.T) {
	collector := newSlowQueryCollector()
	collector.setThreshold(time.Nanosecond)
	db := observedSQLiteDB(t, collector)

	if _, err := db.Exec(`CREATE TABLE sample (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("exec error = %v", err)
	}
	items := collector.snapshot(0)
	if len(items) != 1 || items[0].Operation != "exec" {
		t.Fatalf("recorded = %+v, want one exec record", items)
	}
}

func TestSlowQueryCollectorKeepsTheNewestStatements(t *testing.T) {
	collector := newSlowQueryCollector()
	collector.setThreshold(time.Nanosecond)
	for i := 0; i < slowQueryCapacity+10; i++ {
		collector.record("exec", "SELECT "+strconv.Itoa(i), time.Millisecond)
	}
	items := collector.snapshot(0)
	if len(items) != slowQueryCapacity {
		t.Fatalf("ring holds %d statements, want %d", len(items), slowQueryCapacity)
	}
	if items[0].Statement != "SELECT "+strconv.Itoa(slowQueryCapacity+9) {
		t.Fatalf("newest statement = %q, want the last one recorded", items[0].Statement)
	}
	if last := items[len(items)-1].Statement; last != "SELECT 10" {
		t.Fatalf("oldest retained statement = %q, want SELECT 10", last)
	}
	capped := collector.snapshot(5)
	if len(capped) != 5 || capped[0].Statement != items[0].Statement {
		t.Fatalf("snapshot(5) = %+v, want the five newest", capped)
	}
}

func TestSanitizeSlowQueryTruncatesOnARuneBoundary(t *testing.T) {
	long := "SELECT '" + strings.Repeat("é", 400) + "'"
	out := sanitizeSlowQuery(long)
	if len(out) > slowQueryStatementLimit+len("…") {
		t.Fatalf("sanitized length = %d, want at most %d", len(out), slowQueryStatementLimit+len("…"))
	}
	if !utf8.ValidString(out) {
		t.Fatalf("sanitized statement is not valid UTF-8: %q", out)
	}
	if !strings.HasSuffix(out, "…") {
		t.Fatalf("truncated statement = %q, want an ellipsis suffix", out)
	}
}

func TestSanitizeSlowQueryCollapsesWhitespaceAndKeepsShortSQL(t *testing.T) {
	if got := sanitizeSlowQuery("  SELECT\n\t a,\r\n b  FROM  t  "); got != "SELECT a, b FROM t" {
		t.Fatalf("sanitizeSlowQuery() = %q", got)
	}
	if got := sanitizeSlowQuery(""); got != "" {
		t.Fatalf("sanitizeSlowQuery(\"\") = %q, want empty", got)
	}
}

func TestStorePoolStatsAndDriverFacts(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if got := st.DriverName(); got != "sqlite" {
		t.Fatalf("DriverName() = %q, want sqlite", got)
	}
	if st.IsPostgresDriver() {
		t.Fatal("IsPostgresDriver() = true for a SQLite store")
	}
	stats := st.PoolStats()
	if stats.MaxOpenConnections != 4 {
		t.Fatalf("MaxOpenConnections = %d, want the 4 the store was opened with", stats.MaxOpenConnections)
	}

	var nilStore *Store
	if got := nilStore.DriverName(); got != "" {
		t.Fatalf("nil DriverName() = %q, want empty", got)
	}
	if nilStore.IsPostgresDriver() {
		t.Fatal("nil IsPostgresDriver() = true")
	}
	if got := nilStore.PoolStats(); got.MaxOpenConnections != 0 {
		t.Fatalf("nil PoolStats() = %+v, want the zero value", got)
	}
}

func TestStoreSlowQuerySnapshotReflectsTheProcessSwitch(t *testing.T) {
	t.Cleanup(func() {
		SetSlowQueryThreshold(0)
		ResetSlowQueries()
	})
	SetSlowQueryThreshold(0)
	ResetSlowQueries()

	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	disabled := st.SlowQuerySnapshot(systemDatabaseListLimit)
	if disabled.Enabled {
		t.Fatal("snapshot reports enabled while the threshold is zero")
	}
	if disabled.ThresholdMs != 0 {
		t.Fatalf("threshold_ms = %v, want 0", disabled.ThresholdMs)
	}
	if disabled.Capacity != slowQueryCapacity {
		t.Fatalf("capacity = %d, want %d", disabled.Capacity, slowQueryCapacity)
	}
	if len(disabled.Items) != 0 {
		t.Fatalf("disabled snapshot has %d items", len(disabled.Items))
	}

	SetSlowQueryThreshold(time.Nanosecond)
	ResetSlowQueries()
	var value int
	if err := st.db.QueryRowContext(context.Background(), `SELECT 1`).Scan(&value); err != nil {
		t.Fatalf("query error = %v", err)
	}
	enabled := st.SlowQuerySnapshot(systemDatabaseListLimit)
	if !enabled.Enabled {
		t.Fatal("snapshot reports disabled after the threshold was armed")
	}
	if enabled.ThresholdMs <= 0 {
		t.Fatalf("threshold_ms = %v, want a positive threshold", enabled.ThresholdMs)
	}
	if len(enabled.Items) == 0 {
		t.Fatal("the store's own statement was not recorded through the observed pool")
	}
	if enabled.Items[0].Statement != "SELECT 1" {
		t.Fatalf("recorded statement = %q, want SELECT 1", enabled.Items[0].Statement)
	}
}

func TestSystemDatabaseSnapshotIsUnsupportedOutsidePostgres(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	snapshot, err := st.SystemDatabaseSnapshot(context.Background())
	if err != nil {
		t.Fatalf("SystemDatabaseSnapshot() error = %v", err)
	}
	if snapshot.Supported || !snapshot.Unsupported {
		t.Fatalf("snapshot = supported %v / unsupported %v, want a structured unsupported answer", snapshot.Supported, snapshot.Unsupported)
	}
	if snapshot.Driver != "sqlite" {
		t.Fatalf("driver = %q, want sqlite", snapshot.Driver)
	}
	if !strings.Contains(snapshot.Reason, "PostgreSQL") {
		t.Fatalf("reason = %q, want it to name PostgreSQL", snapshot.Reason)
	}
	if snapshot.Relations == nil || snapshot.Tables == nil || snapshot.Settings == nil || snapshot.Warnings == nil {
		t.Fatalf("unsupported snapshot has nil lists: %+v", snapshot)
	}
}

// TestPostgresCheckpointerTimesAreCastToIntegers pins the bug a live Postgres 17
// found: pg_stat_checkpointer.write_time and pg_stat_bgwriter.checkpoint_write_time
// are double precision, so scanning them straight into an int64 fails once the
// value is large enough for Go to format it in scientific notation. The failure
// was silent - the checkpointer section simply came back absent with a warning -
// and it did not reproduce in tests, because a fresh database reports 0, which
// parses as an integer.
//
// The test drives the exact expressions the collector's queries use, so removing
// the round()::bigint casts fails here rather than on an operator's page.
func TestPostgresCheckpointerTimesAreCastToIntegers(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TRAJECTA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TRAJECTA_TEST_POSTGRES_DSN to a disposable Postgres test database DSN")
	}
	// A served store refuses to open unless the versioned migrations have been
	// applied, so bring the schema up first exactly as the other Postgres tests do.
	if err := appdbmigrate.MigrateUp("postgres", dsn, 0); err != nil {
		t.Fatalf("MigrateUp(postgres) error = %v", err)
	}
	st, err := NewWithDatabaseOptions(t.TempDir(), "postgres", dsn, 2, 2, DatabaseOptions{AutoMigrate: false})
	if err != nil {
		t.Fatalf("NewWithDatabaseOptions(postgres) error = %v", err)
	}
	defer st.Close()

	// 70169111 is the write_time the reference deployment reported; Go formats it
	// as 7.0169111e+07, which strconv.ParseInt rejects.
	const largeMs = 70169111.0

	var casted int64
	if err := st.db.QueryRow(`SELECT round(?::double precision)::bigint`, largeMs).Scan(&casted); err != nil {
		t.Fatalf("the cast the collector relies on failed: %v", err)
	}
	if casted != int64(largeMs) {
		t.Fatalf("casted value = %d, want %d", casted, int64(largeMs))
	}

	// The uncast shape is what failed, so the test states the failure it guards
	// against rather than only the happy path.
	var uncast int64
	if err := st.db.QueryRow(`SELECT ?::double precision`, largeMs).Scan(&uncast); err == nil {
		t.Fatalf("scanning a large double precision into int64 unexpectedly succeeded (%d); if database/sql changed, this guard needs a different assertion", uncast)
	}
}
