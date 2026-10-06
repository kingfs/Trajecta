package router

import (
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
)

// TestDisabledCredentialIsLeftOutOfRouting pins that credentials[].enabled is honoured by the
// router, not only by the provider probe.
//
// `cmd/server/provider.go` skips a credential whose `enabled` is false when it picks the key for a
// probe, and the field is documented as a credential field, but buildTargets expanded every
// non-empty credential entry regardless. A disabled credential therefore kept serving traffic with
// the key an operator had turned off, which is exactly what disabling it is meant to stop.
func TestDisabledCredentialIsLeftOutOfRouting(t *testing.T) {
	disabled, enabled := false, true
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "openai-primary",
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: ModelDiscoveryStaticOnly,
			StaticModels:   []string{"gpt-5"},
			Upstream: config.UpstreamConfig{
				BaseURL:        "https://api.openai.com/v1",
				ApiKey:         "sk-inline",
				ProviderPreset: "openai",
			},
			Credentials: []config.CredentialConfig{
				{ID: "retired", ApiKey: "sk-retired", Enabled: &disabled},
				{ID: "current", ApiKey: "sk-current", Enabled: &enabled},
				// No explicit enabled: absent means enabled, as before.
				{ID: "untouched", ApiKey: "sk-untouched"},
			},
		}},
	}
	cfg.Router.Selection.Policy = PolicyFirstAvailable

	rtr, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	got := map[string]string{}
	for _, target := range rtr.Targets() {
		got[target.CredentialID] = target.Upstream.APIKey
	}
	if _, ok := got["retired"]; ok {
		t.Errorf("route targets = %v, want no target for the disabled credential %q", got, "retired")
	}
	for id, wantKey := range map[string]string{"current": "sk-current", "untouched": "sk-untouched"} {
		if key, ok := got[id]; !ok {
			t.Errorf("route targets = %v, want a target for credential %q", got, id)
		} else if key != wantKey {
			t.Errorf("target %q API key = %q, want %q", id, key, wantKey)
		}
	}
	if len(got) != 2 {
		t.Fatalf("route targets = %v, want exactly the two enabled credentials", got)
	}
}

// TestTargetWithOnlyDisabledCredentialsContributesNoRouteTarget pins the boundary the in-place
// skip exists for.
//
// Filtering the credential slice before the "no credentials configured" check would make a channel
// whose credentials are all disabled look like a channel that never declared any, and buildTargets
// would then create the default target that uses the channel-level api_key. That would resurrect
// traffic on a key the operator disabled. The channel must instead contribute no target, which
// leaves New() with the same "no enabled upstream targets configured" error a disabled channel
// produces.
func TestTargetWithOnlyDisabledCredentialsContributesNoRouteTarget(t *testing.T) {
	disabled := false
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "openai-primary",
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: ModelDiscoveryStaticOnly,
			StaticModels:   []string{"gpt-5"},
			Upstream: config.UpstreamConfig{
				BaseURL:        "https://api.openai.com/v1",
				ApiKey:         "sk-channel-fallback",
				ProviderPreset: "openai",
			},
			Credentials: []config.CredentialConfig{
				{ID: "retired", ApiKey: "sk-retired", Enabled: &disabled},
			},
		}},
	}
	cfg.Router.Selection.Policy = PolicyFirstAvailable

	_, err := New(cfg, nil)
	if err == nil {
		t.Fatalf("New() accepted a channel whose only credential is disabled; want no route target rather than a fallback to the channel key")
	}
	if !strings.Contains(err.Error(), "no enabled upstream targets") {
		t.Fatalf("New() error = %q, want the no-enabled-upstream-targets error", err)
	}
}
