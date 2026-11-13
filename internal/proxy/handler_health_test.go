package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthzAnswersWithoutAuthentication(t *testing.T) {
	t.Parallel()

	// proxyTestVerifier rejects every token (handler_test.go); healthz must not
	// consult it at all.
	handler := &Handler{authVerifier: proxyTestVerifier{}}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
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
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/healthz", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD /healthz = %d body %q, want 200 and no body", head.Code, head.Body.String())
	}

	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz = %d, want 405", post.Code)
	}

	// The forwarding surface stays authenticated: healthz is the only bypass.
	api := httptest.NewRecorder()
	handler.ServeHTTP(api, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if api.Code != http.StatusUnauthorized {
		t.Fatalf("GET /v1/models without a token = %d, want 401", api.Code)
	}
}
