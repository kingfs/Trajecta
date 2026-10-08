package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/kingfs/Trajecta/internal/redaction"
)

// This file owns the read-only "what is this process doing" surface behind the
// Monitor's System page:
//
//   - the connection-pool counters of the store's pools (PoolStats);
//   - a bounded, opt-in in-memory ring of slow statements (SlowQueries);
//   - a read-only snapshot of the Postgres statistics views
//     (SystemDatabaseSnapshot).
//
// Nothing here writes to the database, and nothing here is on by default: the
// slow-query ring records only while SetSlowQueryThreshold has a positive
// value, and the Postgres snapshot runs inside a read-only transaction with a
// short statement timeout.

// ---------------------------------------------------------------------------
// Slow-query collector
// ---------------------------------------------------------------------------

const (
	// slowQueryCapacity bounds the in-memory ring. The page asks for 50.
	slowQueryCapacity = 50
	// slowQueryStatementLimit is the byte budget of one recorded statement.
	// Statements are collapsed and truncated so a pathological query cannot
	// pin megabytes of SQL text in memory or on the page.
	slowQueryStatementLimit = 500
	// slowQueryRawLimit bounds how much of a pg_stat_activity.query the
	// database panel is allowed to pull across the wire before the Go-side
	// sanitizer truncates it.
	systemQueryTextLimit = 4000
)

// SlowQueryRecord is one recorded statement. DurationMs is a float so the page
// can show sub-millisecond values without a unit switch.
type SlowQueryRecord struct {
	At         time.Time `json:"at"`
	Operation  string    `json:"operation"`
	DurationMs float64   `json:"duration_ms"`
	Statement  string    `json:"statement"`
}

// SlowQuerySnapshot is the payload behind GET /api/system/slow-queries.
type SlowQuerySnapshot struct {
	Driver      string            `json:"driver"`
	Enabled     bool              `json:"enabled"`
	ThresholdMs float64           `json:"threshold_ms"`
	Capacity    int               `json:"capacity"`
	Items       []SlowQueryRecord `json:"items"`
}

// slowQueryTokenPattern catches credential-shaped string literals that the
// generic redaction marker list does not name, e.g. a raw `sk-...` key pasted
// into a statement. It is deliberately narrow so it does not mangle ordinary
// SQL identifiers.
var slowQueryTokenPattern = regexp.MustCompile(`(?i)\b(?:sk|rk|pk|ghp|gho|ghs|ghu|xox[baprs])[-_][A-Za-z0-9_-]{10,}`)

// slowQueryCollector is a fixed-size ring of recent statements plus the
// threshold that arms it. A zero threshold means "record nothing", and the
// hot path checks that with a single atomic load.
type slowQueryCollector struct {
	thresholdNanos atomic.Int64

	mu     sync.Mutex
	items  []SlowQueryRecord
	next   int
	filled bool
}

func newSlowQueryCollector() *slowQueryCollector {
	return &slowQueryCollector{items: make([]SlowQueryRecord, 0, slowQueryCapacity)}
}

func (c *slowQueryCollector) setThreshold(threshold time.Duration) {
	if threshold < 0 {
		threshold = 0
	}
	c.thresholdNanos.Store(int64(threshold))
}

func (c *slowQueryCollector) threshold() time.Duration {
	if c == nil {
		return 0
	}
	return time.Duration(c.thresholdNanos.Load())
}

func (c *slowQueryCollector) enabled() bool {
	return c != nil && c.thresholdNanos.Load() > 0
}

func (c *slowQueryCollector) record(operation string, statement string, elapsed time.Duration) {
	if !c.enabled() || elapsed < c.threshold() {
		return
	}
	item := SlowQueryRecord{
		At:         time.Now().UTC(),
		Operation:  operation,
		DurationMs: float64(elapsed.Nanoseconds()) / float64(time.Millisecond),
		Statement:  sanitizeSlowQuery(statement),
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) < slowQueryCapacity {
		c.items = append(c.items, item)
		return
	}
	c.items[c.next] = item
	c.next = (c.next + 1) % slowQueryCapacity
	c.filled = true
}

