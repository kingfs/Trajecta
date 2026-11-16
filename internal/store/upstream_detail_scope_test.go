package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestGetUpstreamDetailScopedMatchesListAnalytics is the parity oracle for the
// id-scoped detail read: the Analytics record GetUpstreamDetail returns must be
// exactly the record ListUpstreamAnalytics produces for the same upstream under
// the same window and model filter, because the detail payload shape is what
// the Monitor's detail endpoint asserts.
func TestGetUpstreamDetailScopedMatchesListAnalytics(t *testing.T) {
	dir := t.TempDir()
	st, err := newSQLiteTestStore(dir)
	if err != nil {
		t.Fatalf("newSQLiteTestStore() error = %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	// up-a carries several models, failures and a trailing-60s row so the
	// coverage cut, the error/failure lists and the Current* rates all have
	// content; up-b is success-only; up-c has a single row.
	writeModelLog(t, st, dir, "sa-a1.http", "gpt-5", "/v1/responses", "POST", "up-a", 200, 10, now.Add(-50*time.Minute))
	writeModelLog(t, st, dir, "sa-a2.http", "gpt-5", "/v1/responses", "POST", "up-a", 500, 0, now.Add(-40*time.Minute))
	writeModelLog(t, st, dir, "sa-a3.http", "gpt-4.1", "/v1/chat/completions", "POST", "up-a", 429, 5, now.Add(-30*time.Minute))
	writeModelLog(t, st, dir, "sa-a4.http", "claude-3", "/v1/messages", "POST", "up-a", 200, 7, now.Add(-20*time.Minute))
	writeModelLog(t, st, dir, "sa-a5.http", "gemini-pro", "/v1/responses", "POST", "up-a", 200, 9, now.Add(-10*time.Second))
	writeModelLog(t, st, dir, "sa-b1.http", "gpt-5", "/v1/responses", "POST", "up-b", 200, 3, now.Add(-10*time.Minute))
	writeModelLog(t, st, dir, "sa-c1.http", "gpt-5", "/v1/responses", "POST", "up-c", 200, 4, now.Add(-5*time.Minute))
	writeModelLog(t, st, dir, "sa-old.http", "gpt-5", "/v1/responses", "POST", "up-a", 200, 1, now.Add(-48*time.Hour))

	for _, since := range []time.Time{{}, now.Add(-24 * time.Hour)} {
		for _, modelFilter := range []string{"", "gpt-5"} {
			list, err := st.ListUpstreamAnalytics(8, 5, since, modelFilter)
			if err != nil {
				t.Fatalf("ListUpstreamAnalytics() error = %v", err)
			}
			for _, upstreamID := range []string{"up-a", "up-b", "up-c"} {
				var want UpstreamAnalyticsRecord
				found := false
				for _, item := range list {
					if item.UpstreamID == upstreamID {
						want = item
						found = true
						break
					}
				}
				detail, err := st.GetUpstreamDetail(upstreamID, since, modelFilter, 10, time.Hour, 4, nil)
				if !found {
					if !errors.Is(err, sql.ErrNoRows) {
						t.Fatalf("GetUpstreamDetail(%s, since=%v, model=%q) error = %v, want sql.ErrNoRows", upstreamID, since, modelFilter, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("GetUpstreamDetail(%s) error = %v", upstreamID, err)
				}
				got := detail.Analytics
				// RatesSampledAt is a wall-clock sampling instant: the rates
				// must match exactly, the instant itself is compared for
				// zero/non-zero only.
				if got.RatesSampledAt.IsZero() != want.RatesSampledAt.IsZero() {
					t.Fatalf("RatesSampledAt zero mismatch for %s: detail=%v list=%v", upstreamID, got.RatesSampledAt, want.RatesSampledAt)
				}
				got.RatesSampledAt, want.RatesSampledAt = time.Time{}, time.Time{}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("scoped analytics for %s (since=%v, model=%q) = %#v, want %#v", upstreamID, since, modelFilter, got, want)
				}
			}
		}
	}

	// The trailing-60s rates must actually be populated for up-a, otherwise the
	// parity above would only compare zero rates.
	detail, err := st.GetUpstreamDetail("up-a", time.Time{}, "", 10, time.Hour, 4, nil)
	if err != nil {
		t.Fatalf("GetUpstreamDetail(up-a) error = %v", err)
	}
	if detail.Analytics.CurrentRPM == 0 || detail.Analytics.CurrentTPM == 0 || detail.Analytics.RatesSampledAt.IsZero() {
		t.Fatalf("expected non-zero trailing-60s rates, got %+v", detail.Analytics)
	}

	for _, missing := range []string{"up-missing", ""} {
		if _, err := st.GetUpstreamDetail(missing, time.Time{}, "", 10, time.Hour, 4, nil); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("GetUpstreamDetail(%q) error = %v, want sql.ErrNoRows", missing, err)
		}
	}
}

// TestGetUpstreamDetailQueriesAreIDScoped counts the SQL statements the detail
// read issues and asserts every one of them is restricted to the target
// upstream id. Before the scoped read, GetUpstreamDetail ran the page-wide
// ListUpstreamAnalytics (six statements ranking every upstream) and then picked
// the target out of the result; the statement count is unchanged but every
// analytics statement now carries `selected_upstream_id = ?` instead of
// `selected_upstream_id <> ”`.
func TestGetUpstreamDetailQueriesAreIDScoped(t *testing.T) {
	dir := t.TempDir()
	st, err := newSQLiteTestStore(dir)
	if err != nil {
		t.Fatalf("newSQLiteTestStore() error = %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	for u, upstreamID := range []string{"up-a", "up-b", "up-c"} {
		for i := 0; i < 4; i++ {
			status := 200
			if i == 1 {
				status = 503
			}
			writeModelLog(t, st, dir, upstreamID+string(rune('a'+i))+".http", "gpt-5", "/v1/responses", "POST", upstreamID, status, 10, now.Add(-time.Duration(10*u+i)*time.Minute))
		}
	}

	rec := &detailQueryRecorder{}
	pool, err := openCountingSQLitePool(st.dbPath, rec)
	if err != nil {
		t.Fatalf("openCountingSQLitePool() error = %v", err)
	}
	defer pool.Close()
	saved := st.db
	st.db = &rebindingDB{DB: pool, driver: "sqlite"}
	defer func() { st.db = saved }()

	if _, err := st.GetUpstreamDetail("up-a", now.Add(-24*time.Hour), "", 10, time.Hour, 4, nil); err != nil {
		t.Fatalf("GetUpstreamDetail() error = %v", err)
	}
	statements := rec.snapshot()
	// aggregate(1) + coverage(2) + errors(1) + failures(1) + rates(1) +
	// traces(1) + failure reasons(1) + latest failure instant(1) + timeline(1).
	if len(statements) != 10 {
		t.Fatalf("GetUpstreamDetail issued %d statements, want 10:\n%s", len(statements), strings.Join(statements, "\n"))
	}
	for _, statement := range statements {
		if !strings.Contains(statement, "selected_upstream_id = ?") {
			t.Fatalf("detail statement is not id-scoped:\n%s", statement)
		}
		if strings.Contains(statement, "selected_upstream_id <> ''") {
			t.Fatalf("detail statement still scans every upstream:\n%s", statement)
		}
	}

	// The batched list itself keeps its page-wide shape for the Overview/list
	// pages: six statements, all ranking every upstream.
	rec.reset()
	if _, err := st.ListUpstreamAnalytics(8, 5, now.Add(-24*time.Hour), ""); err != nil {
		t.Fatalf("ListUpstreamAnalytics() error = %v", err)
	}
	listStatements := rec.snapshot()
	if len(listStatements) != 6 {
		t.Fatalf("ListUpstreamAnalytics issued %d statements, want 6:\n%s", len(listStatements), strings.Join(listStatements, "\n"))
	}
	for _, statement := range listStatements {
		if !strings.Contains(statement, "selected_upstream_id <> ''") {
			t.Fatalf("list statement lost its page-wide scope:\n%s", statement)
		}
	}
}

// detailQueryRecorder collects the SQL text of every statement executed
// through a counting pool, so a test can assert both the number and the scope
// of the statements one store call issues.
type detailQueryRecorder struct {
	mu         sync.Mutex
	statements []string
}

func (r *detailQueryRecorder) record(query string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, query)
}

func (r *detailQueryRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.statements...)
}

func (r *detailQueryRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = nil
}

// openCountingSQLitePool opens a second pool over the same SQLite file whose
// connections record every executed statement. It wraps both execution paths
// database/sql can take: a direct Queryer/QueryerContext call and the
// prepared-statement fallback (modernc.org/sqlite implements the non-context
// Queryer, and its statements are counted through the wrapped driver.Stmt).
func openCountingSQLitePool(dbPath string, rec *detailQueryRecorder) (*sql.DB, error) {
	dsn := sqliteDSN(dbPath)
	base, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	connector, err := observedConnector(base.Driver(), dsn)
	if err != nil {
		_ = base.Close()
		return nil, err
	}
	_ = base.Close()
	pool := sql.OpenDB(&countingConnector{base: connector, rec: rec})
	pool.SetMaxOpenConns(4)
	return pool, nil
}

type countingConnector struct {
	base driver.Connector
	rec  *detailQueryRecorder
}

func (c *countingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: conn, rec: c.rec}, nil
}

