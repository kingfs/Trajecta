package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// TestNativePassThroughDoesNotFallBackToAChatOnlyUpstream pins what a failing native
// Responses target may fall back to.
//
// The proxy decides native-versus-local per request: with a native Responses target
// available it forwards /v1/responses unchanged. When that target then fails with a status
// the retry policy considers retryable, the next attempt re-selects among the remaining
// targets - and a Chat Completions target is a legitimate candidate there for "can this
// model be served on /v1/responses", because the local runtime can translate for it. A
// pass-through does not translate, so the retry would deliver a Responses request to an
// upstream that only implements Chat Completions; the live suite saw exactly that, as a
// chat upstream receiving POST /v1/responses.
//
// The request is marked as a native pass-through, so the retry must find no eligible
// target and fail the client request instead. Measured: without the mark the chat backend
// receives one request for /v1/responses and this test fails on chatCalls = 1.
func TestNativePassThroughDoesNotFallBackToAChatOnlyUpstream(t *testing.T) {
	var chatCalls atomic.Int32
	var mu sync.Mutex
	var wrongSurface []string

	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatCalls.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			mu.Lock()
			wrongSurface = append(wrongSurface, r.Method+" "+r.URL.Path)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_1","model":"gpt-5","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer chatSrv.Close()

	var nativeCalls atomic.Int32
	nativeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nativeCalls.Add(1)
		// 500 is a retryable status, so the proxy moves on to the next candidate.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"native responses upstream is down","type":"api_error"}}`)
	}))
	defer nativeSrv.Close()

	responsesEnabled, chatDisabled, chatEnabled := true, false, true
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()
	if err := st.SaveAppSettingJSON(context.Background(), "routing.settings", map[string]string{"responses_strategy": "auto"}); err != nil {
		t.Fatalf("SaveAppSettingJSON() error = %v", err)
	}
	cfg := &config.Config{}
	cfg.Upstreams = append(cfg.Upstreams,
		config.UpstreamTargetConfig{
			ID: "native-responses", Enabled: boolPtr(true), Priority: 200,
			ModelDiscovery: router.ModelDiscoveryStaticOnly, StaticModels: []string{"gpt-5"},
			Upstream: config.UpstreamConfig{
				BaseURL: nativeSrv.URL + "/v1", ProviderPreset: "openai", APIType: "responses_native", Mode: "proxy",
				Capabilities: config.UpstreamCapabilitiesConfig{Responses: &responsesEnabled, ChatCompletions: &chatDisabled},
			},
		},
		config.UpstreamTargetConfig{
			ID: "chat-backend", Enabled: boolPtr(true), Priority: 100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly, StaticModels: []string{"gpt-5"},
			Upstream: config.UpstreamConfig{
				BaseURL: chatSrv.URL + "/v1", ProviderPreset: "openai", APIType: "chat_completions", Mode: "responses_server",
				Capabilities: config.UpstreamCapabilitiesConfig{ChatCompletions: &chatEnabled},
			},
		})
	cfg.Debug.OutputDir = t.TempDir()
	cfg.Debug.MaskKey = true

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxySrv := httptest.NewServer(handler)
	defer proxySrv.Close()

	// The retry budget keeps re-selecting for up to 30s, so a bounded caller context is
	// the ordinary ending here; the invariant under test is which upstream was contacted,
	// not how the wait ends.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxySrv.URL+"/v1/responses",
		strings.NewReader(`{"model":"gpt-5","input":"ping"}`))
	if err != nil {
		t.Fatalf("http.NewRequestWithContext() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := proxySrv.Client().Do(req)
	var body []byte
	if err == nil {
		body, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("status = %d, want %d once the native target failed; body=%s", resp.StatusCode, http.StatusBadGateway, body)
		}
	}
	// Let any forward that was already in flight land before judging it.
	time.Sleep(250 * time.Millisecond)

	if nativeCalls.Load() == 0 {
		t.Fatal("the native Responses target was never attempted, so the scenario never ran")
	}
	if got := chatCalls.Load(); got != 0 {
		mu.Lock()
		paths := append([]string(nil), wrongSurface...)
		mu.Unlock()
		t.Fatalf("the chat-only upstream received %d request(s) for a Responses pass-through (wrong surfaces: %v); body=%s",
			got, paths, body)
	}
}