// snapshot returns the newest-first contents of the ring, capped at limit
// (limit <= 0 means the whole ring).
func (c *slowQueryCollector) snapshot(limit int) []SlowQueryRecord {
	if c == nil {
		return []SlowQueryRecord{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	count := len(c.items)
	if count == 0 {
		return []SlowQueryRecord{}
	}
	out := make([]SlowQueryRecord, 0, count)
	if c.filled {
		for i := 0; i < count; i++ {
			out = append(out, c.items[(c.next+i)%count])
		}
	} else {
		out = append(out, c.items...)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (c *slowQueryCollector) reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = c.items[:0]
	c.next = 0
	c.filled = false
}

// processSlowQueries is the process-wide collector. It is process-wide rather
// than per-Store because the switch it reads comes from configuration, and the
// pools it instruments are opened before any Store-owned hook exists.
var processSlowQueries = newSlowQueryCollector()

// SetSlowQueryThreshold arms or disarms slow-statement recording. Zero (the
// default, and what an unset debug.slow_query_threshold produces) records
// nothing at all.
func SetSlowQueryThreshold(threshold time.Duration) {
	processSlowQueries.setThreshold(threshold)
}

// SlowQueryThreshold reports the armed threshold; zero means recording is off.
func SlowQueryThreshold() time.Duration {
	return processSlowQueries.threshold()
}

// SlowQueries returns up to limit recorded statements, newest first.
func SlowQueries(limit int) []SlowQueryRecord {
	return processSlowQueries.snapshot(limit)
}

// ResetSlowQueries drops every recorded statement. It exists for tests and for
// an operator who wants a clean window; it does not change the threshold.
func ResetSlowQueries() {
	processSlowQueries.reset()
}

// SlowQueries reports up to limit recorded statements, newest first.
func (s *Store) SlowQueries(limit int) []SlowQueryRecord {
	return SlowQueries(limit)
}

// SlowQueryThreshold reports the armed slow-statement threshold.
func (s *Store) SlowQueryThreshold() time.Duration {
	return SlowQueryThreshold()
}

// SlowQuerySnapshot is the /api/system/slow-queries payload for this store.
func (s *Store) SlowQuerySnapshot(limit int) SlowQuerySnapshot {
	threshold := SlowQueryThreshold()
	driverName := ""
	if s != nil {
		driverName = s.driver
	}
	items := SlowQueries(limit)
	if items == nil {
		items = []SlowQueryRecord{}
	}
	return SlowQuerySnapshot{
		Driver:      driverName,
		Enabled:     threshold > 0,
		ThresholdMs: float64(threshold.Nanoseconds()) / float64(time.Millisecond),
		Capacity:    slowQueryCapacity,
		Items:       items,
	}
}

// sanitizeSlowQuery makes one statement safe to hold in memory and render:
// whitespace is collapsed, credential-shaped text is redacted, and the result
// is truncated on a rune boundary.
func sanitizeSlowQuery(statement string) string {
	collapsed := strings.Join(strings.Fields(statement), " ")
	if collapsed == "" {
		return ""
	}
	collapsed = redaction.MetadataText(collapsed)
	collapsed = slowQueryTokenPattern.ReplaceAllString(collapsed, "REDACTED")
	if len(collapsed) > slowQueryStatementLimit {
		trimmed := collapsed[:slowQueryStatementLimit]
		for len(trimmed) > 0 && !utf8.ValidString(trimmed) {
			trimmed = trimmed[:len(trimmed)-1]
		}
		collapsed = trimmed + "…"
	}
	return collapsed
}

// ---------------------------------------------------------------------------
// driver-level recording
// ---------------------------------------------------------------------------

// openObservedDatabase opens driverName/dsn and returns a pool whose
// connections record statements into the process collector. The base
// *sql.DB is closed before it is ever used: sql.Open is lazy, so no connection
// is dropped by the swap, and every pool setting the caller applies afterwards
// lands on the observed pool.
func openObservedDatabase(driverName string, dsn string) (*sql.DB, error) {
	base, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	baseDriver := base.Driver()
	if baseDriver == nil {
		return base, nil
	}
	connector, err := observedConnector(baseDriver, dsn)
	if err != nil {
		_ = base.Close()
		return nil, err
	}
	_ = base.Close()
	return sql.OpenDB(&slowQueryConnector{base: connector, collector: processSlowQueries}), nil
}

func observedConnector(base driver.Driver, dsn string) (driver.Connector, error) {
	if driverContext, ok := base.(driver.DriverContext); ok {
		connector, err := driverContext.OpenConnector(dsn)
		if err != nil {
			return nil, err
		}
		return connector, nil
	}
	return dsnConnector{base: base, dsn: dsn}, nil
}

// dsnConnector is database/sql's own fallback connector shape, for a driver
// that only implements driver.Driver.
type dsnConnector struct {
	base driver.Driver
	dsn  string
}

func (c dsnConnector) Connect(context.Context) (driver.Conn, error) {
	return c.base.Open(c.dsn)
}

func (c dsnConnector) Driver() driver.Driver {
	return c.base
}

type slowQueryConnector struct {
	base      driver.Connector
	collector *slowQueryCollector
}

func (c *slowQueryConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	if c.collector == nil {
		return conn, nil
	}
	wrapped := &slowQueryConn{Conn: conn, collector: c.collector}
	// Only add BeginTx when the wrapped driver has it: database/sql checks the
	// returned connection's method set, so leaving it off keeps its own
	// context-aware fallback in charge instead of reimplementing it worse.
	if _, ok := conn.(driver.ConnBeginTx); ok {
		return &slowQueryConnTx{slowQueryConn: wrapped}, nil
	}
	return wrapped, nil
}

func (c *slowQueryConnector) Driver() driver.Driver {
	return c.base.Driver()
}

// slowQueryConn wraps a driver connection and times the statements that pass
// through it. Every optional database/sql connection interface is forwarded to
// the underlying connection so wrapping cannot change how the pool resets,
// validates or parameterises that connection; when the underlying driver does
// not implement one, the wrapper reproduces database/sql's own fallback.
type slowQueryConn struct {
	driver.Conn
	collector *slowQueryCollector
}

// slowQueryConnTx adds BeginTx for drivers that can begin a transaction with a
// context. It is a separate type rather than a method on slowQueryConn because
// a driver without ConnBeginTx must not be handed a wrapper that claims to
// support it: database/sql's own fallback for that case also checks the
// context and reports the non-default-isolation and read-only errors, which is
// exactly what the wrapped driver would have got.
type slowQueryConnTx struct {
	*slowQueryConn
}

func (c *slowQueryConnTx) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

var (
	_ driver.Conn               = (*slowQueryConn)(nil)
	_ driver.ConnPrepareContext = (*slowQueryConn)(nil)
	_ driver.ExecerContext      = (*slowQueryConn)(nil)
	_ driver.QueryerContext     = (*slowQueryConn)(nil)
	_ driver.NamedValueChecker  = (*slowQueryConn)(nil)
	_ driver.SessionResetter    = (*slowQueryConn)(nil)
	_ driver.Validator          = (*slowQueryConn)(nil)
	_ driver.Pinger             = (*slowQueryConn)(nil)
	_ driver.ConnBeginTx        = (*slowQueryConnTx)(nil)
	_ driver.Conn               = (*slowQueryConnTx)(nil)
)

func (c *slowQueryConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if preparer, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return preparer.PrepareContext(ctx, query)
	}
	return c.Prepare(query)
}

func (c *slowQueryConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	start := time.Now()
	result, err := execer.ExecContext(ctx, query, args)
	c.observe("exec", query, start, err)
	return result, err
}

func (c *slowQueryConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	start := time.Now()
	rows, err := queryer.QueryContext(ctx, query, args)
	c.observe("query", query, start, err)
	return rows, err
}

func (c *slowQueryConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

func (c *slowQueryConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c *slowQueryConn) IsValid() bool {
	if validator, ok := c.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

func (c *slowQueryConn) Ping(ctx context.Context) error {
	if pinger, ok := c.Conn.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

// observe records a completed statement. ErrSkip means the driver declined the
// call and database/sql will fall back to a prepared statement, which is
// recorded when that statement actually runs.
func (c *slowQueryConn) observe(operation string, statement string, start time.Time, err error) {
	if err == driver.ErrSkip || !c.collector.enabled() {
		return
	}
	c.collector.record(operation, statement, time.Since(start))
}

// ---------------------------------------------------------------------------
// connection-pool and driver facts
// ---------------------------------------------------------------------------

// PoolStats reports the connection-pool counters of the primary pool.
func (s *Store) PoolStats() sql.DBStats {
	if s == nil || s.db == nil || s.db.DB == nil {
		return sql.DBStats{}
	}
	return s.db.Stats()
}

// DriverName is the normalized database driver the store was opened with.
func (s *Store) DriverName() string {
	if s == nil {
		return ""
	}
	return s.driver
}

// IsPostgresDriver reports whether the store runs on Postgres. The database
// panel only exists for Postgres.
func (s *Store) IsPostgresDriver() bool {
	return s != nil && s.driver == "postgres"
}

// ---------------------------------------------------------------------------
// Postgres statistics snapshot
// ---------------------------------------------------------------------------

const (
	// systemDatabaseStatementTimeout is applied with SET LOCAL so a statistics
	// view that has grown pathological cannot become the thing that makes the
	// Monitor slow.
	systemDatabaseStatementTimeout = 2000 * time.Millisecond
	// systemDatabaseQueryTimeout is the outer context bound; the transaction
	// plus every statement share it.
	systemDatabaseQueryTimeout = 5 * time.Second
	// systemDatabaseListLimit caps every list this snapshot returns.
	systemDatabaseListLimit = 20
)

// SystemDatabaseSnapshot is the payload behind GET /api/system/db.
type SystemDatabaseSnapshot struct {
	Driver      string    `json:"driver"`
	Supported   bool      `json:"supported"`
	Unsupported bool      `json:"unsupported"`
	Reason      string    `json:"reason,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
	Warnings    []string  `json:"warnings"`

	Server       *SystemDatabaseServer    `json:"server,omitempty"`
	Database     *SystemDatabaseCounters  `json:"database,omitempty"`
	Activity     *SystemDatabaseActivity  `json:"activity,omitempty"`
	Relations    []SystemDatabaseRelation `json:"relations"`
	Indexes      *SystemDatabaseIndexes   `json:"indexes,omitempty"`
	Tables       []SystemDatabaseTable    `json:"tables"`
	Checkpointer *SystemCheckpointerState `json:"checkpointer,omitempty"`
	Settings     []SystemDatabaseSetting  `json:"settings"`
}

type SystemDatabaseServer struct {
	Version    string `json:"version"`
	VersionNum int    `json:"version_num"`
}

type SystemDatabaseCounters struct {
	Name          string  `json:"name"`
	SizeBytes     int64   `json:"size_bytes"`
	BlocksHit     int64   `json:"blks_hit"`
	BlocksRead    int64   `json:"blks_read"`
	CacheHitRatio float64 `json:"cache_hit_ratio"`
	TempFiles     int64   `json:"temp_files"`
	TempBytes     int64   `json:"temp_bytes"`
	Deadlocks     int64   `json:"deadlocks"`
	XactCommit    int64   `json:"xact_commit"`
	XactRollback  int64   `json:"xact_rollback"`
}

type SystemDatabaseActivity struct {
	Sessions        int64                 `json:"sessions"`
	TotalSessions   int64                 `json:"total_sessions"`
	ByState         []SystemDatabaseCount `json:"by_state"`
	ByWaitEventType []SystemDatabaseCount `json:"by_wait_event_type"`
	LongestQuery    *SystemDatabaseQuery  `json:"longest_query,omitempty"`
}

type SystemDatabaseCount struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

type SystemDatabaseQuery struct {
	PID           int       `json:"pid"`
	State         string    `json:"state,omitempty"`
	WaitEventType string    `json:"wait_event_type,omitempty"`
	WaitEvent     string    `json:"wait_event,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	DurationMs    float64   `json:"duration_ms"`
	Statement     string    `json:"statement"`
}

type SystemDatabaseRelation struct {
	Name       string `json:"name"`
	TotalBytes int64  `json:"total_bytes"`
	HeapBytes  int64  `json:"heap_bytes"`
	EstRows    int64  `json:"est_rows"`
}

type SystemDatabaseIndexes struct {
	Unused    []SystemDatabaseIndex `json:"unused"`
	WorstRead []SystemDatabaseIndex `json:"worst_tup_read_per_scan"`
}

type SystemDatabaseIndex struct {
	Table          string  `json:"table"`
	Name           string  `json:"name"`
	ScanCount      int64   `json:"idx_scan"`
	TuplesRead     int64   `json:"idx_tup_read"`
	TuplesFetched  int64   `json:"idx_tup_fetch"`
	SizeBytes      int64   `json:"size_bytes"`
	TupReadPerScan float64 `json:"tup_read_per_scan"`
	FetchRatio     float64 `json:"fetch_ratio"`
}

type SystemDatabaseTable struct {
	Name       string `json:"name"`
	LiveTuples int64  `json:"n_live_tup"`
	Inserts    int64  `json:"n_tup_ins"`
	Updates    int64  `json:"n_tup_upd"`
	Deletes    int64  `json:"n_tup_del"`
	SeqScan    int64  `json:"seq_scan"`
	SeqTupRead int64  `json:"seq_tup_read"`
	SizeBytes  int64  `json:"size_bytes"`
}

// SystemCheckpointerState reports the checkpointer counters. Postgres 17 moved
// them from pg_stat_bgwriter into pg_stat_checkpointer and dropped
// checkpoints_timed/checkpoints_req in favour of num_timed/num_requested.
//
// The two time columns are double precision in both the old and the new view
// (they accumulate milliseconds), so both queries round and cast them to bigint
// in SQL. Scanning them straight into int64 fails at runtime with "converting
// driver.Value type float64 to a int64", which is what a live Postgres 17
// reported before this cast was added - the section came back absent and only a
// warning was left behind.
type SystemCheckpointerState struct {
	Source         string `json:"source"`
	Timed          int64  `json:"num_timed"`
	Requested      int64  `json:"num_requested"`
	WriteTimeMs    int64  `json:"write_time_ms"`
	SyncTimeMs     int64  `json:"sync_time_ms"`
	BuffersWritten int64  `json:"buffers_written"`
}

type SystemDatabaseSetting struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Unit  string `json:"unit,omitempty"`
	Raw   string `json:"raw,omitempty"`
}

// systemSettingNames are the settings this workload is sensitive to: the page
// exists so an operator can see, from the running server, that shared_buffers
// is 128 MB and random_page_cost still assumes an SSD.
var systemSettingNames = []string{
	"shared_buffers",
	"work_mem",
	"maintenance_work_mem",
	"effective_cache_size",
	"random_page_cost",
	"max_wal_size",
	"max_connections",
	"statement_timeout",
}

// UnsupportedSystemDatabaseSnapshot is the structured "not Postgres" payload.
// It is a 200-shaped answer, not an error, so the System page can degrade to a
// note instead of a failure.
func UnsupportedSystemDatabaseSnapshot(driver string, reason string) SystemDatabaseSnapshot {
	return SystemDatabaseSnapshot{
		Driver:      driver,
		Supported:   false,
		Unsupported: true,
		Reason:      reason,
		GeneratedAt: time.Now().UTC(),
		Warnings:    []string{},
		Relations:   []SystemDatabaseRelation{},
		Tables:      []SystemDatabaseTable{},
		Settings:    []SystemDatabaseSetting{},
	}
}

// statisticsPool prefers the store's read-only pool when configuration opened
// one, so the System page never borrows the connections the proxy records
// through. Statement routing does not apply inside a transaction, which is why
// this picks the pool explicitly.
func (s *Store) statisticsPool() *sql.DB {
	if s == nil || s.db == nil {
		return nil
	}
	if s.db.readDB != nil {
		return s.db.readDB
	}
	return s.db.DB
}

// SystemDatabaseSnapshot reads the Postgres statistics views. Every statement
// is a read; the whole read runs in one read-only transaction with a short
// statement timeout, and every list is capped.
func (s *Store) SystemDatabaseSnapshot(ctx context.Context) (SystemDatabaseSnapshot, error) {
	snapshot := SystemDatabaseSnapshot{
		Driver:      s.DriverName(),
		GeneratedAt: time.Now().UTC(),
		Warnings:    []string{},
		Relations:   []SystemDatabaseRelation{},
		Tables:      []SystemDatabaseTable{},
		Settings:    []SystemDatabaseSetting{},
	}
	if !s.IsPostgresDriver() {
		return UnsupportedSystemDatabaseSnapshot(snapshot.Driver, "the database panel reads the PostgreSQL pg_stat_* views; this deployment uses the "+snapshot.Driver+" driver"), nil
	}
	snapshot.Supported = true

	pool := s.statisticsPool()
	if pool == nil {
		return snapshot, errors.New("store: database is not configured")
	}

	ctx, cancel := context.WithTimeout(ctx, systemDatabaseQueryTimeout)
	defer cancel()

	tx, err := pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snapshot, fmt.Errorf("open read-only statistics transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// SET LOCAL keeps the short timeout on this transaction's connection and
	// the ROLLBACK drops it again; the context deadline is the outer bound.
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = `+strconv.FormatInt(systemDatabaseStatementTimeout.Milliseconds(), 10)); err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "could not set a local statement timeout: "+err.Error())
	}

	versionNum, err := s.collectServerVersion(ctx, tx, &snapshot)
	if err != nil {
		return snapshot, err
	}
	if err := s.collectDatabaseCounters(ctx, tx, &snapshot); err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "pg_stat_database: "+err.Error())
	}
	if err := s.collectActivity(ctx, tx, &snapshot); err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "pg_stat_activity: "+err.Error())
	}
	if err := s.collectRelations(ctx, tx, &snapshot); err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "pg_class: "+err.Error())
	}
	if err := s.collectIndexes(ctx, tx, &snapshot); err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "pg_stat_user_indexes: "+err.Error())
	}
	if err := s.collectTables(ctx, tx, &snapshot); err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "pg_stat_user_tables: "+err.Error())
	}
	if err := s.collectCheckpointer(ctx, tx, &snapshot, versionNum); err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "checkpointer statistics: "+err.Error())
	}
	if err := s.collectSettings(ctx, tx, &snapshot); err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "pg_settings: "+err.Error())
	}
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		snapshot.Warnings = append(snapshot.Warnings, "rollback: "+err.Error())
	}
	return snapshot, nil
}

func (s *Store) collectServerVersion(ctx context.Context, tx *sql.Tx, snapshot *SystemDatabaseSnapshot) (int, error) {
	var version string
	var versionNum int
	if err := tx.QueryRowContext(ctx, `SELECT current_setting('server_version'), current_setting('server_version_num')::int`).Scan(&version, &versionNum); err != nil {
		return 0, fmt.Errorf("read server version: %w", err)
	}
	snapshot.Server = &SystemDatabaseServer{Version: version, VersionNum: versionNum}
	return versionNum, nil
}

func (s *Store) collectDatabaseCounters(ctx context.Context, tx *sql.Tx, snapshot *SystemDatabaseSnapshot) error {
	const query = `
SELECT current_database(),
       pg_database_size(current_database()),
       COALESCE(s.blks_hit, 0),
       COALESCE(s.blks_read, 0),
       COALESCE(s.temp_files, 0),
       COALESCE(s.temp_bytes, 0),
       COALESCE(s.deadlocks, 0),
       COALESCE(s.xact_commit, 0),
       COALESCE(s.xact_rollback, 0)
FROM pg_stat_database s
WHERE s.datname = current_database()`
	var counters SystemDatabaseCounters
	err := tx.QueryRowContext(ctx, query).Scan(
		&counters.Name,
		&counters.SizeBytes,
		&counters.BlocksHit,
		&counters.BlocksRead,
		&counters.TempFiles,
		&counters.TempBytes,
		&counters.Deadlocks,
		&counters.XactCommit,
		&counters.XactRollback,
	)
	if errors.Is(err, sql.ErrNoRows) {
		counters.CacheHitRatio = 0
		snapshot.Database = &counters
		return nil
	}
	if err != nil {
		return err
	}
	if total := counters.BlocksHit + counters.BlocksRead; total > 0 {
		counters.CacheHitRatio = float64(counters.BlocksHit) / float64(total)
	}
	snapshot.Database = &counters
	return nil
}

func (s *Store) collectActivity(ctx context.Context, tx *sql.Tx, snapshot *SystemDatabaseSnapshot) error {
	activity := &SystemDatabaseActivity{
		ByState:         []SystemDatabaseCount{},
		ByWaitEventType: []SystemDatabaseCount{},
	}
	const countsQuery = `
SELECT count(*) FILTER (WHERE datname = current_database()), count(*)
FROM pg_stat_activity`
	if err := tx.QueryRowContext(ctx, countsQuery).Scan(&activity.Sessions, &activity.TotalSessions); err != nil {
		return err
	}
	const byStateQuery = `
SELECT COALESCE(state, 'unknown') AS label, count(*)
FROM pg_stat_activity
WHERE datname = current_database()
GROUP BY 1
ORDER BY 2 DESC, 1 ASC`
	byState, err := querySystemCounts(ctx, tx, byStateQuery)
	if err != nil {
		return err
	}
	activity.ByState = byState
	const byWaitQuery = `
SELECT COALESCE(wait_event_type, 'none') AS label, count(*)
FROM pg_stat_activity
WHERE datname = current_database()
GROUP BY 1
ORDER BY 2 DESC, 1 ASC`
	byWait, err := querySystemCounts(ctx, tx, byWaitQuery)
	if err != nil {
		return err
	}
	activity.ByWaitEventType = byWait

	longestQuery := `
SELECT pid,
       COALESCE(state, ''),
       COALESCE(wait_event_type, ''),
       COALESCE(wait_event, ''),
       query_start,
       EXTRACT(EPOCH FROM (now() - query_start)) * 1000,
       left(query, ` + strconv.Itoa(systemQueryTextLimit) + `)
FROM pg_stat_activity
WHERE datname = current_database()
  AND state <> 'idle'
  AND query_start IS NOT NULL
  AND pid <> pg_backend_pid()
ORDER BY query_start ASC
LIMIT 1`
	var (
		longest    SystemDatabaseQuery
		startedAt  sql.NullTime
		durationMs sql.NullFloat64
		statement  sql.NullString
	)
	err = tx.QueryRowContext(ctx, longestQuery).Scan(
		&longest.PID,
		&longest.State,
		&longest.WaitEventType,
		&longest.WaitEvent,
		&startedAt,
		&durationMs,
		&statement,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		if startedAt.Valid {
			longest.StartedAt = startedAt.Time.UTC()
		}
		if durationMs.Valid {
			longest.DurationMs = durationMs.Float64
		}
		longest.Statement = sanitizeSlowQuery(statement.String)
		activity.LongestQuery = &longest
	}
	snapshot.Activity = activity
	return nil
}

func querySystemCounts(ctx context.Context, tx *sql.Tx, query string) ([]SystemDatabaseCount, error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SystemDatabaseCount{}
	for rows.Next() {
		var item SystemDatabaseCount
		if err := rows.Scan(&item.Label, &item.Count); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) collectRelations(ctx context.Context, tx *sql.Tx, snapshot *SystemDatabaseSnapshot) error {
	query := `
SELECT c.relname,
       pg_total_relation_size(c.oid),
       pg_relation_size(c.oid),
       GREATEST(c.reltuples, 0)::bigint
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relkind IN ('r', 'p', 'm')
ORDER BY pg_total_relation_size(c.oid) DESC
LIMIT ` + strconv.Itoa(systemDatabaseListLimit)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []SystemDatabaseRelation{}
	for rows.Next() {
		var item SystemDatabaseRelation
		if err := rows.Scan(&item.Name, &item.TotalBytes, &item.HeapBytes, &item.EstRows); err != nil {
			return err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	snapshot.Relations = out
	return nil
}

func (s *Store) collectIndexes(ctx context.Context, tx *sql.Tx, snapshot *SystemDatabaseSnapshot) error {
	indexes := &SystemDatabaseIndexes{
		Unused:    []SystemDatabaseIndex{},
		WorstRead: []SystemDatabaseIndex{},
	}
	// The seventh column is the read-per-scan ratio the other query computes;
	// an index with idx_scan = 0 has no ratio, so it is NULL. Both queries must
	// return the same shape because scanSystemIndexes reads them alike.
	unusedQuery := `
SELECT s.relname, s.indexrelname, s.idx_scan, s.idx_tup_read, s.idx_tup_fetch,
       pg_relation_size(s.indexrelid),
       NULL::float8
FROM pg_stat_user_indexes s
WHERE s.schemaname = 'public'
  AND s.idx_scan = 0
ORDER BY pg_relation_size(s.indexrelid) DESC, s.relname ASC, s.indexrelname ASC
LIMIT ` + strconv.Itoa(systemDatabaseListLimit)
	if err := scanSystemIndexes(ctx, tx, unusedQuery, &indexes.Unused); err != nil {
		return err
	}
	// "Worst" is tuples read per scan: an index whose scans each read thousands
	// of tuples is either bloated or not selective enough for the query shape
	// that uses it. idx_scan = 0 has no ratio and is reported separately.
	worstQuery := `
SELECT s.relname, s.indexrelname, s.idx_scan, s.idx_tup_read, s.idx_tup_fetch,
       pg_relation_size(s.indexrelid),
       (s.idx_tup_read::float8 / s.idx_scan)
FROM pg_stat_user_indexes s
WHERE s.schemaname = 'public'
  AND s.idx_scan > 0
ORDER BY 7 DESC, s.idx_tup_read DESC, s.relname ASC
LIMIT ` + strconv.Itoa(systemDatabaseListLimit)
	if err := scanSystemIndexes(ctx, tx, worstQuery, &indexes.WorstRead); err != nil {
		return err
	}
	snapshot.Indexes = indexes
	return nil
}

func scanSystemIndexes(ctx context.Context, tx *sql.Tx, query string, into *[]SystemDatabaseIndex) error {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []SystemDatabaseIndex{}
	for rows.Next() {
		var (
			item        SystemDatabaseIndex
			readPerScan sql.NullFloat64
		)
		if err := rows.Scan(&item.Table, &item.Name, &item.ScanCount, &item.TuplesRead, &item.TuplesFetched, &item.SizeBytes, &readPerScan); err != nil {
			return err
		}
		if readPerScan.Valid {
			item.TupReadPerScan = readPerScan.Float64
		} else if item.ScanCount > 0 {
			item.TupReadPerScan = float64(item.TuplesRead) / float64(item.ScanCount)
		}
		if item.TuplesRead > 0 {
			item.FetchRatio = float64(item.TuplesFetched) / float64(item.TuplesRead)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	*into = out
	return nil
}

func (s *Store) collectTables(ctx context.Context, tx *sql.Tx, snapshot *SystemDatabaseSnapshot) error {
	query := `
SELECT s.relname, s.n_live_tup, s.n_tup_ins, s.n_tup_upd, s.n_tup_del, s.seq_scan, s.seq_tup_read,
       pg_relation_size(s.relid)
FROM pg_stat_user_tables s
WHERE s.schemaname = 'public'
ORDER BY (s.n_tup_ins + s.n_tup_upd + s.n_tup_del) DESC, s.n_live_tup DESC, s.relname ASC
LIMIT ` + strconv.Itoa(systemDatabaseListLimit)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []SystemDatabaseTable{}
	for rows.Next() {
		var item SystemDatabaseTable
		if err := rows.Scan(&item.Name, &item.LiveTuples, &item.Inserts, &item.Updates, &item.Deletes, &item.SeqScan, &item.SeqTupRead, &item.SizeBytes); err != nil {
			return err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	snapshot.Tables = out
	return nil
}

func (s *Store) collectCheckpointer(ctx context.Context, tx *sql.Tx, snapshot *SystemDatabaseSnapshot, versionNum int) error {
	if versionNum >= 170000 {
		const query = `
SELECT num_timed, num_requested,
       round(write_time)::bigint, round(sync_time)::bigint,
       buffers_written
FROM pg_stat_checkpointer`
		var state SystemCheckpointerState
		state.Source = "pg_stat_checkpointer"
		if err := tx.QueryRowContext(ctx, query).Scan(&state.Timed, &state.Requested, &state.WriteTimeMs, &state.SyncTimeMs, &state.BuffersWritten); err != nil {
			return err
		}
		snapshot.Checkpointer = &state
		return nil
	}
	// Postgres 16 and older: the counters live in pg_stat_bgwriter and use the
	// checkpoints_* names.
	const query = `
SELECT checkpoints_timed, checkpoints_req,
       round(checkpoint_write_time)::bigint, round(checkpoint_sync_time)::bigint,
       buffers_checkpoint
FROM pg_stat_bgwriter`
	var state SystemCheckpointerState
	state.Source = "pg_stat_bgwriter"
	if err := tx.QueryRowContext(ctx, query).Scan(&state.Timed, &state.Requested, &state.WriteTimeMs, &state.SyncTimeMs, &state.BuffersWritten); err != nil {
		return err
	}
	snapshot.Checkpointer = &state
	return nil
}

func (s *Store) collectSettings(ctx context.Context, tx *sql.Tx, snapshot *SystemDatabaseSnapshot) error {
	names := make([]string, 0, len(systemSettingNames))
	for _, name := range systemSettingNames {
		names = append(names, "'"+name+"'")
	}
	query := `
SELECT name, setting, COALESCE(unit, ''), current_setting(name)
FROM pg_settings
WHERE name IN (` + strings.Join(names, ", ") + `)
ORDER BY name`
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []SystemDatabaseSetting{}
	for rows.Next() {
		var item SystemDatabaseSetting
		if err := rows.Scan(&item.Name, &item.Raw, &item.Unit, &item.Value); err != nil {
			return err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	snapshot.Settings = out
	return nil
}
