package monitor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/auth"
)

func TestMonitorAuthStatusFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name     string
		verifier auth.TokenVerifier
		status   int
	}{
		{"missing", nil, http.StatusServiceUnavailable},
		{"configured", rejectingTokenVerifier{}, http.StatusOK},
		{"explicit fixture", auth.AllowAnonymousForTest(), http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			authStatusAPIHandler(test.verifier)(recorder, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
			if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), `"auth_required":true`) {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestEveryMonitorAPIRouteFailsClosedWithoutVerifier(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil)
	for _, pattern := range monitorAPIRoutePatternsFromSource(t) {
		if _, public := publicMonitorAPIRoutes[pattern]; public {
			continue
		}
		t.Run(pattern, func(t *testing.T) {
			path := pattern
			if strings.HasSuffix(path, "/") {
				path += "unauthenticated-probe"
			}
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503; body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestMonitorConfiguredVerifierCannotBeBypassedByContext(t *testing.T) {
	next := func(http.ResponseWriter, *http.Request) { t.Error("handler bypassed verifier") }
	request := httptest.NewRequest(http.MethodGet, "/api/traces", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: 1, Role: auth.RoleAdmin}))
	recorder := httptest.NewRecorder()
	monitorAuthRequired(next, rejectingTokenVerifier{})(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestMonitorInternalPrincipalPreservesAdminGate(t *testing.T) {
	for _, test := range []struct {
		role   string
		status int
	}{
		{auth.RoleAdmin, http.StatusNoContent},
		{"user", http.StatusForbidden},
	} {
		t.Run(test.role, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/configuration", nil)
			request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: 1, Role: test.role}))
			recorder := httptest.NewRecorder()
			monitorAdminRequired(func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(http.StatusNoContent)
			}, nil)(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
		})
	}
}

func TestAnonymousRouteFixturePreservesAuthVerifierFallback(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutesForTest(mux, nil, RouteOptions{AuthVerifier: rejectingTokenVerifier{}})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/auth/check", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}
