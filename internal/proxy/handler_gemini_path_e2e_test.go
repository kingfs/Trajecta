package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

func TestHandlerRewritesNativeGeminiWireModelInPath(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	var gotPath, gotAPIKey string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-goog-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	}))
	defer upstreamServer.Close()

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "google",
			Enabled:        boolPtr(true),
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"gemini-client"},
			ProtocolModelAliases: map[string]map[string]string{
				"gemini": {"gemini-client": "wire-gemini"},
			},
			Upstream: config.UpstreamConfig{
				BaseURL:        upstreamServer.URL + "/v1beta",
				ProviderPreset: "google_genai",
				ProtocolFamily: "google_genai",
				RoutingProfile: "google_ai_studio",
				ApiKey:         "goog-secret",
			},
		}},
	}
	cfg.Debug.OutputDir = outputDir
	cfg.Debug.MaskKey = true

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	req, err := http.NewRequest(http.MethodPost, proxyServer.URL+"/v1beta/models/gemini-client:generateContent", bytes.NewBufferString(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotPath != "/v1beta/models/wire-gemini:generateContent" {
		t.Fatalf("upstream path = %q, want the wire model without a doubled /v1beta", gotPath)
	}
	if gotAPIKey != "goog-secret" {
		t.Fatalf("x-goog-api-key = %q, want goog-secret", gotAPIKey)
	}
}
