package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/upstream"
)

// The forwarding path writes the upstream exchange itself (it does not use
// httputil.ReverseProxy), so this test pins the header policy on the path that
// actually runs: end-to-end headers pass through, connection-scoped headers are
// dropped, and X-Forwarded-For gains the client IP.
func TestApplyForwardingHeadersStripsHopByHopAndAppendsClientIP(t *testing.T) {
	t.Parallel()

	original := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", nil)
	original.RemoteAddr = "192.0.2.9:1234"
	original.Header.Set("Connection", "keep-alive, X-Hop-Test")
	original.Header.Set("Keep-Alive", "timeout=5")
	original.Header.Set("X-Hop-Test", "must-not-leak")
	original.Header.Set("Proxy-Authorization", "Basic c2VjcmV0")
	original.Header.Set("Forwarded", "for=1.2.3.4;proto=https")
	original.Header.Set("X-Forwarded-Host", "example.test")
	original.Header.Set("X-Forwarded-Proto", "https")
	original.Header.Add("X-Forwarded-For", "10.0.0.1")
	original.Header.Add("X-Forwarded-For", "10.0.0.2")

	outreq := httptest.NewRequest(http.MethodPost, "http://upstream.local/v1/chat/completions", nil)
	applyForwardingHeaders(outreq, original)

	if got := outreq.Header.Get("Connection"); got != "" {
		t.Fatalf("Connection forwarded: %q", got)
	}
	if got := outreq.Header.Get("X-Hop-Test"); got != "" {
		t.Fatalf("Connection-named header forwarded: %q", got)
	}
	if got := outreq.Header.Get("Keep-Alive"); got != "" {
		t.Fatalf("Keep-Alive forwarded: %q", got)
	}
	if got := outreq.Header.Get("Proxy-Authorization"); got != "" {
		t.Fatalf("Proxy-Authorization forwarded: %q", got)
	}
	for name, want := range map[string]string{
		"Forwarded":         "for=1.2.3.4;proto=https",
		"X-Forwarded-Host":  "example.test",
		"X-Forwarded-Proto": "https",
		"X-Forwarded-For":   "10.0.0.1, 10.0.0.2, 192.0.2.9",
	} {
		if got := outreq.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// The real forwarding helper is wired into the path that performs the
// round-trip, not only into a helper that a future reverse-proxy migration
// might call.
func TestSendUpstreamRequestUsesForwardingHeaderPolicy(t *testing.T) {
	t.Parallel()

	var received http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	original := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", nil)
	original.RemoteAddr = "192.0.2.9:1234"
	original.Header.Set("Connection", "X-Hop-Test")
	original.Header.Set("X-Hop-Test", "must-not-leak")
	original.Header.Set("Proxy-Authorization", "Basic c2VjcmV0")
	original.Header.Set("Forwarded", "for=1.2.3.4;proto=https")
	original.Header.Set("X-Forwarded-For", "10.0.0.1")

	handler := &Handler{transport: &http.Transport{}}
	target := &router.Target{Upstream: upstream.ResolvedUpstream{
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-test",
	}}
	resp, err := handler.sendUpstreamRequestWithPath(original, target, []byte(`{}`), "", nil)
	if err != nil {
		t.Fatalf("sendUpstreamRequestWithPath: %v", err)
	}
	defer resp.Body.Close()

	if got := received.Get("X-Hop-Test"); got != "" {
		t.Fatalf("X-Hop-Test reached the upstream: %q", got)
	}
	if got := received.Get("Proxy-Authorization"); got != "" {
		t.Fatalf("Proxy-Authorization reached the upstream: %q", got)
	}
	if got, want := received.Get("Forwarded"), "for=1.2.3.4;proto=https"; got != want {
		t.Fatalf("Forwarded = %q, want %q", got, want)
	}
	if got, want := received.Get("X-Forwarded-For"), "10.0.0.1, 192.0.2.9"; got != want {
		t.Fatalf("X-Forwarded-For = %q, want %q", got, want)
	}
	if got, want := received.Get("Authorization"), "Bearer sk-test"; got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}
}
