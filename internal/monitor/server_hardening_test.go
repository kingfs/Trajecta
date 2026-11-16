package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/auth"
	"github.com/kingfs/Trajecta/internal/store"
)

type fixedPrincipalVerifier struct {
	principal auth.Principal
	ok        bool
}

func (v fixedPrincipalVerifier) VerifyToken(context.Context, string) (auth.Principal, bool, error) {
	return v.principal, v.ok, nil
}

func newHardeningTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := newTestStore(t.TempDir())
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestHealthzAnswersWithoutCredentials(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	RegisterRoutes(mux, newHardeningTestStore(t), RouteOptions{
		AuthVerifier:        fixedPrincipalVerifier{ok: false},
		MonitorAuthVerifier: fixedPrincipalVerifier{ok: false},
	})

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := strings.TrimSpace(rr.Body.String()); got != `{"status":"ok"}` {
		t.Fatalf("GET /healthz body = %q", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	head := httptest.NewRecorder()
	mux.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/healthz", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD /healthz = %d body %q, want 200 and no body", head.Code, head.Body.String())
	}

	post := httptest.NewRecorder()
	mux.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz = %d, want 405", post.Code)
	}
}

func TestSensitiveMonitorRoutesRequireAdmin(t *testing.T) {
	t.Parallel()

	paths := []string{
		"/api/channels",
		"/api/settings/routing",
		"/api/secrets/local-key",
		"/api/secret-masks",
		"/api/secret-mappings",
		"/api/secret-suppressions",
		"/api/provider-probe",
		"/api/provider-setup/probe",
		"/api/provider-model-test",
		"/api/provider-model-detect",
		"/api/model-aliases",
		"/api/analysis/batch/reanalyze",
		"/api/resources/backfill",
		"/api/responses/function-executors",
	}

	for _, tc := range []struct {
		name  string
		role  string
		want  int
		exact bool
	}{
		{name: "viewer", role: auth.RoleViewer, want: http.StatusForbidden, exact: true},
		{name: "admin", role: auth.RoleAdmin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			RegisterRoutes(mux, newHardeningTestStore(t), RouteOptions{
				AuthVerifier:        fixedPrincipalVerifier{principal: auth.Principal{Username: tc.name, Role: tc.role}, ok: true},
				MonitorAuthVerifier: fixedPrincipalVerifier{principal: auth.Principal{Username: tc.name, Role: tc.role}, ok: true},
			})
			for _, path := range paths {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Authorization", "Bearer token")
				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, req)
				if tc.exact && rr.Code != tc.want {
					t.Fatalf("GET %s as %s = %d, want %d; body=%s", path, tc.name, rr.Code, tc.want, rr.Body.String())
				}
				if !tc.exact && (rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden) {
					t.Fatalf("GET %s as %s = %d, want the admin gate to pass; body=%s", path, tc.name, rr.Code, rr.Body.String())
				}
			}
		})
	}

	// A viewer keeps the read surfaces; the gate must not be a blanket deny.
	mux := http.NewServeMux()
	RegisterRoutes(mux, newHardeningTestStore(t), RouteOptions{
		AuthVerifier:        fixedPrincipalVerifier{principal: auth.Principal{Username: "viewer", Role: auth.RoleViewer}, ok: true},
		MonitorAuthVerifier: fixedPrincipalVerifier{principal: auth.Principal{Username: "viewer", Role: auth.RoleViewer}, ok: true},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/traces", nil)
	req.Header.Set("Authorization", "Bearer token")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("viewer GET /api/traces = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPprofRequiresAdminWhenEnabled(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		verify fixedPrincipalVerifier
		want   int
	}{
		{name: "unauthenticated", verify: fixedPrincipalVerifier{ok: false}, want: http.StatusUnauthorized},
		{name: "viewer", verify: fixedPrincipalVerifier{principal: auth.Principal{Username: "viewer", Role: auth.RoleViewer}, ok: true}, want: http.StatusForbidden},
		{name: "admin", verify: fixedPrincipalVerifier{principal: auth.Principal{Username: "admin", Role: auth.RoleAdmin}, ok: true}, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			RegisterRoutes(mux, newHardeningTestStore(t), RouteOptions{
				AuthVerifier:        tc.verify,
				MonitorAuthVerifier: tc.verify,
				DebugPprofEnabled:   true,
			})
			req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
			req.Header.Set("Authorization", "Bearer token")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("GET /debug/pprof/ as %s = %d, want %d", tc.name, rr.Code, tc.want)
			}
		})
	}
}

