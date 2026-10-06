package router

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
)

// concurrentSelectRouter builds a router with enough selectable candidates that
// the p2c path samples two distinct targets, which is the code that touches the
// shared random generator.
func concurrentSelectRouter(t *testing.T) *Router {
	t.Helper()
	upstreams := make([]config.UpstreamTargetConfig, 0, 4)
	for _, id := range []string{"a", "b", "c", "d"} {
		upstreams = append(upstreams, config.UpstreamTargetConfig{
			ID:             id,
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: ModelDiscoveryStaticOnly,
			StaticModels:   []string{"gpt-5"},
			Upstream: config.UpstreamConfig{
				BaseURL:        "https://api.openai.com/v1",
				ProviderPreset: "openai",
			},
		})
	}
	rtr, err := New(&config.Config{Upstreams: upstreams}, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	return rtr
}

// TestSelectTargetsIsConcurrencySafe pins that concurrent selection is safe.
// selectTargets holds the router's read lock for the whole selection, so any
// mutable state it touches must itself be concurrency-safe: the read lock
// admits unlimited concurrent selectors. The p2c policy is the default, so a
// shared *rand.Rand here is a data race on the hot path of every multi-upstream
// deployment, and it also corrupts the sampling (duplicated or skewed
// candidate pairs). Run under -race to observe it.
func TestSelectTargetsIsConcurrencySafe(t *testing.T) {
	rtr := concurrentSelectRouter(t)

	const goroutines = 16
	const iterations = 200

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions",
					strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))
				if err != nil {
					errs <- err
					return
				}
				req.Header.Set("Content-Type", "application/json")
				// A distinct session key per call keeps the sticky path from
				// collapsing every goroutine onto one target.
				req.Header.Set("Session_id", "sess-concurrency")
				if _, err := rtr.Select(req); err != nil {
					errs <- err
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Select() error = %v", err)
	}
}
