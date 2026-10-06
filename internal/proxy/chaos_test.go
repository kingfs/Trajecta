package proxy

import (
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

// chaosProxy builds a proxy whose only upstream is a stub, with the given fault-injection
// rules, and reports how many requests reached the upstream.
func chaosProxy(t *testing.T, rules []config.ChaosRule) (*httptest.Server, func() int) {
	t.Helper()

	var (
		mu   sync.Mutex
		hits int
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
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
			ID:             "chaos-target",
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"gpt-5"},
			Upstream: config.UpstreamConfig{
				BaseURL:        upstream.URL + "/v1",
				ApiKey:         "placeholder-key",
				ProviderPreset: "openai",
				APIType:        "chat_completions",
			},
		}},
	}
	cfg.Debug.OutputDir = t.TempDir()
	cfg.Debug.MaskKey = true
	cfg.Chaos.Enabled = true
	cfg.Chaos.Rules = rules
	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return hits
	}
}

func postChaosChat(t *testing.T, srv *httptest.Server) (*http.Response, string) {
	t.Helper()
	body := `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post /v1/chat/completions error = %v", err)
	}
	payload, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read response body error = %v", err)
	}
	return resp, string(payload)
}

// TestChaosActionIsCaseInsensitive pins the end-to-end effect of normalizing the configured
// action.
//
// The proxy compares the action with an exact string, so `action: ERROR` used to mark the rule
// as hit and then fall through both branches: the request was forwarded to the upstream and the
// operator's fault injection silently did not happen (a probe with `action: "erorr"` returned
// 200 and the stub upstream received it).
func TestChaosActionIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	srv, hits := chaosProxy(t, []config.ChaosRule{{Model: "*", Rate: 1, Action: "ERROR", StatusCode: 503, Message: "injected"}})
	resp, payload := postChaosChat(t, srv)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", resp.StatusCode, payload)
	}
	if got := hits(); got != 0 {
		t.Fatalf("upstream hits = %d, want the injected error to answer instead of forwarding", got)
	}
	if resp.Header.Get("X-Trajecta-Chaos") != "injected" {
		t.Fatalf("X-Trajecta-Chaos = %q, want injected", resp.Header.Get("X-Trajecta-Chaos"))
	}
	if !strings.Contains(payload, "chaos_injected") {
		t.Fatalf("body = %s, want the chaos_injected error code", payload)
	}
}

// TestChaosStatusCodeOutOfRangeCannotPanicTheHandler pins the robustness fallback for a status
// code net/http refuses to write.
//
// `status_code: 1000` reached WriteHeader through the ordinary upstream response path, and
// net/http panicked while serving the request ("invalid WriteHeader code 1000"); the client saw
// a torn connection (EOF) instead of an error response. Load rejects such a rule now, and this
// case builds the Config in code - the way an embedded user or a test would - so the fallback
// is what keeps the handler alive.
func TestChaosStatusCodeOutOfRangeCannotPanicTheHandler(t *testing.T) {
	t.Parallel()

	for _, code := range []int{1000, 99, -1} {
		srv, hits := chaosProxy(t, []config.ChaosRule{{Model: "*", Rate: 1, Action: "error", StatusCode: code, Message: "injected"}})
		resp, payload := postChaosChat(t, srv)
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status_code %d: status = %d, want the 500 fallback; body=%s", code, resp.StatusCode, payload)
		}
		if got := hits(); got != 0 {
			t.Fatalf("status_code %d: upstream hits = %d, want the injected error to answer", code, got)
		}
		if !strings.Contains(payload, "chaos_injected") {
			t.Fatalf("status_code %d: body = %s, want the chaos_injected error code", code, payload)
		}
	}
}
