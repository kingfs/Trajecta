package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// TestOllamaShowUsesTheDocumentedRequestFieldAndRefusesUnknownModels pins the two halves of the
// POST /api/show contract.
//
// Ollama's API reference documents `{"model": "..."}` (its own ShowRequest moved from `name` to
// `model`), but the proxy only decoded `name`, so clients following the documentation were told
// their body had no model at all. Older clients that still send `name` must keep working. The
// endpoint also synthesizes a manifest for any model, so - exactly like GET /v1/models/{id} - an
// unknown model now gets the 404 an Ollama client expects, in Ollama's `{"error": "<message>"}`
// shape rather than an OpenAI envelope.
func TestOllamaShowUsesTheDocumentedRequestFieldAndRefusesUnknownModels(t *testing.T) {
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

	post := func(t *testing.T, body string) (int, []byte) {
		t.Helper()
		resp, err := http.Post(proxySrv.URL+"/api/show", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST /api/show error = %v", err)
		}
		defer resp.Body.Close()
		payload, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, payload
	}

	t.Run("documented model field", func(t *testing.T) {
		status, body := post(t, `{"model":"glm-5.1"}`)
		if status != http.StatusOK {
			t.Fatalf("POST /api/show {\"model\":\"glm-5.1\"} status = %d, want 200, body = %s", status, body)
		}
		var entry struct {
			ID     string `json:"id"`
			Object string `json:"object"`
		}
		if err := json.Unmarshal(body, &entry); err != nil {
			t.Fatalf("decode body %s: %v", body, err)
		}
		if entry.ID != "glm-5.1" || entry.Object != "model" {
			t.Fatalf("POST /api/show body = %s, want the manifest for glm-5.1", body)
		}
	})

	t.Run("legacy name field", func(t *testing.T) {
		status, body := post(t, `{"name":"glm-5.1"}`)
		if status != http.StatusOK {
			t.Fatalf("POST /api/show {\"name\":\"glm-5.1\"} status = %d, want 200, body = %s", status, body)
		}
	})

	t.Run("registry metadata only", func(t *testing.T) {
		status, body := post(t, `{"model":"gpt-5"}`)
		if status != http.StatusOK {
			t.Fatalf("POST /api/show {\"model\":\"gpt-5\"} status = %d, want 200, body = %s", status, body)
		}
	})

	t.Run("unknown model", func(t *testing.T) {
		status, body := post(t, `{"model":"glm-5.11"}`)
		if status != http.StatusNotFound {
			t.Fatalf("POST /api/show for an unknown model status = %d, want 404, body = %s", status, body)
		}
		var envelope struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("decode body %s as Ollama's error shape: %v", body, err)
		}
		if strings.TrimSpace(envelope.Error) == "" {
			t.Fatalf("POST /api/show unknown-model body = %s, want Ollama's {\"error\":\"<message>\"} shape", body)
		}
		if strings.Contains(string(body), `"code"`) {
			t.Fatalf("POST /api/show unknown-model body = %s, want the Ollama error shape rather than the OpenAI envelope", body)
		}
	})

	t.Run("no model at all", func(t *testing.T) {
		status, body := post(t, `{}`)
		if status != http.StatusBadRequest {
			t.Fatalf("POST /api/show with an empty body status = %d, want 400, body = %s", status, body)
		}
		var envelope struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil || strings.TrimSpace(envelope.Error) == "" {
			t.Fatalf("POST /api/show with an empty body = %s, want the Ollama error shape, decode error = %v", body, err)
		}
	})
}
