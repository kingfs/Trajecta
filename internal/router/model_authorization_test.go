package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
)

// A database-managed channel serves exactly the models the operator enabled.
// Discovery lists are inventory, not authorization, and the legacy unknown-model
// switch is gone: a disabled name and an unknown name are both rejected before
// any network call, with different reasons so the caller can say what to fix.
func TestConfiguredModelAuthorizationIsStrict(t *testing.T) {
	var calls atomic.Int64
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"data":[{"id":"disabled"},{"id":"undiscovered"}]}`))
	}))
	defer upstreamServer.Close()
	cfg := &config.Config{Upstreams: []config.UpstreamTargetConfig{{
		ID: "managed", ConfiguredModelsOnly: true, ModelDiscovery: ModelDiscoveryListModels,
		StaticModels: []string{"enabled", "disabled"}, DisabledModels: []string{"disabled"},
		Upstream: config.UpstreamConfig{BaseURL: upstreamServer.URL + "/v1", ProviderPreset: "openai"},
	}}}
	rtr, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		model  string
		reason string
	}{
		{"enabled", ""},
		{"disabled", SelectionFailureModelNotEnabled},
		{"unknown", SelectionFailureModelUnknown},
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+tc.model+`"}`))
		_, err := rtr.Select(req)
		if tc.reason == "" {
			if err != nil {
				t.Fatalf("Select(%s) error = %v, want a route", tc.model, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("Select(%s) succeeded, want %s", tc.model, tc.reason)
		}
		if got := SelectionFailureReason(err); got != tc.reason {
			t.Fatalf("Select(%s) reason = %q, want %q", tc.model, got, tc.reason)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("authorization performed %d discovery calls", calls.Load())
	}
	if got := rtr.AggregatedModels(); len(got) != 1 || got[0] != "enabled" {
		t.Fatalf("models=%v", got)
	}
}

// The model switch is per channel-model, not a global ban: switching
// deepseek-flash off on a spent channel must leave the model routable through
// the other channel that still enables it, and switching it off everywhere is
// the 404 model_not_enabled case.
func TestDisabledModelFallsThroughToAnotherChannel(t *testing.T) {
	cfg := &config.Config{Upstreams: []config.UpstreamTargetConfig{
		{
			ID: "spent", ConfiguredModelsOnly: true, ModelDiscovery: ModelDiscoveryStaticOnly,
			StaticModels: []string{"deepseek-flash"}, DisabledModels: []string{"deepseek-flash"},
			Upstream: config.UpstreamConfig{BaseURL: "https://api.deepseek.com/v1", ProviderPreset: "deepseek"},
		},
		{
			ID: "funded", ConfiguredModelsOnly: true, ModelDiscovery: ModelDiscoveryStaticOnly,
			StaticModels: []string{"deepseek-flash"},
			Upstream:     config.UpstreamConfig{BaseURL: "https://relay.example/v1", ProviderPreset: "openai"},
		},
	}}
	cfg.Router.Selection.Policy = PolicyFirstAvailable
	rtr, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"deepseek-flash"}`))
	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v, want the funded channel", err)
	}
	if selection.Target.ID != "funded" {
		t.Fatalf("selected target = %q, want funded", selection.Target.ID)
	}

	// Disable it on the second channel too: now no channel serves the name.
	if err := rtr.Reload([]config.UpstreamTargetConfig{
		{
			ID: "spent", ConfiguredModelsOnly: true, ModelDiscovery: ModelDiscoveryStaticOnly,
			StaticModels: []string{"deepseek-flash"}, DisabledModels: []string{"deepseek-flash"},
			Upstream: config.UpstreamConfig{BaseURL: "https://api.deepseek.com/v1", ProviderPreset: "deepseek"},
		},
		{
			ID: "funded", ConfiguredModelsOnly: true, ModelDiscovery: ModelDiscoveryStaticOnly,
			StaticModels: []string{"deepseek-flash"}, DisabledModels: []string{"deepseek-flash"},
			Upstream: config.UpstreamConfig{BaseURL: "https://relay.example/v1", ProviderPreset: "openai"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rtr.Select(req); SelectionFailureReason(err) != SelectionFailureModelNotEnabled {
		t.Fatalf("Select() after disabling everywhere error = %v, want %s", err, SelectionFailureModelNotEnabled)
	}
}

func TestReloadCommitFailurePreservesRuntime(t *testing.T) {
	rtr, err := New(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	old := []config.UpstreamTargetConfig{{ID: "old", ConfiguredModelsOnly: true, StaticModels: []string{"old-model"}, Upstream: config.UpstreamConfig{BaseURL: "https://example.invalid/v1", ProviderPreset: "openai"}}}
	if err := rtr.Reload(old); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("commit failed")
	if err := rtr.ReloadWithCommit(nil, func() error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("error=%v", err)
	}
	if got := rtr.AggregatedModels(); len(got) != 1 || got[0] != "old-model" {
		t.Fatalf("runtime changed after failed commit: %v", got)
	}
}
