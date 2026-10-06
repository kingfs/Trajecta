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

// proxyWithCapabilities builds a proxy in front of a stub upstream that records the path of
// every request it receives, with a single target whose declared capabilities are `caps`.
func proxyWithCapabilities(t *testing.T, caps config.UpstreamCapabilitiesConfig) (*httptest.Server, func() []string) {
	t.Helper()

	var (
		mu    sync.Mutex
		paths []string
	)
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstreamSrv.Close)

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "capability",
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"gpt-5"},
			Upstream: config.UpstreamConfig{
				BaseURL:        upstreamSrv.URL + "/v1",
				ApiKey:         "placeholder-key",
				ProviderPreset: "openai",
				APIType:        "chat_completions",
				Capabilities:   caps,
			},
		}},
	}
	cfg.Debug.OutputDir = t.TempDir()
	cfg.Debug.MaskKey = true
	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxySrv := httptest.NewServer(handler)
	t.Cleanup(proxySrv.Close)

	return proxySrv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// TestTokenizeRequestsHonourTheDeclaredTokenizeCapability pins an endpoint-level rule that
// the capability registry described but no routing decision consulted.
//
// `capabilities.tokenize` was declared in YAML, discovered by provider probe, stored with
// the channel and shown in the monitor, while SupportsEndpointForModel answered `true` for
// every endpoint other than Chat Completions and Responses. A target declared as not serving
// tokenization therefore stayed a routing candidate and received `/tokenize` requests it had
// said it cannot serve - the same shape of mistake as the raw Responses pass-through that
// could reach a Chat Completions-only upstream. The control below shows the mechanism does
// exist for Chat Completions; tokenize now behaves the same way, and an undeclared capability
// keeps the previous permissive behaviour.
func TestTokenizeRequestsHonourTheDeclaredTokenizeCapability(t *testing.T) {
	posted := func(t *testing.T, srv *httptest.Server, path, body string) (int, string) {
		t.Helper()
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(out)
	}

	for _, tc := range []struct {
		name  string
		caps  config.UpstreamCapabilitiesConfig
		check func(t *testing.T, srv *httptest.Server, seen func() []string)
	}{
		{
			name: "tokenize declared absent",
			caps: config.UpstreamCapabilitiesConfig{Tokenize: boolPtr(false)},
			check: func(t *testing.T, srv *httptest.Server, seen func() []string) {
				for _, path := range []string{"/tokenize", "/v1/detokenize"} {
					status, body := posted(t, srv, path, `{"model":"gpt-5","input":"hello"}`)
					if status != http.StatusBadGateway {
						t.Fatalf("%s: status = %d, want %d (body %s)", path, status, http.StatusBadGateway, body)
					}
					if !strings.Contains(body, "no_supporting_target") {
						t.Fatalf("%s: body = %s, want the routing failure to name the cause", path, body)
					}
				}
				if got := seen(); len(got) != 0 {
					t.Fatalf("the upstream received %v, want nothing: the target declared it does not serve tokenization", got)
				}
				// The declaration is specific: Chat Completions is unaffected.
				if status, body := posted(t, srv, "/v1/chat/completions", `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`); status != http.StatusOK {
					t.Fatalf("chat completions: status = %d, want 200 (body %s)", status, body)
				}
			},
		},
		{
			name: "tokenize declared present",
			caps: config.UpstreamCapabilitiesConfig{Tokenize: boolPtr(true)},
			check: func(t *testing.T, srv *httptest.Server, seen func() []string) {
				if status, body := posted(t, srv, "/tokenize", `{"model":"gpt-5","input":"hello"}`); status != http.StatusOK {
					t.Fatalf("status = %d, want 200 (body %s)", status, body)
				}
				if got := seen(); len(got) != 1 || got[0] != "/tokenize" {
					t.Fatalf("upstream paths = %v, want [/tokenize]", got)
				}
			},
		},
		{
			name: "tokenize undeclared keeps the previous behaviour",
			caps: config.UpstreamCapabilitiesConfig{},
			check: func(t *testing.T, srv *httptest.Server, seen func() []string) {
				if status, body := posted(t, srv, "/tokenize", `{"model":"gpt-5","input":"hello"}`); status != http.StatusOK {
					t.Fatalf("status = %d, want 200 (body %s)", status, body)
				}
				if got := seen(); len(got) != 1 {
					t.Fatalf("upstream paths = %v, want the request forwarded", got)
				}
			},
		},
		{
			name: "chat completions declared absent stays rejected",
			caps: config.UpstreamCapabilitiesConfig{ChatCompletions: boolPtr(false), Responses: boolPtr(false)},
			check: func(t *testing.T, srv *httptest.Server, seen func() []string) {
				if status, _ := posted(t, srv, "/v1/chat/completions", `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`); status != http.StatusBadGateway {
					t.Fatalf("status = %d, want %d", status, http.StatusBadGateway)
				}
				if got := seen(); len(got) != 0 {
					t.Fatalf("upstream paths = %v, want nothing", got)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, seen := proxyWithCapabilities(t, tc.caps)
			tc.check(t, srv, seen)
		})
	}
}
