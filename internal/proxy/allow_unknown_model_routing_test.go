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

// TestUndeclaredModelIsRoutedOnlyWhenTheTargetAllowsIt pins the forwarding half of the rule the
// routing inspector shares (TestRoutingInspectHonoursAllowUnknownModels).
//
// A target that declares models serves a model outside that set only when allow_unknown_models is
// set; the strict default answers the selection failure instead. The inspector has to describe the
// same decision, and it can only do that if the planner is given the flag.
func TestUndeclaredModelIsRoutedOnlyWhenTheTargetAllowsIt(t *testing.T) {
	cases := []struct {
		name       string
		allow      bool
		wantStatus int
	}{
		{name: "allow-unknown", allow: true, wantStatus: http.StatusOK},
		{name: "strict", allow: false, wantStatus: http.StatusBadGateway},
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

			allow := tc.allow
			cfg := &config.Config{
				Upstreams: []config.UpstreamTargetConfig{{
					ID:                 "declared-only",
					Enabled:            boolPtr(true),
					Priority:           100,
					ModelDiscovery:     router.ModelDiscoveryStaticOnly,
					StaticModels:       []string{"declared"},
					AllowUnknownModels: &allow,
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

			resp, err := http.Post(proxySrv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"undeclared","messages":[{"role":"user","content":"hi"}]}`))
			if err != nil {
				t.Fatalf("POST /v1/chat/completions error = %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("POST /v1/chat/completions status = %d, want %d, body = %s", resp.StatusCode, tc.wantStatus, body)
			}

			mu.Lock()
			got := append([]string(nil), calls...)
			mu.Unlock()
			if !tc.allow {
				if len(got) != 0 {
					t.Fatalf("upstream received %v, want no request: the target declares its models and does not allow unknown ones", got)
				}
				return
			}
			if len(got) != 1 || got[0] != "undeclared" {
				t.Fatalf("upstream received %v, want the undeclared model forwarded unchanged", got)
			}
		})
	}
}
