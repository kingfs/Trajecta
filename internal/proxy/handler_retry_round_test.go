package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// TestHandlerRetryRoundAttemptsEveryCandidate pins that one retry round tries
// every candidate that was available when the round started.
//
// The loop used to compare the accumulated attempt count against
// selection.CandidateCount, but the router recomputes that count with the
// already-tried targets excluded, so it shrinks as the round progresses. The
// comparison `tried >= total - (tried - 1)` is satisfied once the round has
// attempted about half of the candidates, and a non-transient retryable status
// such as 404 then breaks out of the round and returns 502 to the client, even
// though a healthy candidate was never contacted. With two candidates the
// arithmetic happens to work out, which is why a two-upstream failover test
// does not catch it; this test uses four.
func TestHandlerRetryRoundAttemptsEveryCandidate(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	var mu sync.Mutex
	called := map[string]int{}
	track := func(id string) {
		mu.Lock()
		called[id]++
		mu.Unlock()
	}

	// Three upstreams reject the model with a retryable 404; the fourth serves
	// it. Priorities descend so first_available contacts them in this order.
	newFailing := func(id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			track(id)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"model not found","type":"not_found_error"}}`)
		}))
	}
	brokenA := newFailing("broken-a")
	defer brokenA.Close()
	brokenB := newFailing("broken-b")
	defer brokenB.Close()
	brokenC := newFailing("broken-c")
	defer brokenC.Close()

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		track("healthy")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_healthy","object":"response","usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`)
	}))
	defer healthy.Close()

	target := func(id string, priority int, baseURL string) config.UpstreamTargetConfig {
		return config.UpstreamTargetConfig{
			ID:             id,
			Enabled:        boolPtr(true),
			Priority:       priority,
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"gpt-5.5"},
			Upstream: config.UpstreamConfig{
				BaseURL:        baseURL + "/v1",
				ProviderPreset: "openai",
				Capabilities:   config.UpstreamCapabilitiesConfig{Responses: boolPtr(true)},
			},
		}
	}

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			target("broken-a", 100, brokenA.URL),
			target("broken-b", 90, brokenB.URL),
			target("broken-c", 80, brokenC.URL),
			target("healthy", 10, healthy.URL),
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

	req, err := http.NewRequest(http.MethodPost, proxyServer.URL+"/v1/responses",
		bytes.NewBufferString(`{"model":"gpt-5.5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	mu.Lock()
	defer mu.Unlock()
	for _, id := range []string{"broken-a", "broken-b", "broken-c"} {
		if called[id] == 0 {
			t.Errorf("candidate %s was never attempted (called=%v)", id, called)
		}
	}
	if called["healthy"] == 0 {
		t.Fatalf("the healthy candidate was never attempted: status=%d body=%s called=%v",
			resp.StatusCode, string(body), called)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 with four candidates where only the last is healthy (called=%v body=%s)",
			resp.StatusCode, called, fmt.Sprintf("%.200s", string(body)))
	}
}
