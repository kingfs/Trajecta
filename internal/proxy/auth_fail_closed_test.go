package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
)

func TestProxyMissingVerifierFailsClosed(t *testing.T) {
	handler := &Handler{cfg: &config.Config{}}
	for _, path := range []string{"/v1/models", "/v1/models/test", "/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1beta/models/test:generateContent", "/tokenize"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", recorder.Code)
			}
		})
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", recorder.Code)
	}
}
