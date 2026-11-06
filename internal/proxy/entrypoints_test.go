package proxy

import (
	"bytes"
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

func TestCanonicalClientPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want string
	}{
		{path: "/v1/chat/completions", want: "/v1/chat/completions"},
		{path: "/v1/responses", want: "/v1/responses"},
		{path: "/responses", want: "/v1/responses"},
		{path: "/tokenize", want: "/tokenize"},
		{path: "/detokenize", want: "/detokenize"},
		{path: "/v1/tokenize", want: "/tokenize"},
		{path: "/v1/detokenize", want: "/detokenize"},
		{path: "/anthropic/messages", want: "/v1/messages"},
		{path: "/anthropic/v1/messages", want: "/v1/messages"},
		{path: "/anthropic/messages/count_tokens", want: "/v1/messages/count_tokens"},
		{path: "/anthropic/v1/messages/count_tokens", want: "/v1/messages/count_tokens"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := canonicalClientPath(tt.path); got != tt.want {
				t.Fatalf("canonicalClientPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// entrypointTestProxy wires one handler over the given static targets and serves
// it. Static discovery keeps the tests off the network and deterministic.
func entrypointTestProxy(t *testing.T, targets ...config.UpstreamTargetConfig) *httptest.Server {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{Upstreams: targets}
	cfg.Router.Selection.Policy = router.PolicyFirstAvailable
	cfg.Debug.OutputDir = t.TempDir()
	cfg.Debug.MaskKey = true

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxySrv := httptest.NewServer(handler)
	t.Cleanup(proxySrv.Close)
	return proxySrv
}

func entrypointTestTarget(id string, priority int, baseURL string, apiType string, models ...string) config.UpstreamTargetConfig {
	enabled := true
	return config.UpstreamTargetConfig{
		ID:             id,
		Enabled:        &enabled,
		Priority:       priority,
		ModelDiscovery: router.ModelDiscoveryStaticOnly,
		StaticModels:   models,
		Upstream:       config.UpstreamConfig{BaseURL: baseURL, APIType: apiType},
	}
}

// recordingUpstream serves a canned body per path and records the paths it was
// asked for. Any unexpected path answers 404 so a misrouted request fails the
// test's assertion instead of hanging.
func recordingUpstream(t *testing.T, responses map[string]string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		body, ok := responses[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

func postEntrypoint(t *testing.T, proxySrv *httptest.Server, path string, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, proxySrv.URL+path, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("http.NewRequest(%s) error = %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := proxySrv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s error = %v", path, err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(respBody)
}

// TestEntrypointChatCompletionsPassThrough pins the ordinary OpenAI-compatible
// entrypoint: the request is forwarded unchanged to a Chat Completions channel.
func TestEntrypointChatCompletionsPassThrough(t *testing.T) {
	upstream, paths := recordingUpstream(t, map[string]string{
		"/v1/chat/completions": `{"id":"chatcmpl_entrypoint","object":"chat.completion","model":"gpt-5","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
	})
	target := entrypointTestTarget("openai-chat", 100, upstream.URL+"/v1", "chat_completions", "gpt-5")
	target.Upstream.ProviderPreset = "openai"
	proxySrv := entrypointTestProxy(t, target)

	status, body := postEntrypoint(t, proxySrv, "/v1/chat/completions",
		`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if !strings.Contains(body, `"id":"chatcmpl_entrypoint"`) {
		t.Fatalf("body = %q, want the upstream Chat Completions payload", body)
	}
	if seen := paths(); len(seen) != 1 || seen[0] != "/v1/chat/completions" {
		t.Fatalf("upstream paths = %v, want exactly [/v1/chat/completions]", seen)
	}
}

// TestEntrypointAnthropicMessagesPassThrough pins the Anthropic entrypoint: the
// proxy forwards /v1/messages to an Anthropic channel with its own auth header
// and protocol family.
func TestEntrypointAnthropicMessagesPassThrough(t *testing.T) {
	var mu sync.Mutex
	var gotAPIKey string
	var gotVersion string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_entrypoint","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)

	target := entrypointTestTarget("anthropic", 100, upstream.URL, "messages", "claude-sonnet-4-5")
	target.Upstream.ProviderPreset = "anthropic"
	target.Upstream.ApiKey = "anth-secret"
	proxySrv := entrypointTestProxy(t, target)

	status, body := postEntrypoint(t, proxySrv, "/v1/messages",
		`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if !strings.Contains(body, `"id":"msg_entrypoint"`) {
		t.Fatalf("body = %q, want the upstream Messages payload", body)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotAPIKey != "anth-secret" {
		t.Fatalf("x-api-key = %q, want anth-secret (Anthropic auth must reach the channel)", gotAPIKey)
	}
	if gotVersion == "" {
		t.Fatal("anthropic-version header is missing, so the channel was not treated as Anthropic")
	}
}

// TestEntrypointResponsesOnChatChannelUsesLocalRuntime pins the translation half
// of /v1/responses: a channel that only serves Chat Completions answers the
// Responses entrypoint through the local runtime, so the upstream sees
// /v1/chat/completions and never a raw /v1/responses.
func TestEntrypointResponsesOnChatChannelUsesLocalRuntime(t *testing.T) {
	upstream, paths := recordingUpstream(t, map[string]string{
		"/v1/chat/completions": `{"id":"chatcmpl_local","object":"chat.completion","model":"gpt-5","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
	})
	responsesEnabled, chatEnabled := true, true
	target := entrypointTestTarget("chat-only", 100, upstream.URL+"/v1", "chat_completions", "gpt-5")
	target.Upstream.ProviderPreset = "openai"
	target.Upstream.Capabilities = config.UpstreamCapabilitiesConfig{
		Responses:       &responsesEnabled,
		ChatCompletions: &chatEnabled,
	}
	proxySrv := entrypointTestProxy(t, target)

	status, body := postEntrypoint(t, proxySrv, "/v1/responses", `{"model":"gpt-5","input":"hi"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the local Responses runtime; body=%s", status, body)
	}
	if !strings.Contains(body, `"object":"response"`) {
		t.Fatalf("body = %q, want a Responses payload from the local runtime", body)
	}
	seen := paths()
	var chatCalls, responsesCalls int
	for _, p := range seen {
		switch p {
		case "/v1/chat/completions":
			chatCalls++
		case "/v1/responses":
			responsesCalls++
		}
	}
	if responsesCalls != 0 {
		t.Fatalf("upstream paths = %v, want no raw /v1/responses for a chat-only channel", seen)
	}
	if chatCalls != 1 {
		t.Fatalf("upstream paths = %v, want exactly one translated /v1/chat/completions call", seen)
	}
}

// TestEntrypointResponsesOnNativeChannelPassesThrough pins the pass-through half
// of /v1/responses: a channel that declares the native Responses API receives
// the raw request unchanged.
func TestEntrypointResponsesOnNativeChannelPassesThrough(t *testing.T) {
	upstream, paths := recordingUpstream(t, map[string]string{
		"/v1/responses": `{"id":"resp_native","object":"response","status":"completed","model":"gpt-5","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
	})
	target := entrypointTestTarget("native-responses", 100, upstream.URL+"/v1", "responses_native", "gpt-5")
	target.Upstream.ProviderPreset = "openai"
	target.Upstream.Capabilities = config.UpstreamCapabilitiesConfig{
		Responses:       boolPtr(true),
		ChatCompletions: boolPtr(false),
	}
	proxySrv := entrypointTestProxy(t, target)

	status, body := postEntrypoint(t, proxySrv, "/v1/responses", `{"model":"gpt-5","input":"hi"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if !strings.Contains(body, `"id":"resp_native"`) {
		t.Fatalf("body = %q, want the upstream Responses payload", body)
	}
	if seen := paths(); len(seen) != 1 || seen[0] != "/v1/responses" {
		t.Fatalf("upstream paths = %v, want exactly one raw /v1/responses", seen)
	}
}
