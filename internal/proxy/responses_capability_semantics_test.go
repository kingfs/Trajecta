package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/store"
)

// TestChatChannelResponsesCapabilityIsNotNative pins the incident that motivated the
// native-Responses contract change, end to end through the proxy surface.
//
// Channel `nvidia-nim` had api_type=chat_completions and capabilities.responses=true, and
// the requested model had no per-model supports_responses override. The old semantics
// treated the channel-level flag as proof of the native API, so a raw POST /v1/responses
// was forwarded unchanged and hit NVIDIA's /v1/responses, which 404s for models whose
// route is chat-only. The flag is not proof: a Chat Completions channel can answer
// /v1/responses through the local Responses runtime, which is exactly what must happen
// here, while a model the operator explicitly opts in through the tri-state override keeps
// the native pass-through.
func TestChatChannelResponsesCapabilityIsNotNative(t *testing.T) {
	const model = "deepseek-ai/deepseek-v4.1-flash"

	var mu sync.Mutex
	var paths []string
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chatcmpl_nim","object":"chat.completion","model":"`+model+`","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		case "/v1/responses":
			// A model that really does serve the native API; the test asserts below
			// that this path is only reached for the explicit per-model opt-in.
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"resp_native","object":"response","status":"completed","model":"`+model+`","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstreamSrv.Close)

	pathsSeen := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
	resetPaths := func() {
		mu.Lock()
		paths = nil
		mu.Unlock()
	}

	newProxy := func(t *testing.T, overrides map[string]config.UpstreamCapabilitiesConfig) *httptest.Server {
		t.Helper()
		st, err := store.New(t.TempDir())
		if err != nil {
			t.Fatalf("store.New() error = %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })

		responsesEnabled, chatEnabled := true, true
		cfg := &config.Config{}
		cfg.Upstream = config.UpstreamConfig{
			BaseURL:        upstreamSrv.URL + "/v1",
			ProviderPreset: "nvidia_nim",
			APIType:        "chat_completions",
			Capabilities: config.UpstreamCapabilitiesConfig{
				Responses:       &responsesEnabled,
				ChatCompletions: &chatEnabled,
			},
			ModelCapabilities: overrides,
		}
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

	postResponses := func(t *testing.T, proxySrv *httptest.Server) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, proxySrv.URL+"/v1/responses",
			strings.NewReader(`{"model":"`+model+`","input":"hello"}`))
		if err != nil {
			t.Fatalf("http.NewRequest() error = %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := proxySrv.Client().Do(req)
		if err != nil {
			t.Fatalf("client.Do() error = %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(body)
	}

	// No per-model override: the channel-level responses flag must not make the proxy
	// forward the raw request. The local runtime translates to Chat Completions instead.
	t.Run("capability alone uses the local runtime", func(t *testing.T) {
		resetPaths()
		proxySrv := newProxy(t, nil)
		status, body := postResponses(t, proxySrv)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200 from the local Responses runtime; body=%s", status, body)
		}
		seen := pathsSeen()
		var responsesCalls, chatCalls int
		for _, p := range seen {
			switch p {
			case "/v1/responses":
				responsesCalls++
			case "/v1/chat/completions":
				chatCalls++
			}
		}
		if responsesCalls != 0 {
			t.Fatalf("upstream received %d raw POST /v1/responses for a chat-only model (paths = %v): the channel-level responses capability was treated as native", responsesCalls, seen)
		}
		if chatCalls != 1 {
			t.Fatalf("upstream paths = %v, want the local runtime to call /v1/chat/completions once", seen)
		}
	})

	// The tri-state per-model override is the operator declaring the native API. It must
	// keep the pass-through for that model.
	t.Run("model override opts into native pass-through", func(t *testing.T) {
		resetPaths()
		modelResponsesEnabled := true
		proxySrv := newProxy(t, map[string]config.UpstreamCapabilitiesConfig{
			model: {Responses: &modelResponsesEnabled},
		})
		status, body := postResponses(t, proxySrv)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", status, body)
		}
		seen := pathsSeen()
		var responsesCalls, chatCalls int
		for _, p := range seen {
			switch p {
			case "/v1/responses":
				responsesCalls++
			case "/v1/chat/completions":
				chatCalls++
			}
		}
		if responsesCalls != 1 || chatCalls != 0 {
			t.Fatalf("upstream paths = %v, want exactly one raw POST /v1/responses and no Chat Completions call", seen)
		}
	})
}
