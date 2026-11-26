package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kingfs/Trajecta/internal/auth"
)

type staticTokenVerifier struct{}

func (staticTokenVerifier) VerifyToken(_ context.Context, token string) (auth.Principal, bool, error) {
	if token != "good" {
		return auth.Principal{}, false, nil
	}
	return auth.Principal{UserID: 1, Username: "admin", Role: auth.RoleAdmin, Scope: auth.ScopeAll}, true, nil
}

func TestMonitorAuthAcceptsSessionCookie(t *testing.T) {
	handler := monitorAuthRequired(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, staticTokenVerifier{})

	req := httptest.NewRequest(http.MethodGet, "/api/traces", nil)
	req.AddCookie(&http.Cookie{Name: monitorSessionCookie, Value: "good"})
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("cookie auth status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestMonitorAuthRejectsAccessTokenInQuery(t *testing.T) {
	handler := monitorAuthRequired(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, staticTokenVerifier{})

	req := httptest.NewRequest(http.MethodGet, "/api/events/stream?access_token=good", nil)
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("query token status = %d, want 401", rr.Code)
	}
}

func TestMonitorRequestSameOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/events/ws", nil)
	req.Host = "monitor.example.test"
	req.Header.Set("Origin", "https://monitor.example.test")
	if !monitorRequestSameOrigin(req) {
		t.Fatal("same-origin request rejected")
	}

	req.Header.Set("Origin", "https://evil.example.test")
	if monitorRequestSameOrigin(req) {
		t.Fatal("cross-origin request accepted")
	}

	req.Header.Set("Origin", "https://monitor.example.test")
	req.Header.Set("X-Forwarded-Host", "monitor.internal.test")
	if !monitorRequestSameOrigin(req) {
		t.Fatal("forwarded host was not honoured")
	}
}
