package testnet

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestGuardTransportBlocksNonLoopback(t *testing.T) {
	tr := GuardTransport{}
	req, err := http.NewRequest(http.MethodGet, "https://ai-api-gateway.example/api/openai/v1/models", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	_, err = tr.RoundTrip(req)
	if err == nil {
		t.Fatalf("RoundTrip() error = nil, want the guard to block a non-loopback host")
	}
	if !strings.Contains(err.Error(), "testnet: outbound network access") {
		t.Fatalf("RoundTrip() error = %v, want the guard message", err)
	}
}

func TestGuardTransportAllowsLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	tr := GuardTransport{}
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v, want loopback to pass", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want 200", resp.StatusCode)
	}
}

func TestAllowNetworkReadsEnv(t *testing.T) {
	t.Setenv(AllowEnv, "1")
	if !AllowNetwork() {
		t.Fatalf("AllowNetwork() = false with %s=1", AllowEnv)
	}
	t.Setenv(AllowEnv, "")
	if AllowNetwork() {
		t.Fatalf("AllowNetwork() = true with %s empty", AllowEnv)
	}
}

func TestInstallLoopbackGuardReplacesDefaultTransport(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = original
		http.DefaultClient.Transport = nil
		_ = os.Unsetenv(AllowEnv)
	})

	InstallLoopbackGuard()
	if _, ok := http.DefaultTransport.(GuardTransport); !ok {
		t.Fatalf("DefaultTransport = %T, want testnet.GuardTransport", http.DefaultTransport)
	}
	if _, err := http.Get("https://example.invalid/"); err == nil {
		t.Fatalf("http.Get() error = nil, want the installed guard to block the request")
	}
}
