package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kingfs/Trajecta/internal/netproxy"
)

// TestChannelProxyRoutesOnlyThatChannelsTraffic pins the channel-level
// override: a target whose proxy is enabled sends its request to that proxy,
// even though the process-wide system policy is off. The target host is not
// loopback (a loopback upstream is always direct), so the proxy is actually
// exercised; the proxy answers instead of forwarding.
func TestChannelProxyRoutesOnlyThatChannelsTraffic(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var seen []string
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_channel_proxy","object":"chat.completion","model":"gpt-5","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(proxyServer.Close)

	target := entrypointTestTarget("proxied", 100, "http://upstream.example/v1", "chat_completions", "gpt-5")
	target.Upstream.ProviderPreset = "openai"
	target.Upstream.Proxy = netproxy.Settings{Enabled: true, HTTPProxy: proxyServer.URL}
	proxySrv := entrypointTestProxy(t, target)

	status, body := postEntrypoint(t, proxySrv, "/v1/chat/completions",
		`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if !strings.Contains(body, `"id":"chatcmpl_channel_proxy"`) {
		t.Fatalf("body = %q, want the proxied Chat Completions payload", body)
	}
	mu.Lock()
	paths := append([]string(nil), seen...)
	mu.Unlock()
	if len(paths) != 1 || paths[0] != "/v1/chat/completions" {
		t.Fatalf("proxy paths = %v, want exactly [/v1/chat/completions]", paths)
	}
}
