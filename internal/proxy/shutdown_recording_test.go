package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// slowUpstream answers chat completions only once the test releases it, so a request can
// be held in flight while a shutdown is started.
func slowUpstream(t *testing.T) (*httptest.Server, <-chan struct{}, chan<- struct{}) {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	var closed bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !closed {
			close(entered)
			closed = true
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"stub-1","object":"chat.completion","model":"gpt-5","choices":[{"index":0,"message":{"role":"assistant","content":"drained-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	t.Cleanup(upstream.Close)
	return upstream, entered, release
}

func recordingHandlerFor(t *testing.T, upstreamURL string) (*Handler, string) {
	t.Helper()
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "primary",
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"gpt-5"},
			Upstream: config.UpstreamConfig{
				BaseURL:        upstreamURL + "/v1",
				ApiKey:         "placeholder-key",
				ProviderPreset: "openai",
			},
		}},
	}
	cfg.Debug.OutputDir = outputDir
	cfg.Debug.MaskKey = true
	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler, outputDir
}

// TestShutdownDrainsInFlightRequestAndFinalisesRecording pins the pair of guarantees a
// shutdown signal has to give the recording path: the request that is in flight when the
// signal arrives is served to completion, and the cassette it is being recorded into is
// finalised with its prelude.
//
// A cassette is written record-first and the prelude is prepended only at finalisation,
// so a process that dies on the first SIGTERM leaves the recording without a prelude -
// readable neither as V3 nor as a legacy header block, and skipped by the index. The
// exchange is then lost together with the upstream cost already paid for it, which is
// the artifact a record/replay proxy exists to produce.
func TestShutdownDrainsInFlightRequestAndFinalisesRecording(t *testing.T) {
	upstream, entered, release := slowUpstream(t)
	handler, outputDir := recordingHandlerFor(t, upstream.URL)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	type result struct {
		status int
		body   string
		err    error
	}
	served := make(chan result, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Post(
			"http://"+ln.Addr().String()+"/v1/chat/completions",
			"application/json",
			strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			served <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		served <- result{status: resp.StatusCode, body: string(body)}
	}()

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the request never reached the upstream")
	}

	// Drain while the request is in flight, then let it finish.
	shutdownDone := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		close(shutdownDone)
	}()
	time.Sleep(150 * time.Millisecond)
	close(release)

	select {
	case got := <-served:
		if got.err != nil {
			t.Fatalf("the in-flight request failed during shutdown: %v", got.err)
		}
		if got.status != http.StatusOK {
			t.Fatalf("in-flight request got status %d, want 200 (body %q)", got.status, got.body)
		}
		if !strings.Contains(got.body, "drained-ok") {
			t.Fatalf("in-flight request body %q does not carry the upstream answer", got.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the in-flight request never completed")
	}
	select {
	case <-shutdownDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not return after the in-flight request finished")
	}

	recording := waitForCassette(t, outputDir)
	head, err := os.ReadFile(recording)
	if err != nil {
		t.Fatalf("read cassette: %v", err)
	}
	if !strings.HasPrefix(string(head), "# trajecta/v3") {
		t.Fatalf("the drained request left an unfinalised cassette (%s): %q", recording, firstLine(string(head)))
	}
	if !strings.Contains(string(head), "drained-ok") {
		t.Fatalf("the cassette does not hold the response of the drained request")
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// waitForCassette waits for the recording of the drained request to appear.
func waitForCassette(t *testing.T, dir string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var found string
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".http") && found == "" {
				found = path
			}
			return nil
		})
		if found != "" {
			return found
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no cassette was written under %s", dir)
	return ""
}
