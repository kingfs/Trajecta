package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// TestAggregatedModelListOnlyNamesRoutableModels pins the catalog an SDK reads before it sends
// anything: every id in `GET /v1/models` must be a model the same proxy will route, and a
// disabled target must not contribute ids.
//
// The list is answered by the proxy itself (handler.go:1157 -> serveAggregatedModelList), not by
// an upstream, so it is the proxy's own claim about what it can serve. A client that enumerates
// it and then requests one of the ids must not get a 502 for a model the proxy advertised, and a
// model that a disabled channel happens to declare must not be advertised at all.
func TestAggregatedModelListOnlyNamesRoutableModels(t *testing.T) {
	upstream := newStubUpstream(t)
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "enabled",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: router.ModelDiscoveryStaticOnly,
				StaticModels:   []string{"model-enabled", "model-shared"},
				Upstream: config.UpstreamConfig{
					BaseURL:        upstream.URL + "/v1",
					ApiKey:         "placeholder-key",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "disabled",
				Enabled:        boolPtr(false),
				Priority:       90,
				ModelDiscovery: router.ModelDiscoveryStaticOnly,
				StaticModels:   []string{"model-disabled", "model-shared"},
				Upstream: config.UpstreamConfig{
					BaseURL:        upstream.URL + "/unused/v1",
					ApiKey:         "placeholder-key",
					ProviderPreset: "openai",
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

	resp, err := http.Get(proxySrv.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models error = %v", err)
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read catalog error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/models status = %d, body = %s", resp.StatusCode, raw)
	}
	var catalog struct {
		Data []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created *int64 `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatalf("decode catalog %s: %v", raw, err)
	}
	listed := map[string]bool{}
	for _, item := range catalog.Data {
		listed[item.ID] = true
		// The list is consumed by typed OpenAI clients, whose Model type requires all four
		// fields; a locally answered entry that omits one fails client-side validation even
		// though the JSON is well formed.
		if item.Object != "model" || item.OwnedBy == "" {
			t.Errorf("catalog entry %q = %+v, want object \"model\" and a non-empty owned_by", item.ID, item)
		}
		if item.Created == nil {
			t.Errorf("catalog entry %q has no created field; the OpenAI Model type requires it", item.ID)
		}
	}
	if len(listed) == 0 {
		t.Fatalf("catalog is empty, want the enabled target's models; body = %s", raw)
	}
	if listed["model-disabled"] {
		t.Errorf("catalog advertises %q, which only the disabled target declares", "model-disabled")
	}
	if !listed["model-enabled"] || !listed["model-shared"] {
		t.Errorf("catalog = %v, want model-enabled and model-shared from the enabled target", listed)
	}

	for model := range listed {
		body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
		chatResp, err := http.Post(proxySrv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST /v1/chat/completions for %q error = %v", model, err)
		}
		chatBody, _ := io.ReadAll(chatResp.Body)
		chatResp.Body.Close()
		if chatResp.StatusCode != http.StatusOK {
			t.Errorf("catalog advertises %q but the proxy answers %d: %s", model, chatResp.StatusCode, chatBody)
		}

		// The detail endpoint synthesizes the same object, so it has to carry the same shape.
		detailResp, err := http.Get(proxySrv.URL + "/v1/models/" + model)
		if err != nil {
			t.Fatalf("GET /v1/models/%s error = %v", model, err)
		}
		detailBody, _ := io.ReadAll(detailResp.Body)
		detailResp.Body.Close()
		if detailResp.StatusCode != http.StatusOK {
			t.Fatalf("GET /v1/models/%s status = %d, body = %s", model, detailResp.StatusCode, detailBody)
		}
		var detail struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created *int64 `json:"created"`
			OwnedBy string `json:"owned_by"`
		}
		if err := json.Unmarshal(detailBody, &detail); err != nil {
			t.Fatalf("decode detail %s: %v", detailBody, err)
		}
		if detail.ID != model || detail.Object != "model" || detail.OwnedBy == "" || detail.Created == nil {
			t.Errorf("GET /v1/models/%s = %s, want the id, object, owned_by and created fields the list entry carries", model, detailBody)
		}
	}
}
