package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// A provider 404 is the most common "my model does not work" report, and the
// provider's own message is the only thing that separates a retired model id
// from a wrong base_url. The final error must carry the channel name, the path
// and that message instead of an opaque id.
func TestUpstream404ErrorCarriesProviderNamePathAndBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"message":"model gpt-5 is not available on this key","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(upstream.Close)

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:                   "provider-uuid",
			Name:                 "Mistral Aggregator",
			Enabled:              boolPtr(true),
			Priority:             100,
			ModelDiscovery:       router.ModelDiscoveryStaticOnly,
			StaticModels:         []string{"gpt-5"},
			ConfiguredModelsOnly: true,
			Upstream: config.UpstreamConfig{
				BaseURL:        upstream.URL + "/v1",
				ApiKey:         "k",
				ProviderPreset: "openai",
				APIType:        "chat_completions",
			},
		}},
	}
	cfg.Debug.OutputDir = t.TempDir()
	cfg.Debug.MaskKey = true
	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxySrv := newStubProxyServer(t, handler)

	resp, err := http.Post(proxySrv.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("POST error = %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", resp.StatusCode, text)
	}
	for _, want := range []string{
		"Mistral Aggregator",
		"provider-uuid",
		"/v1/chat/completions",
		"model gpt-5 is not available on this key",
		"does not serve this model id",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("error body = %s, want it to contain %q", text, want)
		}
	}
}
