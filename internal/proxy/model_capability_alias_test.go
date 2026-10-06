package proxy

import (
	"encoding/json"
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

// TestAliasResolvedModelKeepsTheTargetCapabilities pins the hot-path half of the rule the routing
// inspector shares (TestRoutingInspectResolvesAliasedModelCapabilities).
//
// The router keys per-model capability overrides by the model name in the client request, and
// channel.ChannelModelCapabilities gives an alias name its target row's overrides. An alias whose
// name is also a declared model of the same channel - the natural "alias a public model id to a
// pinned snapshot" case - therefore has to be served with the target's capabilities, not the
// shadowed row's. Here the shadowed row declares native Responses support and the target does
// not, so a `/v1/responses` request for the alias name must be answered by the local runtime,
// which calls the upstream's Chat Completions path with the resolved model, rather than being
// forwarded unchanged to a target that does not implement the Responses API.
func TestAliasResolvedModelKeepsTheTargetCapabilities(t *testing.T) {
	type call struct {
		path  string
		model string
	}

	var (
		mu    sync.Mutex
		calls []call
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &payload)
		mu.Lock()
		calls = append(calls, call{path: r.URL.Path, model: payload.Model})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	yes, no := true, false
	chatOnly := config.UpstreamCapabilitiesConfig{ChatCompletions: &yes, Responses: &no}
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "alias-shadowing",
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"fast", "slow"},
			ModelAliases:   map[string]string{"fast": "slow"},
			Upstream: config.UpstreamConfig{
				BaseURL:        upstream.URL + "/v1",
				ApiKey:         "placeholder-key",
				ProviderPreset: "openai",
				APIType:        "chat_completions",
				// The shape channel.ChannelModelCapabilities projects for a channel whose
				// `fast` row is aliased onto its `slow` row: the alias key carries the target's
				// overrides.
				ModelCapabilities: map[string]config.UpstreamCapabilitiesConfig{"fast": chatOnly, "slow": chatOnly},
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

	resp, err := http.Post(proxySrv.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"fast","input":"hi"}`))
	if err != nil {
		t.Fatalf("POST /v1/responses error = %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/responses status = %d, body = %s", resp.StatusCode, body)
	}

	mu.Lock()
	got := append([]call(nil), calls...)
	mu.Unlock()
	if len(got) == 0 {
		t.Fatal("upstream received no request; the local runtime was expected to call it")
	}
	for _, item := range got {
		if item.path == "/v1/responses" {
			t.Fatalf("upstream received %s with model %q; the request must not be forwarded unchanged, because the alias target does not implement the Responses API (calls = %+v)", item.path, item.model, got)
		}
		if item.path != "/v1/chat/completions" {
			t.Fatalf("upstream received unexpected path %s (calls = %+v)", item.path, got)
		}
		if item.model != "slow" {
			t.Fatalf("upstream received model %q, want the alias target %q (calls = %+v)", item.model, "slow", got)
		}
	}
}
