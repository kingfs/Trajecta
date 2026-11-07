package proxy

// End-to-end coverage for opt-in protocol conversion in the forwarding hot
// path: a /v1/messages client request is converted to Chat Completions for a
// chat-only upstream, and the chat response is converted back to Messages.
// The cassette must keep the client-visible bytes on both sides.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/upstream"
	"github.com/kingfs/Trajecta/pkg/protocolconvert"
)

func TestHandlerConvertsMessagesToChatWhenEnabled(t *testing.T) {
	var mu sync.Mutex
	var upstreamPath string
	var upstreamBody []byte

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		upstreamPath = r.URL.Path
		upstreamBody = body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"claude-3-7-sonnet","choices":[{"index":0,"message":{"role":"assistant","content":"Hi there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "chat-only",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: router.ModelDiscoveryStaticOnly,
				StaticModels:   []string{"claude-3-7-sonnet"},
				Upstream: config.UpstreamConfig{
					BaseURL:        upstream.URL + "/v1",
					ProviderPreset: "openai",
					APIType:        "chat_completions",
				},
			},
		},
	}
	cfg.Debug.OutputDir = outputDir
	cfg.Router.Selection.ProtocolConversion = true

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	clientBody := `{"model":"claude-3-7-sonnet","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"Hello"}]}]}`
	req, err := http.NewRequest(http.MethodPost, proxyServer.URL+"/v1/messages", bytes.NewBufferString(clientBody))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")

	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resp.StatusCode = %d, body = %s", resp.StatusCode, respBody)
	}

	mu.Lock()
	if upstreamPath != "/v1/chat/completions" {
		t.Errorf("upstream path = %q, want /v1/chat/completions (converted)", upstreamPath)
	}
	var converted map[string]any
	if err := json.Unmarshal(upstreamBody, &converted); err != nil {
		t.Fatalf("upstream body is not JSON: %v (%s)", err, upstreamBody)
	}
	if converted["model"] != "claude-3-7-sonnet" {
		t.Errorf("converted body model = %v", converted["model"])
	}
	if !strings.Contains(string(upstreamBody), "Hello") {
		t.Errorf("converted upstream body lost user text: %s", upstreamBody)
	}
	mu.Unlock()

	// The client receives Messages-shaped JSON, not the upstream chat shape.
	var clientResp map[string]any
	if err := json.Unmarshal(respBody, &clientResp); err != nil {
		t.Fatalf("client response is not JSON: %v (%s)", err, respBody)
	}
	if clientResp["type"] != "message" {
		t.Errorf("client response type = %v, want message (converted back)", clientResp["type"])
	}
	if !strings.Contains(string(respBody), "Hi there") {
		t.Errorf("client response lost text: %s", respBody)
	}

	// The cassette records the client-visible request body, not the
	// converted wire body.
	recordPath := findRecordedHTTP(t, outputDir)
	raw := mustReadFile(t, recordPath)
	if !strings.Contains(string(raw), `"type":"text"`) {
		t.Errorf("cassette does not contain the original client body:\n%s", raw)
	}
	if strings.Contains(string(raw), "chat.completion") && !strings.Contains(string(raw), `"type":"text"`) {
		t.Errorf("cassette appears to record the converted body instead of the client body")
	}
}

func TestHandlerRejectsMessagesWhenConversionDisabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be contacted when conversion is disabled")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "chat-only",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: router.ModelDiscoveryStaticOnly,
				StaticModels:   []string{"claude-3-7-sonnet"},
				Upstream: config.UpstreamConfig{
					BaseURL:        upstream.URL + "/v1",
					ProviderPreset: "openai",
					APIType:        "chat_completions",
				},
			},
		},
	}
	cfg.Debug.OutputDir = outputDir
	// ProtocolConversion left at its zero value (false).

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	clientBody := `{"model":"claude-3-7-sonnet","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"Hello"}]}]}`
	req, err := http.NewRequest(http.MethodPost, proxyServer.URL+"/v1/messages", bytes.NewBufferString(clientBody))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")

	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("resp.StatusCode = %d, want 502 (no supporting target without conversion)", resp.StatusCode)
	}
}

func TestConversionForSelectionRejectsCompressedBody(t *testing.T) {
	h := &Handler{cfg: func() *config.Config {
		cfg := &config.Config{}
		cfg.Router.Selection.ProtocolConversion = true
		return cfg
	}()}
	target := &router.Target{Upstream: func() upstream.ResolvedUpstream {
		// Build from a minimal chat upstream via the string zero value is
		// not enough; instead exercise the gzip guard before any caps check
		// by asserting the error message shape.
		return upstream.ResolvedUpstream{}
	}()}
	_ = target
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Content-Encoding", "gzip")
	sel := &router.Selection{Target: target, Request: router.RequestFeatures{ModelName: "m"}}
	if _, err := h.conversionForSelection(req, sel, []byte(`{"model":"m"}`)); err == nil || !strings.Contains(err.Error(), "Content-Encoding") {
		t.Fatalf("compressed body must error explicitly, got %v", err)
	}
	req2 := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(`{"model":"m"}`))
	if conv, err := h.conversionForSelection(req2, sel, []byte(`{"model":"m"}`)); conv != nil || err != nil {
		t.Errorf("count_tokens must never convert (conv=%v err=%v)", conv, err)
	}
}

func TestConvertResponseBodyEmptyAndOversized(t *testing.T) {
	conversion := &protocolConversion{
		clientProtocol:   protocolconvert.ProtocolMessages,
		upstreamProtocol: protocolconvert.ProtocolChatCompletions,
		wireModel:        "m",
	}
	// Empty 2xx body passes through unconverted instead of failing.
	emptyResp := &http.Response{
		StatusCode: http.StatusNoContent,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
	}
	out, err := conversion.convertResponseBody(emptyResp, 1<<20)
	if err != nil {
		t.Fatalf("empty body: %v", err)
	}
	if out != emptyResp {
		t.Error("empty body response should pass through unchanged")
	}
	// Body over the cap fails the candidate instead of buffering unboundedly.
	bigResp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", 100))),
	}
	if _, err := conversion.convertResponseBody(bigResp, 10); err == nil {
		t.Error("oversized body should error")
	}
	// Error responses pass through unconverted.
	errResp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"error":"upstream boom"}`)),
	}
	out, err = conversion.convertResponseBody(errResp, 1<<20)
	if err != nil {
		t.Fatalf("error response: %v", err)
	}
	if out != errResp {
		t.Error("error response should pass through unconverted")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}
