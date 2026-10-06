package router

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

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

// TestRebuildCatalogDoesNotRaceWithModelRefresh reproduces the interleaving between
// a configuration reload and the periodic model refresh.
//
// refreshTargetsInMemory applies what a refresh learned with setRefreshResult, which
// replaces target.models under the target's lock; the refresh loop only takes the
// router's lock afterwards, to rebuild the model catalog. Every reload (Reload,
// ReloadWithCommit, RefreshNow) rebuilds that same catalog while holding the router's
// lock, so the two paths can touch one model map at the same time under two different
// locks. That is a data race on a plain map, and Go escalates it to
//
//	fatal error: concurrent map read and map write
//
// which takes the process down rather than returning an error. Run under -race.
//
// The two goroutines below are the two racing operations: rebuilding the catalog under
// the router's write lock, which is what the reload path does, and applying a refresh
// result to a live target, which is what the refresh path does without that lock.
func TestRebuildCatalogDoesNotRaceWithModelRefresh(t *testing.T) {
	upstreams := []config.UpstreamTargetConfig{{
		ID:             "race-a",
		Enabled:        boolPtr(true),
		Priority:       100,
		ModelDiscovery: ModelDiscoveryStaticOnly,
		StaticModels:   []string{"gpt-5"},
		Upstream: config.UpstreamConfig{
			BaseURL:        "https://api.openai.com/v1",
			ProviderPreset: "openai",
		},
	}}

	rtr, err := New(&config.Config{Upstreams: upstreams}, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	targets := rtr.Targets()
	if len(targets) == 0 {
		t.Fatal("no targets")
	}
	costs := rtr.costs

	const iterations = 600
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)

	// The reload path: rebuild the model catalog while holding the router's lock.
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			rtr.mu.Lock()
			rtr.rebuildCatalog()
			rtr.mu.Unlock()
		}
	}()

	// The refresh path: apply model discoveries to the live targets, exactly as
	// refreshTargetsInMemory does before the router lock is taken.
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			for _, target := range targets {
				target.setRefreshResult(
					[]string{"gpt-5", "gpt-4o", fmt.Sprintf("discovered-%d", i%5)},
					"ready", nil, 3, 15*time.Second, costs,
				)
			}
		}
	}()

	close(start)
	wg.Wait()
}

// TestSelectIsConcurrencySafeWithModelRefresh widens the concurrency gate to the
// interleaving that production actually has: selection on the request path, the
// periodic model refresh applying what it learned, and the reload that republishes the
// catalog. Selection holds the router's read lock, which admits unlimited concurrent
// selectors, so every mutable target field it reads must be read under the target's own
// lock - and the refresh replaces the model set and the health state from another
// goroutine. Run under -race.
func TestSelectIsConcurrencySafeWithModelRefresh(t *testing.T) {
	upstreams := make([]config.UpstreamTargetConfig, 0, 4)
	for _, id := range []string{"race-a", "race-b", "race-c", "race-d"} {
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
	costs := rtr.costs

	const selectors = 8
	const iterations = 150
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, selectors)

	for i := 0; i < selectors; i++ {
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
				req.Header.Set("Session_id", "sess-refresh-race")
				if _, err := rtr.Select(req); err != nil {
					errs <- err
					return
				}
			}
		}()
	}

	// The refresh goroutine: discover models for the live targets, then republish the
	// catalog, which is the order refreshAll and the refresh loop use.
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			for _, target := range rtr.Targets() {
				target.setRefreshResult(
					[]string{"gpt-5", "gpt-4o", fmt.Sprintf("discovered-%d", i%5)},
					"ready", nil, 3, 15*time.Second, costs,
				)
			}
			rtr.mu.Lock()
			rtr.rebuildCatalog()
			rtr.mu.Unlock()
		}
	}()

	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Select() error = %v", err)
	}
}
