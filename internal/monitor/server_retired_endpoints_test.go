package monitor

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kingfs/Trajecta/internal/auth"
)

// TestRetiredAPIOnlyEndpointsAnswer404 pins the P3 retirement of the API-only
// endpoints that had no UI, MCP or CLI caller left:
//
//   - GET /api/routing/exchanges and GET /api/routing/summary (the routing facts
//     stay on each trace row and in the overview/analytics failure views),
//   - GET/POST /api/secrets/local-key (the CLI `db secret` reads the store
//     directly and remains the operator entry point),
//   - POST /api/provider-setup/validate and /apply (the create dialog uses
//     /api/provider-probe plus /api/channels),
//   - POST /api/traces/{id}/scan (CLI `analyze scan` and the MCP reanalyze tool
//     call the reanalysis service directly).
//
// A retired path is simply not registered, so it falls through to appHandler,
// which answers 404 for every /api/* path. The neighbouring live surface must
// keep working: the trace dispatcher itself, the reparse action next to the
// removed scan branch, and the routing inspector.
func TestRetiredAPIOnlyEndpointsAnswer404(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	content := buildRecordFixture(t, "/v1/responses", false, `{"model":"gpt-5.1","input":"hello"}`, `{"output_text":"done"}`)
	writeTraceFixture(t, outputDir, "retired-endpoints.http", content)

	st, err := newTestStore(outputDir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()
	syncStore(t, st)
	entry, err := st.GetByRequestID("req_1")
	if err != nil {
		t.Fatalf("GetByRequestID() error = %v", err)
	}

	admin := fixedPrincipalVerifier{principal: auth.Principal{Username: "admin", Role: auth.RoleAdmin}, ok: true}
	mux := http.NewServeMux()
	registerRoutesForTest(mux, st, RouteOptions{
		AuthVerifier:        admin,
		MonitorAuthVerifier: admin,
	})

	retired := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/routing/exchanges"},
		{http.MethodGet, "/api/routing/summary"},
		{http.MethodGet, "/api/routing/summary?window=all"},
		{http.MethodGet, "/api/secrets/local-key"},
		{http.MethodGet, "/api/secrets/local-key?export=1"},
		{http.MethodPost, "/api/secrets/local-key?rotate=1"},
		{http.MethodPost, "/api/provider-setup/validate"},
		{http.MethodPost, "/api/provider-setup/apply"},
		// The trace exists, so this 404 comes from the dispatcher's removed
		// scan branch, not from a missing trace.
		{http.MethodPost, "/api/traces/" + entry.ID + "/scan"},
	}
	for _, tc := range retired {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(`{}`))
		req.Header.Set("Authorization", "Bearer token")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404 (retired endpoint); body=%s", tc.method, tc.path, rr.Code, rr.Body.String())
		}
	}

	// The trace dispatcher keeps serving the surviving actions.
	detail := httptest.NewRequest(http.MethodGet, "/api/traces/"+entry.ID, nil)
	detail.Header.Set("Authorization", "Bearer token")
	detailRR := httptest.NewRecorder()
	mux.ServeHTTP(detailRR, detail)
	if detailRR.Code != http.StatusOK {
		t.Fatalf("GET trace detail = %d, want 200; body=%s", detailRR.Code, detailRR.Body.String())
	}

	reparse := httptest.NewRequest(http.MethodPost, "/api/traces/"+entry.ID+"/reparse", bytes.NewBufferString(`{"mode":"sync"}`))
	reparse.Header.Set("Authorization", "Bearer token")
	reparseRR := httptest.NewRecorder()
	mux.ServeHTTP(reparseRR, reparse)
	if reparseRR.Code == http.StatusNotFound {
		t.Fatalf("POST trace reparse = 404, want the surviving action to stay registered; body=%s", reparseRR.Body.String())
	}

	// The routing inspector stays registered; without a live router it reports
	// 503 rather than the retired 404.
	inspect := httptest.NewRequest(http.MethodPost, "/api/routing/inspect", bytes.NewBufferString(`{"endpoint":"responses","model":"gpt-5.1"}`))
	inspect.Header.Set("Authorization", "Bearer token")
	inspectRR := httptest.NewRecorder()
	mux.ServeHTTP(inspectRR, inspect)
	if inspectRR.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/routing/inspect without router = %d, want 503; body=%s", inspectRR.Code, inspectRR.Body.String())
	}

	// The store keeps the secret methods the CLI uses; retirement removed only
	// the HTTP surface.
	if status := st.SecretStatus(); status.Mode == "" {
		t.Fatalf("SecretStatus() = %+v, want the store method to stay available", status)
	}
}
