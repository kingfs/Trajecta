package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// silentUpstream accepts the connection, drains the request and never answers, which is
// what a wedged upstream (or a deliberate slow-loris) looks like to the forwarding path.
func silentUpstream(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_, _ = io.Copy(io.Discard, io.LimitReader(c, 1<<20))
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); <-done })
	return "http://" + ln.Addr().String() + "/v1"
}

func handlerAgainstSilentUpstream(t *testing.T, headerTimeout time.Duration) (*Handler, string) {
	t.Helper()
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	baseURL := silentUpstream(t)
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "silent",
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"gpt-5"},
			Upstream: config.UpstreamConfig{
				BaseURL:        baseURL,
				ApiKey:         "placeholder-key",
				ProviderPreset: "openai",
			},
		}},
	}
	cfg.Debug.OutputDir = outputDir
	cfg.Debug.MaskKey = true
	cfg.Server.UpstreamResponseHeaderTimeout = headerTimeout
	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler, baseURL
}

func forwardingTransport(t *testing.T, handler *Handler) *http.Transport {
	t.Helper()
	transport, ok := handler.proxy.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("forwarding transport is %T, want *http.Transport", handler.proxy.Transport)
	}
	return transport
}

// TestUpstreamResponseHeaderTimeoutIsWiredIntoTheForwardingTransport pins the setting to
// the transport the forwarding path actually round-trips through, and pins the default:
// zero, because this proxy cannot tell a stuck upstream from a slow one.
func TestUpstreamResponseHeaderTimeoutIsWiredIntoTheForwardingTransport(t *testing.T) {
	boundedHandler, _ := handlerAgainstSilentUpstream(t, 300*time.Millisecond)
	bounded := forwardingTransport(t, boundedHandler)
	if got := bounded.ResponseHeaderTimeout; got != 300*time.Millisecond {
		t.Fatalf("ResponseHeaderTimeout = %v, want the configured 300ms", got)
	}
	unboundedHandler, _ := handlerAgainstSilentUpstream(t, 0)
	unbounded := forwardingTransport(t, unboundedHandler)
	if got := unbounded.ResponseHeaderTimeout; got != 0 {
		t.Fatalf("ResponseHeaderTimeout = %v, want 0 (no bound) by default", got)
	}
}

// TestUpstreamResponseHeaderTimeoutBoundsASilentUpstream covers what the operator setting
// buys: the round trip to an upstream that accepts the connection and then never answers
// is abandoned instead of pinning the client's request, its concurrency slot and its
// connection until the caller gives up.
//
// The bound is asserted here, on the transport, rather than through a whole proxied
// request: a forwarded request that fails is retried inside the handler's 30-second
// retry budget, so the handler-level symptom of a configured bound is a shorter failed
// attempt, not a faster error. The retry loop is the subject of the retry-round tests.
func TestUpstreamResponseHeaderTimeoutBoundsASilentUpstream(t *testing.T) {
	handler, baseURL := handlerAgainstSilentUpstream(t, 300*time.Millisecond)
	transport := forwardingTransport(t, handler)

	// The caller's own deadline is deliberately generous but finite: if the
	// configured bound stops applying, this test fails in seconds with a context
	// error instead of hanging on the silent upstream.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/chat/completions",
		strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := transport.RoundTrip(req)
	elapsed := time.Since(start)

	if err == nil {
		resp.Body.Close()
		t.Fatalf("a silent upstream answered after %v; the configured header timeout did not apply", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("the round trip took %v to give up; the configured 300ms bound did not apply", elapsed)
	}
	if !strings.Contains(err.Error(), "timeout awaiting response headers") {
		t.Fatalf("round trip failed with %v; want the response-header wait to be the cause", err)
	}
}

// TestUpstreamResponseHeaderTimeoutZeroLeavesTheWaitToTheCaller is the paired default:
// with no configured bound the round trip is still waiting when the caller's own deadline
// expires, which is what tells the two cases apart.
func TestUpstreamResponseHeaderTimeoutZeroLeavesTheWaitToTheCaller(t *testing.T) {
	handler, baseURL := handlerAgainstSilentUpstream(t, 0)
	transport := forwardingTransport(t, handler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/chat/completions",
		strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	type outcome struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan outcome, 1)
	start := time.Now()
	go func() {
		resp, err := transport.RoundTrip(req)
		if resp != nil {
			resp.Body.Close()
		}
		done <- outcome{err: err, elapsed: time.Since(start)}
	}()

	select {
	case got := <-done:
		t.Fatalf("the round trip ended after %v with %v while no bound was configured", got.elapsed, got.err)
	case <-time.After(500 * time.Millisecond):
	}

	cancel()
	select {
	case got := <-done:
		if got.err == nil {
			t.Fatal("the round trip returned no error after the caller cancelled it")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the round trip did not end after the caller cancelled it")
	}
}
