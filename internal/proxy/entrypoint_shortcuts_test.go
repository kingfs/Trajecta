package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/limit"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// The entrypoints before target selection - the Ollama show call, the OpenAI
// model-detail call, the aggregated model list and the pre-selection rate limit -
// answer or reject without contacting an upstream. These tests cover them with an
// upstream that fails the test when it is reached, so "answered locally" is proven
// rather than inferred from the status code.

// newEntrypointShortcutHandler builds a handler with one static upstream whose
// server records every call, so a test can assert that none happened.
func newEntrypointShortcutHandler(t *testing.T) (*Handler, *store.Store, string, *[]string) {
	t.Helper()

	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var called []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = append(called, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"error":"the upstream must not be reached"}`))
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

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler, st, outputDir, &called
}

func TestHandlerAnswersOllamaShowWithoutForwarding(t *testing.T) {
	handler, _, _, called := newEntrypointShortcutHandler(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/show", strings.NewReader(`{"name":"gpt-5"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var entry struct {
		ID     string `json:"id"`
		Object string `json:"object"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal body %q: %v", rec.Body.String(), err)
	}
	if entry.ID != "gpt-5" {
		t.Fatalf("id = %q, want gpt-5", entry.ID)
	}
	if entry.Object != "model" {
		t.Fatalf("object = %q, want model", entry.Object)
	}
	if len(*called) != 0 {
		t.Fatalf("upstream was called with %v, want a locally answered entrypoint", *called)
	}
}

func TestHandlerOllamaShowRejectsMalformedRequests(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        string
		wantMessage string
	}{
		{name: "not json", body: `{`, wantMessage: "invalid JSON body"},
		{name: "blank name", body: `{"name":"   "}`, wantMessage: "missing model name"},
		{name: "absent name", body: `{}`, wantMessage: "missing model name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, _, _, called := newEntrypointShortcutHandler(t)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/show", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantMessage) {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.wantMessage)
			}
			if len(*called) != 0 {
				t.Fatalf("upstream was called with %v, want a locally rejected request", *called)
			}
		})
	}
}

func TestHandlerAnswersOpenAIModelDetailWithoutForwarding(t *testing.T) {
	handler, _, _, called := newEntrypointShortcutHandler(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models/gpt-5", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var entry struct {
		ID     string `json:"id"`
		Object string `json:"object"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &entry); err != nil {
		t.Fatalf("unmarshal body %q: %v", rec.Body.String(), err)
	}
	if entry.ID != "gpt-5" {
		t.Fatalf("id = %q, want gpt-5", entry.ID)
	}
	if len(*called) != 0 {
		t.Fatalf("upstream was called with %v, want a locally answered entrypoint", *called)
	}
}

// TestHandlerPreSelectionRateLimitRejectsBeforeForwarding covers the pre-selection
// limiter: a request that cannot take a lease is rejected with 429 and recorded as
// a trace, and the router is never asked to select a target.
func TestHandlerPreSelectionRateLimitRejectsBeforeForwarding(t *testing.T) {
	handler, st, outputDir, called := newEntrypointShortcutHandler(t)
	handler.limiter = limit.New(limit.Config{MaxConcurrent: 1})

	// Hold the only lease so the request below cannot take one.
	lease, reason := handler.limiter.Acquire(context.Background(), "global")
	if reason != limit.RejectNone || lease == nil {
		t.Fatalf("setup Acquire() = (%v, %v), want a lease", lease, reason)
	}
	defer lease.Release()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "rate_limited") {
		t.Fatalf("body = %q, want the rate_limited error code", rec.Body.String())
	}
	if len(*called) != 0 {
		t.Fatalf("upstream was called with %v, want a rejected request", *called)
	}

	recordPath := findRecordedHTTP(t, outputDir)
	parsed, err := waitForRecordedPrelude(recordPath, time.Second)
	if err != nil {
		t.Fatalf("waitForRecordedPrelude(%q) error = %v", recordPath, err)
	}
	if parsed.Header.Meta.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("recorded StatusCode = %d, want 429", parsed.Header.Meta.StatusCode)
	}
	if parsed.Header.Meta.Error != string(limit.RejectConcurrencyExceeded) {
		t.Fatalf("recorded Error = %q, want %q", parsed.Header.Meta.Error, limit.RejectConcurrencyExceeded)
	}
	cassette, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read %q: %v", recordPath, err)
	}
	if !strings.Contains(string(cassette), "limit.concurrency_rejected") {
		t.Fatalf("cassette does not record the limit rejection event:\n%s", cassette)
	}
	if entries, err := waitForRecentEntries(st, 1, time.Second); err != nil {
		t.Fatalf("waitForRecentEntries() error = %v", err)
	} else if entries[0].Header.Meta.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("indexed StatusCode = %d, want 429", entries[0].Header.Meta.StatusCode)
	}
}

