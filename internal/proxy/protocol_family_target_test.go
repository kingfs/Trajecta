package proxy

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// TestPresetOnlyTargetsServeOnlyTheirProtocolFamily pins the hot-path half of the rule the
// routing inspector now shares (upstream.ResolvedUpstream.SupportsRawPath, covered on the
// inspector side by TestRoutingInspectCapabilitiesFollowTheProtocolFamily).
//
// The shipped example configs name a provider preset and no `api_type`. That is not a Chat
// Completions channel: the resolver derives the protocol family and the api type from the
// preset, and the forwarding path keys on the family, so an Anthropic target answers
// `/v1/messages` and refuses `/v1/chat/completions`, while a Google target answers only its own
// generateContent path. A target that could answer any of the three endpoints regardless of its
// family would mean the proxy translated between protocol families, which it does not.
func TestPresetOnlyTargetsServeOnlyTheirProtocolFamily(t *testing.T) {
	for _, tc := range []struct {
		preset string
		path   string
		body   string
		want   int
	}{
		{"anthropic", "/v1/messages", `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, http.StatusOK},
		{"anthropic", "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, http.StatusBadGateway},
		{"google_genai", "/v1beta/models/m:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, http.StatusOK},
		{"google_genai", "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, http.StatusBadGateway},
		{"google_genai", "/v1/messages", `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, http.StatusBadGateway},
	} {
		t.Run(tc.preset+tc.path, func(t *testing.T) {
			upstream := newStubUpstream(t)
			st, err := store.New(t.TempDir())
			if err != nil {
				t.Fatalf("store.New() error = %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })

			cfg := &config.Config{
				Upstreams: []config.UpstreamTargetConfig{{
					ID:             "preset-only",
					Enabled:        boolPtr(true),
					Priority:       100,
					ModelDiscovery: router.ModelDiscoveryStaticOnly,
					StaticModels:   []string{"m"},
					Upstream: config.UpstreamConfig{
						BaseURL:        upstream.URL,
						ApiKey:         "placeholder-key",
						ProviderPreset: tc.preset,
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

			resp, err := http.Post(proxySrv.URL+tc.path, "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("POST %s error = %v", tc.path, err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatalf("read body error = %v", err)
			}
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d for preset %q on %s; body = %s", resp.StatusCode, tc.want, tc.preset, tc.path, body)
			}
		})
	}
}
