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
	target := &router.Target{Upstream: upstream.ResolvedUpstream{
		ProtocolFamily: upstream.ProtocolFamilyOpenAICompatible,
		APIType:        upstream.APITypeChatCompletions,
	}}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Content-Encoding", "gzip")
	sel := &router.Selection{Target: target, Request: router.RequestFeatures{ModelName: "m"}}
	if _, err := h.conversionForSelection(req, sel, []byte(`{"model":"m"}`)); err == nil || !strings.Contains(err.Error(), "Content-Encoding") {
		t.Fatalf("compressed body must error explicitly, got %v", err)
	}
	// A native target forwards a compressed body verbatim; the guard must not
	// reject it merely because conversion is enabled.
	native := &router.Target{Upstream: upstream.ResolvedUpstream{
		ProtocolFamily: upstream.ProtocolFamilyAnthropicMessages,
		APIType:        upstream.APITypeMessages,
	}}
	nativeSel := &router.Selection{Target: native, Request: router.RequestFeatures{ModelName: "m"}}
	if conv, err := h.conversionForSelection(req, nativeSel, []byte(`{"model":"m"}`)); conv != nil || err != nil {
		t.Errorf("native compressed passthrough must not be rejected (conv=%v err=%v)", conv, err)
	}
	// ?alt=sse on a converting target cannot be streamed.
	sseReq := httptest.NewRequest(http.MethodPost, "/v1beta/models/m:generateContent?alt=sse", strings.NewReader(`{"contents":[]}`))
	if _, err := h.conversionForSelection(sseReq, sel, []byte(`{"contents":[]}`)); err == nil || !strings.Contains(err.Error(), "alt=sse") {
		t.Fatalf("alt=sse conversion must error explicitly, got %v", err)
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

// TestHandlerConversionFailureIs400AndDoesNotPoisonHealth pins two properties:
// a client-caused conversion failure is a 4xx (not a 502 that invites a retry),
// and it never counts as an upstream health failure. Three unsupported requests
// would otherwise open the target's circuit breaker and make the following
// valid native request fail.
func TestHandlerConversionFailureIs400AndDoesNotPoisonHealth(t *testing.T) {
	var mu sync.Mutex
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		upstreamCalls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "chat-only",
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"m"},
			Upstream: config.UpstreamConfig{
				BaseURL:        upstream.URL + "/v1",
				ProviderPreset: "openai",
				APIType:        "chat_completions",
			},
		}},
	}
	cfg.Debug.OutputDir = outputDir
	cfg.Router.Selection.ProtocolConversion = true

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	badBody := `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]}]}`
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodPost, proxyServer.URL+"/v1/messages", bytes.NewBufferString(badBody))
		req.Header.Set("Content-Type", "application/json")
		resp, err := proxyServer.Client().Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("request %d: status = %d, want 400 (body %s)", i, resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "cannot represent") {
			t.Errorf("request %d: body = %s, want the unsupported-content message", i, body)
		}
	}
	mu.Lock()
	if upstreamCalls != 0 {
		t.Errorf("unsupported requests reached the upstream %d times", upstreamCalls)
	}
	mu.Unlock()

	// A valid native request must still route: the three failures above were
	// local and must not have opened the breaker.
	req, _ := http.NewRequest(http.MethodPost, proxyServer.URL+"/v1/chat/completions", bytes.NewBufferString(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("native request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("native request after conversion failures: status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
}

// TestHandlerMessagesToolLoopAgainstChatUpstream is the end-to-end shape of the
// flagship use case: an Anthropic client calls tools through a Chat-only
// upstream. The first turn must return a Messages tool_use with stop_reason
// tool_use, and the second turn must reach the upstream as a role "tool"
// message carrying the result.
func TestHandlerMessagesToolLoopAgainstChatUpstream(t *testing.T) {
	var mu sync.Mutex
	var secondRequestBody string
	firstTurn := true
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		isSecond := !firstTurn && strings.Contains(string(body), `"role":"tool"`)
		if isSecond {
			secondRequestBody = string(body)
		}
		firstTurn = false
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if isSecond {
			_, _ = w.Write([]byte(`{"id":"chatcmpl-2","object":"chat.completion","created":2,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"It is sunny"},"finish_reason":"stop"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"SH\"}"}}]},"finish_reason":"tool_calls"}]}`))
	}))
	defer upstream.Close()

	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "chat-only",
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"m"},
			Upstream: config.UpstreamConfig{
				BaseURL:        upstream.URL + "/v1",
				ProviderPreset: "openai",
				APIType:        "chat_completions",
			},
		}},
	}
	cfg.Debug.OutputDir = outputDir
	cfg.Router.Selection.ProtocolConversion = true

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	firstBody := `{"model":"m","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"What is the weather?"}]}],"tools":[{"name":"weather","description":"d","input_schema":{"type":"object"}}]}`
	req, _ := http.NewRequest(http.MethodPost, proxyServer.URL+"/v1/messages", bytes.NewBufferString(firstBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	firstResponse, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first turn: status = %d (body %s)", resp.StatusCode, firstResponse)
	}
	if !strings.Contains(string(firstResponse), `"stop_reason":"tool_use"`) || !strings.Contains(string(firstResponse), `"type":"tool_use"`) {
		t.Fatalf("first turn did not return a Messages tool_use: %s", firstResponse)
	}

	secondBody := `{"model":"m","max_tokens":100,"messages":[
		{"role":"user","content":[{"type":"text","text":"What is the weather?"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"weather","input":{"city":"SH"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"sunny"}]}]}`
	req, _ = http.NewRequest(http.MethodPost, proxyServer.URL+"/v1/messages", bytes.NewBufferString(secondBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err = proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	secondResponse, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second turn: status = %d (body %s)", resp.StatusCode, secondResponse)
	}
	mu.Lock()
	captured := secondRequestBody
	mu.Unlock()
	if !strings.Contains(captured, `"role":"tool"`) || !strings.Contains(captured, `"tool_call_id":"call_1"`) || !strings.Contains(captured, "sunny") {
		t.Fatalf("second turn did not reach the upstream as a tool result: %s", captured)
	}
}

// TestResponsesEntrypointNeverConverts pins the boundary: with only a
// Messages-only channel, /v1/responses is rejected by the local Responses
// runtime routing decision and never handed to the forwarding conversion path.
func TestResponsesEntrypointNeverConverts(t *testing.T) {
	var contacted bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacted = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"Hi"}],"stop_reason":"end_turn"}`))
	}))
	defer upstream.Close()

	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "messages-only",
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"m"},
			Upstream: config.UpstreamConfig{
				BaseURL:        upstream.URL,
				ProviderPreset: "anthropic",
				APIType:        "messages",
			},
		}},
	}
	cfg.Debug.OutputDir = outputDir
	cfg.Router.Selection.ProtocolConversion = true

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	body := `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hello"}]}]}`
	req, _ := http.NewRequest(http.MethodPost, proxyServer.URL+"/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", resp.StatusCode, respBody)
	}
	if contacted {
		t.Fatal("the Responses entrypoint must not be converted and sent to a Messages-only target")
	}
}