// TestHandlerWithoutUpstreamTargetsAnswersWithASelectionFailure covers the guard
// that runs before the request body is read: with no upstream target configured
// nothing downstream can succeed, so the proxy answers with the selection-failure
// envelope and records it instead of attempting a selection.
func TestHandlerWithoutUpstreamTargetsAnswersWithASelectionFailure(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{}
	cfg.Debug.OutputDir = outputDir
	cfg.Debug.MaskKey = true
	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), router.SelectionFailureNoSupportingTarget) {
		t.Fatalf("body = %q, want the %q reason", rec.Body.String(), router.SelectionFailureNoSupportingTarget)
	}

	recordPath := findRecordedHTTP(t, outputDir)
	parsed, err := waitForRecordedPrelude(recordPath, time.Second)
	if err != nil {
		t.Fatalf("waitForRecordedPrelude(%q) error = %v", recordPath, err)
	}
	if parsed.Header.Meta.RoutingFailureReason != router.SelectionFailureNoSupportingTarget {
		t.Fatalf("recorded RoutingFailureReason = %q, want %q", parsed.Header.Meta.RoutingFailureReason, router.SelectionFailureNoSupportingTarget)
	}
	if parsed.Header.Meta.StatusCode != http.StatusBadGateway {
		t.Fatalf("recorded StatusCode = %d, want 502", parsed.Header.Meta.StatusCode)
	}
}

// recordedPreludesByStatus parses every cassette under root and keys them by the
// status code recorded in the prelude. The retry loop writes one cassette per
// attempt and the exhaustion path writes its own, so a test that cares about the
// final failure has to consider all of them rather than whichever the walk finds
// first.
func recordedPreludesByStatus(t *testing.T, root string) map[int]*recordfile.ParsedPrelude {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for {
		found := map[int]*recordfile.ParsedPrelude{}
		walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || filepath.Ext(path) != ".http" {
				return nil
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				lastErr = readErr
				return nil
			}
			parsed, parseErr := recordfile.ParsePrelude(content)
			if parseErr != nil {
				lastErr = parseErr
				return nil
			}
			found[parsed.Header.Meta.StatusCode] = parsed
			return nil
		})
		if walkErr == nil && len(found) > 0 {
			return found
		}
		if time.Now().After(deadline) {
			t.Fatalf("no parsed cassette under %q (last error %v)", root, lastErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestHandlerRecordsTheFailureWhenEveryUpstreamIsExhausted completes the coverage of
// the exhaustion path. TestHandlerRetryExhaustedReturns502 asserts the status code
// only, so a change that stopped recording the failure there would still pass it;
// the recorded trace is what the Monitor lists, so it is asserted here.
func TestHandlerRecordsTheFailureWhenEveryUpstreamIsExhausted(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	newFailingUpstream := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		}))
	}
	primary := newFailingUpstream()
	t.Cleanup(primary.Close)
	secondary := newFailingUpstream()
	t.Cleanup(secondary.Close)

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID: "primary", Enabled: boolPtr(true), Priority: 100,
				ModelDiscovery: router.ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5.5"},
				Upstream:       config.UpstreamConfig{BaseURL: primary.URL + "/v1", ProviderPreset: "openai"},
			},
			{
				ID: "secondary", Enabled: boolPtr(true), Priority: 90,
				ModelDiscovery: router.ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5.5"},
				Upstream:       config.UpstreamConfig{BaseURL: secondary.URL + "/v1", ProviderPreset: "openai"},
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
	t.Cleanup(proxyServer.Close)

	resp, err := proxyServer.Client().Post(proxyServer.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("client.Post() error = %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (all upstreams exhausted)", resp.StatusCode)
	}

	preludes := recordedPreludesByStatus(t, outputDir)
	final, ok := preludes[http.StatusBadGateway]
	if !ok {
		statuses := make([]int, 0, len(preludes))
		for status := range preludes {
			statuses = append(statuses, status)
		}
		t.Fatalf("no cassette records the 502 exhaustion; recorded statuses = %v", statuses)
	}
	if strings.TrimSpace(final.Header.Meta.Error) == "" {
		t.Fatalf("the exhaustion cassette records no error message: %+v", final.Header.Meta)
	}
	if !strings.Contains(final.Header.Meta.Error, "404") {
		t.Fatalf("recorded error = %q, want it to name the upstream status 404", final.Header.Meta.Error)
	}
}
