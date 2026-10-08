package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/auth"
	"github.com/kingfs/Trajecta/internal/store"
)

func systemTestToken(t *testing.T, manager *auth.JWTManager, userID int, username string, role string) string {
	t.Helper()
	token, err := manager.IssueToken(auth.Principal{
		UserID:   userID,
		Username: username,
		Role:     role,
		Scope:    auth.DefaultTokenScope,
	})
	if err != nil {
		t.Fatalf("IssueToken(%s) error = %v", username, err)
	}
	return token.Token
}

func systemTestRequest(mux *http.ServeMux, path string, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestSystemAPIRoutesAreAdminOnly(t *testing.T) {
	t.Parallel()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	manager := testJWTManager(t, time.Hour)
	adminToken := systemTestToken(t, manager, 1, "admin", "admin")
	viewerToken := systemTestToken(t, manager, 2, "viewer", "viewer")

	mux := http.NewServeMux()
	RegisterRoutes(mux, st, RouteOptions{MonitorAuthVerifier: manager, MonitorJWT: manager})

	for _, path := range []string{"/api/system/runtime", "/api/system/db", "/api/system/slow-queries"} {
		t.Run(strings.TrimPrefix(path, "/api/system/"), func(t *testing.T) {
			if rr := systemTestRequest(mux, path, ""); rr.Code != http.StatusUnauthorized {
				t.Fatalf("GET %s without credentials = %d, want 401; body=%s", path, rr.Code, rr.Body.String())
			}
			if rr := systemTestRequest(mux, path, viewerToken); rr.Code != http.StatusForbidden {
				t.Fatalf("GET %s as a non-admin = %d, want 403; body=%s", path, rr.Code, rr.Body.String())
			}
			if rr := systemTestRequest(mux, path, adminToken); rr.Code != http.StatusOK {
				t.Fatalf("GET %s as admin = %d, want 200; body=%s", path, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestSystemRuntimeAPIReportsProcessFacts(t *testing.T) {
	t.Parallel()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	manager := testJWTManager(t, time.Hour)
	token := systemTestToken(t, manager, 1, "admin", "admin")
	mux := http.NewServeMux()
	RegisterRoutes(mux, st, RouteOptions{MonitorAuthVerifier: manager, MonitorJWT: manager})

	rr := systemTestRequest(mux, "/api/system/runtime", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/system/runtime = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var payload systemRuntimeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode runtime payload: %v", err)
	}
	if payload.GoVersion != runtime.Version() {
		t.Fatalf("go_version = %q, want %q", payload.GoVersion, runtime.Version())
	}
	if payload.GOOS != runtime.GOOS || payload.GOARCH != runtime.GOARCH {
		t.Fatalf("goos/goarch = %s/%s, want %s/%s", payload.GOOS, payload.GOARCH, runtime.GOOS, runtime.GOARCH)
	}
	if payload.NumCPU <= 0 || payload.GOMAXPROCS <= 0 {
		t.Fatalf("num_cpu/gomaxprocs = %d/%d, want positive values", payload.NumCPU, payload.GOMAXPROCS)
	}
	if payload.Goroutines <= 0 {
		t.Fatalf("goroutines = %d, want a positive count", payload.Goroutines)
	}
	if payload.UptimeSeconds < 0 {
		t.Fatalf("uptime_seconds = %v, want a non-negative value", payload.UptimeSeconds)
	}
	if payload.Heap.TotalSys == 0 {
		t.Fatalf("heap.total_sys_bytes = 0, want the mapped runtime memory")
	}
	if payload.Heap.Objects == 0 {
		t.Fatalf("heap.objects = 0, want the live heap object count")
	}
	if payload.GC.Cycles == 0 {
		// The test process has already collected garbage by the time it serves
		// a request; zero would mean the metric name stopped resolving.
		t.Log("gc cycles reported as 0; the runtime metric may not have been readable")
	}
	if payload.DBPool.Driver != "sqlite" {
		t.Fatalf("db_pool.driver = %q, want sqlite", payload.DBPool.Driver)
	}
	if payload.DBPool.MaxOpen != 4 {
		t.Fatalf("db_pool.max_open = %d, want the 4 the store was opened with", payload.DBPool.MaxOpen)
	}
	if payload.DBPool.InUse < 0 || payload.DBPool.Open < 0 || payload.DBPool.Idle < 0 {
		t.Fatalf("db_pool counters are negative: %+v", payload.DBPool)
	}
}

func TestSystemDatabaseAPIReportsUnsupportedOnSQLite(t *testing.T) {
	t.Parallel()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	manager := testJWTManager(t, time.Hour)
	token := systemTestToken(t, manager, 1, "admin", "admin")
	mux := http.NewServeMux()
	RegisterRoutes(mux, st, RouteOptions{MonitorAuthVerifier: manager, MonitorJWT: manager})

	rr := systemTestRequest(mux, "/api/system/db", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/system/db = %d, want 200 even when unsupported; body=%s", rr.Code, rr.Body.String())
	}
	var payload store.SystemDatabaseSnapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode database payload: %v", err)
	}
	if payload.Supported || !payload.Unsupported {
		t.Fatalf("supported/unsupported = %v/%v, want a structured unsupported answer", payload.Supported, payload.Unsupported)
	}
	if payload.Driver != "sqlite" {
		t.Fatalf("driver = %q, want sqlite", payload.Driver)
	}
	if payload.Reason == "" {
		t.Fatal("unsupported payload has no reason")
	}
	// The page renders these unconditionally, so they must not be null.
	if payload.Relations == nil || payload.Tables == nil || payload.Settings == nil || payload.Warnings == nil {
		t.Fatalf("unsupported payload has null lists: %+v", payload)
	}
}

func TestSystemSlowQueriesAPIReportsTheDisabledCollector(t *testing.T) {
	t.Parallel()

	// The suite does not arm the collector; assert the documented default and
	// restore it so this test cannot leak state into another one.
	store.SetSlowQueryThreshold(0)
	store.ResetSlowQueries()
	t.Cleanup(func() {
		store.SetSlowQueryThreshold(0)
		store.ResetSlowQueries()
	})

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	manager := testJWTManager(t, time.Hour)
	token := systemTestToken(t, manager, 1, "admin", "admin")
	mux := http.NewServeMux()
	RegisterRoutes(mux, st, RouteOptions{MonitorAuthVerifier: manager, MonitorJWT: manager})

	rr := systemTestRequest(mux, "/api/system/slow-queries", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/system/slow-queries = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var payload store.SlowQuerySnapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode slow-query payload: %v", err)
	}
	if payload.Enabled {
		t.Fatalf("collector reports enabled with the default threshold: %+v", payload)
	}
	if payload.ThresholdMs != 0 {
		t.Fatalf("threshold_ms = %v, want 0", payload.ThresholdMs)
	}
	if payload.Capacity != 50 {
		t.Fatalf("capacity = %d, want 50", payload.Capacity)
	}
	if len(payload.Items) != 0 {
		t.Fatalf("disabled collector returned %d items", len(payload.Items))
	}
	if payload.Driver != "sqlite" {
		t.Fatalf("driver = %q, want sqlite", payload.Driver)
	}
}

func TestDebugPprofIsMountedOnlyWhenEnabled(t *testing.T) {
	t.Parallel()

	disabled := http.NewServeMux()
	RegisterRoutes(disabled, nil, RouteOptions{})
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/cmdline", "/debug/pprof/symbol"} {
		rr := systemTestRequest(disabled, path, "")
		if rr.Code != http.StatusNotFound {
			t.Fatalf("GET %s with pprof disabled = %d, want 404; body=%s", path, rr.Code, rr.Body.String())
		}
	}

	enabled := http.NewServeMux()
	RegisterRoutes(enabled, nil, RouteOptions{DebugPprofEnabled: true})
	rr := systemTestRequest(enabled, "/debug/pprof/", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /debug/pprof/ with pprof enabled = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "pprof") {
		t.Fatalf("GET /debug/pprof/ body does not look like the pprof index: %s", rr.Body.String())
	}
}