func (c *countingConnector) Driver() driver.Driver { return c.base.Driver() }

type countingConn struct {
	driver.Conn
	rec *detailQueryRecorder
}

func (c *countingConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &countingStmt{Stmt: stmt, query: query, rec: c.rec}, nil
}

func (c *countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var (
		stmt driver.Stmt
		err  error
	)
	if preparer, ok := c.Conn.(driver.ConnPrepareContext); ok {
		stmt, err = preparer.PrepareContext(ctx, query)
	} else {
		stmt, err = c.Conn.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return &countingStmt{Stmt: stmt, query: query, rec: c.rec}, nil
}

func (c *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if queryer, ok := c.Conn.(driver.QueryerContext); ok {
		c.rec.record(query)
		return queryer.QueryContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

type countingStmt struct {
	driver.Stmt
	query string
	rec   *detailQueryRecorder
}

func (s *countingStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if queryer, ok := s.Stmt.(driver.StmtQueryContext); ok {
		s.rec.record(s.query)
		return queryer.QueryContext(ctx, args)
	}
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	s.rec.record(s.query)
	// Legacy fallback for a driver statement without StmtQueryContext; the
	// modernc sqlite driver used by this test implements the context form,
	// so this branch only keeps the wrapper correct for other drivers.
	return s.Stmt.Query(values) //nolint:staticcheck // intentional legacy fallback
}
