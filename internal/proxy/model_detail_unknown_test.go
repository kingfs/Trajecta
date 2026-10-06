package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// TestModelDetailAnswersOnlyForKnownModels pins the rule GET /v1/models/{id} follows.
//
// The detail view used to answer 200 with a synthesized object for any id, so `models.retrieve`
// with a typo returned a model that does not exist - indistinguishable from a real one now that
// the object carries every field the OpenAI schema requires. It answers 200 for a model the proxy
// can route, matched case-insensitively like every other model lookup, and for a model the registry
// has metadata for; anything else is the 404 model_not_found the OpenAI clients expect.
func TestModelDetailAnswersOnlyForKnownModels(t *testing.T) {
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
			ID:             "routable",
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"glm-5.1"},
			Upstream: config.UpstreamConfig{
				BaseURL:        upstream.URL + "/v1",
				ApiKey:         "placeholder-key",
				ProviderPreset: "openai",
				APIType:        "chat_completions",
			},
		}},
	}
	cfg.Debug.OutputDir = t.TempDir()
	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxySrv := newStubProxyServer(t, handler)

	for _, tc := range []struct {
		name       string
		model      string
		wantStatus int
	}{
		{name: "routable", model: "glm-5.1", wantStatus: http.StatusOK},
		{name: "routable with different case", model: "GLM-5.1", wantStatus: http.StatusOK},
		{name: "registry metadata only", model: "gpt-5", wantStatus: http.StatusOK},
		{name: "typo", model: "glm-5.11", wantStatus: http.StatusNotFound},
		{name: "empty registry and no route", model: "definitely-not-a-model", wantStatus: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(proxySrv.URL + "/v1/models/" + tc.model)
			if err != nil {
				t.Fatalf("GET /v1/models/%s error = %v", tc.model, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("GET /v1/models/%s status = %d, want %d, body = %s", tc.model, resp.StatusCode, tc.wantStatus, body)
			}
			if contentTypes := resp.Header.Get("Content-Type"); contentTypes != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", contentTypes)
			}

			if tc.wantStatus == http.StatusOK {
				var entry struct {
					ID      string `json:"id"`
					Object  string `json:"object"`
					Created *int64 `json:"created"`
					OwnedBy string `json:"owned_by"`
				}
				if err := json.Unmarshal(body, &entry); err != nil {
					t.Fatalf("decode body %s: %v", body, err)
				}
				if entry.ID != tc.model || entry.Object != "model" || entry.OwnedBy == "" || entry.Created == nil {
					t.Fatalf("GET /v1/models/%s = %s, want the OpenAI model object for the requested id", tc.model, body)
				}
				return
			}

			var envelope struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Code    string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("decode error body %s: %v", body, err)
			}
			if envelope.Error.Code != "model_not_found" {
				t.Fatalf("error code = %q, want model_not_found; body = %s", envelope.Error.Code, body)
			}
			if envelope.Error.Type != "invalid_request_error" || envelope.Error.Message == "" {
				t.Fatalf("error envelope = %s, want an invalid_request_error with a message", body)
			}
		})
	}
}
