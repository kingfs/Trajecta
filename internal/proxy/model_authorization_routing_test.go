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

// A provider serves exactly the models the operator enabled. A name that is not
// declared, or is declared but disabled, is answered by the proxy itself with a
// 404 and never reaches the upstream, so a typo cannot turn into a provider 404
// wrapped in a 502.
func TestUndeclaredOrDisabledModelIsRejectedWithoutCallingUpstream(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		wantCode string
	}{
		{name: "unknown", model: "undeclared", wantCode: router.SelectionFailureModelUnknown},
		{name: "disabled", model: "off", wantCode: router.SelectionFailureModelNotEnabled},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu    sync.Mutex
				calls []string
			)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var payload struct {
					Model string `json:"model"`
				}
				_ = json.Unmarshal(body, &payload)
				mu.Lock()
				calls = append(calls, payload.Model)
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

			cfg := &config.Config{
				Upstreams: []config.UpstreamTargetConfig{{
					ID:                   "declared-only",
					Enabled:              boolPtr(true),
					Priority:             100,
					ModelDiscovery:       router.ModelDiscoveryStaticOnly,
					StaticModels:         []string{"declared"},
					DisabledModels:       []string{"off"},
					ConfiguredModelsOnly: true,
					Upstream: config.UpstreamConfig{
						BaseURL:        upstream.URL + "/v1",
						ApiKey:         "placeholder-key",
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
				strings.NewReader(`{"model":"`+tc.model+`","messages":[{"role":"user","content":"hi"}]}`))
			if err != nil {
				t.Fatalf("POST /v1/chat/completions error = %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404, body = %s", resp.StatusCode, body)
			}
			if !strings.Contains(string(body), tc.wantCode) {
				t.Fatalf("body = %s, want code %q", body, tc.wantCode)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(calls) != 0 {
				t.Fatalf("upstream received %v, want no request", calls)
			}
		})
	}
}

// The declared and enabled model still routes, so the rejection above is
// authorization and not a broken fixture.
func TestDeclaredModelStillRoutes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID: "declared-only", Enabled: boolPtr(true), Priority: 100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly, StaticModels: []string{"declared"},
			ConfiguredModelsOnly: true,
			Upstream:             config.UpstreamConfig{BaseURL: upstream.URL + "/v1", ApiKey: "k", ProviderPreset: "openai", APIType: "chat_completions"},
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
		strings.NewReader(`{"model":"declared","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("POST error = %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
}
