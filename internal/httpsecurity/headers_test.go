package httpsecurity

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersPreserveResponseAndStreaming(t *testing.T) {
	for _, tc := range []struct {
		name string
		wrap func(http.Handler) http.Handler
		csp  bool
	}{{"monitor", Monitor, true}, {"proxy", Common, false}} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte("data: unchanged\n\n"))
				w.(http.Flusher).Flush()
			})).ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
			if rr.Code != http.StatusAccepted || rr.Body.String() != "data: unchanged\n\n" || !rr.Flushed || rr.Header().Get("Cache-Control") != "no-cache" {
				t.Fatalf("changed response: %+v", rr)
			}
			for name, want := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer", "Permissions-Policy": "camera=(), geolocation=(), microphone=()"} {
				if got := rr.Header().Get(name); got != want {
					t.Fatalf("%s=%q want %q", name, got, want)
				}
			}
			csp := rr.Header().Get("Content-Security-Policy")
			if tc.csp {
				if !strings.Contains(csp, "script-src 'self';") || !strings.Contains(csp, "frame-ancestors 'none'") {
					t.Fatalf("CSP=%q", csp)
				}
			} else if csp != "" {
				t.Fatalf("proxy must not impose CSP: %q", csp)
			}
		})
	}
}