func TestLoginThrottleBlocksAndExpires(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	throttle := newLoginThrottleWithClock(func() time.Time { return now })
	key := "192.0.2.10|admin"

	for i := 0; i < loginMaxFailures; i++ {
		if wait := throttle.retryAfter(key); wait != 0 {
			t.Fatalf("attempt %d blocked early with %v", i, wait)
		}
		throttle.recordFailure(key)
	}
	if wait := throttle.retryAfter(key); wait <= 0 {
		t.Fatalf("after %d failures retryAfter = 0, want a block", loginMaxFailures)
	}

	now = now.Add(loginFailureWindow + time.Second)
	if wait := throttle.retryAfter(key); wait != 0 {
		t.Fatalf("after the window retryAfter = %v, want 0", wait)
	}

	// A success clears the counter for that (IP, username) pair.
	throttle.recordFailure(key)
	throttle.reset(key)
	if wait := throttle.retryAfter(key); wait != 0 {
		t.Fatalf("after reset retryAfter = %v, want 0", wait)
	}
}

func TestLoginRejectsRepeatedFailuresAndOversizedBodies(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "control.sqlite3")
	if err := auth.MigrateDatabaseUp("sqlite", dbPath, 0); err != nil {
		t.Fatalf("auth.MigrateDatabaseUp() error = %v", err)
	}
	authStore, err := auth.OpenDatabase("sqlite", dbPath, 4, 4)
	if err != nil {
		t.Fatalf("auth.OpenDatabase() error = %v", err)
	}
	t.Cleanup(func() { _ = authStore.Close() })
	if _, err := authStore.CreateUserWithRole(context.Background(), "admin", "change-me-123", auth.RoleAdmin); err != nil {
		t.Fatalf("CreateUserWithRole() error = %v", err)
	}
	jwtManager, err := auth.NewJWTManager(auth.JWTOptions{Secret: []byte("0123456789abcdef0123456789abcdef"), TTL: time.Hour})
	if err != nil {
		t.Fatalf("NewJWTManager() error = %v", err)
	}

	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, RouteOptions{
		AuthStore:           authStore,
		AuthVerifier:        authStore,
		MonitorAuthVerifier: jwtManager,
		MonitorJWT:          jwtManager,
		SessionTTL:          time.Hour,
	})

	login := func(password string) *httptest.ResponseRecorder {
		body := `{"username":"admin","password":"` + password + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(body))
		req.RemoteAddr = "198.51.100.7:5555"
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}

	// Oversized bodies are rejected before decoding, so the unauthenticated
	// endpoint cannot be used to make the process allocate.
	big := strings.Repeat("a", 70<<10)
	bigReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"`+big+`"}`))
	bigRR := httptest.NewRecorder()
	mux.ServeHTTP(bigRR, bigReq)
	if bigRR.Code != http.StatusBadRequest {
		t.Fatalf("oversized login = %d, want 400", bigRR.Code)
	}

	for i := 0; i < loginMaxFailures; i++ {
		if rr := login("wrong-password"); rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, rr.Code)
		}
	}
	blocked := login("change-me-123")
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked login = %d, want 429; body=%s", blocked.Code, blocked.Body.String())
	}
	if blocked.Header().Get("Retry-After") == "" {
		t.Fatal("429 response has no Retry-After header")
	}
	var payload map[string]string
	if err := json.Unmarshal(blocked.Body.Bytes(), &payload); err != nil || payload["error"] == "" {
		t.Fatalf("blocked body = %s", blocked.Body.String())
	}
}
