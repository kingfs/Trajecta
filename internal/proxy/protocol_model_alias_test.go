package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/store"
)

func TestProxyUsesPerProtocolWireModel(t *testing.T) {
	var (
		mu       sync.Mutex
		gotModel string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotModel = string(body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl","model":"wire-chat","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "multi",
			Enabled:        boolPtr(true),
			ModelDiscovery: "static_only",
			StaticModels:   []string{"gpt-5"},
			ProtocolModelAliases: map[string]map[string]string{
				"chat_completions": {"gpt-5": "wire-chat"},
			},
			Upstream: config.UpstreamConfig{
				BaseURL: upstream.URL + "/v1", ApiKey: "k",
				ProviderPreset: "openai", APIType: "chat_completions",
			},
		}},
	}
	cfg.Debug.OutputDir = t.TempDir()
	cfg.Debug.MaskKey = true
	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewBufferString(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if !bytes.Contains([]byte(gotModel), []byte(`"model":"wire-chat"`)) {
		t.Fatalf("upstream body = %s", gotModel)
	}
}
