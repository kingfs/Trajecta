package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// proxyForUpstream builds a proxy handler in front of the given upstream handler and serves
// it on a real HTTP server, so the server's own output buffering is in the loop: a test
// that called the handler directly would not show whether a stream is flushed.
func proxyForUpstream(t *testing.T, upstream http.Handler) *httptest.Server {
	t.Helper()
	upstreamSrv := httptest.NewServer(upstream)
	t.Cleanup(upstreamSrv.Close)

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "stream",
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"gpt-5"},
			Upstream: config.UpstreamConfig{
				BaseURL:        upstreamSrv.URL + "/v1",
				ApiKey:         "placeholder-key",
				ProviderPreset: "openai",
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
	return proxySrv
}

func postStreamingChat(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Post(url+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

// TestForwardedStreamReachesTheClientAsItIsProduced pins the contract a streamed response
// depends on and that the forwarding path owns: it writes the upstream body itself rather
// than handing the response to httputil.ReverseProxy, so nothing else flushes for it.
//
// The server buffers a write that is smaller than its output buffer instead of sending it,
// so without a flush per write the client receives nothing at all - not even the response
// headers - until the handler returns. For a streamed chat completion that is the whole
// feature: `stream: true` delivered the complete answer at once, after the last token, and
// its time to first byte was the total duration. Measured on this fixture before the fix:
// headers and first byte both arrived at 1.24s, when the upstream had already finished.
func TestForwardedStreamReachesTheClientAsItIsProduced(t *testing.T) {
	const firstEvent = "data: {\"delta\":\"first\"}\n\n"
	proxySrv := proxyForUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, firstEvent)
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(1200 * time.Millisecond)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))

	start := time.Now()
	resp := postStreamingChat(t, proxySrv.URL)
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want the upstream's text/event-stream", got)
	}

	// Read until the first event arrives and time it. The upstream holds its second event
	// back for 1.2s, so a stream that is not flushed cannot beat that.
	var (
		received  strings.Builder
		firstSeen time.Duration = -1
		buf                     = make([]byte, 4096)
	)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			received.Write(buf[:n])
			if firstSeen < 0 && strings.Contains(received.String(), firstEvent) {
				firstSeen = time.Since(start)
			}
		}
		if err != nil {
			break
		}
	}

	if firstSeen < 0 {
		t.Fatalf("the first event never arrived; body = %q", received.String())
	}
	if firstSeen > 600*time.Millisecond {
		t.Fatalf("the first event took %v to reach the client while the upstream sent it immediately; the response is not flushed per write", firstSeen)
	}
	if body := received.String(); !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("the stream lost its final event; body = %q", body)
	}
}

// TestForwardedStreamPreservesTheUpstreamFraming is the companion guarantee: the bytes the
// client receives are the bytes the upstream sent. A stream carries the framing the client
// parses - comments, multi-line data fields, event names - so the recording tee and the
// usage sniffer on this path must pass it through untouched.
func TestForwardedStreamPreservesTheUpstreamFraming(t *testing.T) {
	const stream = ": keep-alive\n" +
		"event: message\n" +
		"data: {\"delta\":\"line one\"}\n" +
		"data: {\"delta\":\"line two\"}\n" +
		"\n" +
		"id: 42\n" +
		"data: {\"delta\":\"with id\"}\n" +
		"\n" +
		"data: [DONE]\n" +
		"\n"

	proxySrv := proxyForUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		// Write in pieces that do not align with the event boundaries: whatever
		// arrives, the client must see the same bytes in the same order.
		for _, chunk := range []string{stream[:11], stream[11:40], stream[40:]} {
			_, _ = io.WriteString(w, chunk)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))

	resp := postStreamingChat(t, proxySrv.URL)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != stream {
		t.Fatalf("the forwarded stream was altered:\n got %q\nwant %q", string(body), stream)
	}
}
