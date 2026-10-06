package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// TestDisabledCredentialNeverReceivesTraffic pins the client-visible half of the rule the router
// gate covers (TestDisabledCredentialIsLeftOutOfRouting).
//
// The high-priority target's only credential is disabled, so it must not be a route target at all
// and the request has to be served by the lower-priority target instead of being sent to the
// upstream behind the retired key.
func TestDisabledCredentialNeverReceivesTraffic(t *testing.T) {
	var (
		mu       sync.Mutex
		retired  []string
		fallback []string
	)
	retiredUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		retired = append(retired, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":"retired"}`))
	}))
	t.Cleanup(retiredUpstream.Close)
	fallbackUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fallback = append(fallback, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":"fallback"}`))
	}))
	t.Cleanup(fallbackUpstream.Close)

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	disabled := false
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "retired-channel",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: router.ModelDiscoveryStaticOnly,
				StaticModels:   []string{"shared"},
				Upstream: config.UpstreamConfig{
					BaseURL:        retiredUpstream.URL + "/v1",
					ApiKey:         "sk-channel-fallback",
					ProviderPreset: "openai",
					APIType:        "chat_completions",
				},
				Credentials: []config.CredentialConfig{
					{ID: "retired", ApiKey: "sk-retired", Enabled: &disabled},
				},
			},
			{
				ID:             "live-channel",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: router.ModelDiscoveryStaticOnly,
				StaticModels:   []string{"shared"},
				Upstream: config.UpstreamConfig{
					BaseURL:        fallbackUpstream.URL + "/v1",
					ApiKey:         "sk-live",
					ProviderPreset: "openai",
					APIType:        "chat_completions",
				},
			},
		},
	}
	cfg.Debug.OutputDir = t.TempDir()
	cfg.Debug.MaskKey = true
	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxySrv := newStubProxyServer(t, handler)

	resp, err := http.Post(proxySrv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"shared","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("POST /v1/chat/completions error = %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions status = %d, body = %s", resp.StatusCode, body)
	}

	mu.Lock()
	retiredCalls := append([]string(nil), retired...)
	fallbackCalls := append([]string(nil), fallback...)
	mu.Unlock()
	if len(retiredCalls) != 0 {
		t.Fatalf("the upstream behind the disabled credential received %d call(s) with %v, want none: the retired key must not be used", len(retiredCalls), retiredCalls)
	}
	if len(fallbackCalls) != 1 || fallbackCalls[0] != "Bearer sk-live" {
		t.Fatalf("the live upstream received %v, want exactly one call with the live key", fallbackCalls)
	}
}
