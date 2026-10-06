package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// newBodyLimitHandler builds a handler whose configured request-body limit is small
// enough to exercise, together with a stub upstream that records the size of every
// body it receives.
func newBodyLimitHandler(t *testing.T, limit int64) (*Handler, *[]int, string) {
	t.Helper()

	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var (
		mu       sync.Mutex
		received []int
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAllLimited(r)
		mu.Lock()
		received = append(received, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp-1","output_text":"ok"}`))
	}))
	t.Cleanup(upstream.Close)

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: router.ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        upstream.URL + "/v1",
					ApiKey:         "placeholder-key",
					ProviderPreset: "openai",
				},
			},
		},
	}
	cfg.Debug.OutputDir = outputDir
	cfg.Debug.MaskKey = true
	cfg.ResponsesServer.MaxRequestBodyBytes = limit

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler, &received, outputDir
}

func readAllLimited(r *http.Request) (int, error) {
	var total int
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Body.Read(buf)
		total += n
		if err != nil {
			return total, nil
		}
	}
}

// TestHandlerBoundsTheRequestBodyItBuffers pins the finite guard against an unbounded
// request body on the forwarding path.
//
// The local Responses runtime already bounded its own reads through
// responses/httpapi.WithMaxBodyBytes, and config.ResponsesMaxRequestBodyBytes documents
// its default as a "finite guard against unbounded bodies" chosen high enough for
// base64 image inputs. The body the proxy buffers before routing was read with a plain
// io.ReadAll, so the pass-through path - the main path of a proxy - had no bound at
// all and a client could make it allocate as much memory as it cared to send. The
// guard now applies before anything reads the body, so the buffered read, the
// pass-through transport and the locally answered endpoints are all finite, and a body
// that declares a size over the limit is rejected without being read.
func TestHandlerBoundsTheRequestBodyItBuffers(t *testing.T) {
	const limit = 4096
	handler, received, _ := newBodyLimitHandler(t, limit)

	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://proxy.local"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	oversized := `{"model":"gpt-5","input":"` + strings.Repeat("x", limit*2) + `"}`

	// A forwarded request over the limit is refused, and the upstream never sees it.
	if rec := post("/v1/chat/completions", oversized); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized forwarded request: status = %d, want %d (body %q)", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	if len(*received) != 0 {
		t.Fatalf("the upstream was reached with an oversized body: %v", *received)
	}

	// A locally answered endpoint reads the body itself; it is bounded by the same
	// guard rather than by its own check.
	ollama := `{"name":"` + strings.Repeat("x", limit*2) + `"}`
	if rec := post("/api/show", ollama); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized local request: status = %d, want %d (body %q)", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}

	// A body within the limit still reaches the upstream, complete.
	within := `{"model":"gpt-5","input":"` + strings.Repeat("x", limit/2) + `"}`
	if rec := post("/v1/chat/completions", within); rec.Code != http.StatusOK {
		t.Fatalf("in-limit forwarded request: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if len(*received) != 1 {
		t.Fatalf("the upstream received %d bodies, want 1", len(*received))
	}
	if (*received)[0] < len(within) {
		t.Fatalf("the forwarded body was truncated: got %d bytes, want at least %d", (*received)[0], len(within))
	}
}
