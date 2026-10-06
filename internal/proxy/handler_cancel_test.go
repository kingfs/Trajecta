package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// TestHandlerCancelsUpstreamWhenClientGivesUp pins that the forwarded request
// inherits the caller's context.
//
// The outbound request used to be built with context.Background(), which
// detached it from the client entirely. A cancelled SDK call, a closed browser
// tab or a proxy-level server timeout then left the upstream request running,
// holding a handler goroutine, an upstream connection and the upstream's own
// token spend until the upstream happened to finish on its own.
func TestHandlerCancelsUpstreamWhenClientGivesUp(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	upstreamStarted := make(chan struct{})
	upstreamCancelled := make(chan struct{})
	var seenPath string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the request body to EOF first. net/http only starts the
		// background read that detects a dropped peer once the request body has
		// been fully read (see registerOnHitEOF in net/http/server.go), so
		// without this the upstream would never notice the cancelled connection
		// and the test would pass or fail for the wrong reason.
		_, _ = io.ReadAll(r.Body)
		// Announce that the upstream really is executing, then block the way a
		// stalled provider would.
		seenPath = r.URL.Path
		close(upstreamStarted)
		select {
		case <-r.Context().Done():
			close(upstreamCancelled)
		case <-time.After(20 * time.Second):
		}
	}))
	defer upstream.Close()

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "slow",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: router.ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5.5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        upstream.URL + "/v1",
					ProviderPreset: "openai",
					Capabilities:   config.UpstreamCapabilitiesConfig{Responses: boolPtr(true)},
				},
			},
		},
	}
	cfg.Router.Selection.Policy = router.PolicyFirstAvailable
	cfg.Debug.OutputDir = outputDir
	cfg.Debug.MaskKey = true

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyServer.URL+"/v1/responses",
		bytes.NewBufferString(`{"model":"gpt-5.5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequestWithContext() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		resp, err := proxyServer.Client().Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()

	select {
	case <-upstreamStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the upstream never received the forwarded request")
	}

	// The client gives up.
	cancel()

	select {
	case <-upstreamCancelled:
	case <-time.After(10 * time.Second):
		t.Fatalf("the upstream request was still running after the client cancelled (upstream path was %q): the forwarded request does not inherit the caller's context", seenPath)
	}

	select {
	case <-requestDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the proxy handler did not return after the client cancelled")
	}
}
