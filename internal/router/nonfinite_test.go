package router

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
)

// TestNonFiniteWeightFallsBackToTheDefault pins the router's own guard against a non-finite
// weight or capacity_hint.
//
// Load rejects a non-finite number in a config file (TestLoadRejectsNonFiniteNumbers), but the
// value can still arrive from a stored row or a legacy import, and it is not harmless: the two
// knobs divide a target's expected cost, so a NaN cost wins against every other candidate and
// pins all traffic to the affected target, and the snapshot the Monitor marshals then fails with
// `json: unsupported value: NaN`. The guard treats a non-finite value like the documented
// non-positive default.
func TestNonFiniteWeightFallsBackToTheDefault(t *testing.T) {
	cfg := &config.Config{Upstreams: []config.UpstreamTargetConfig{
		{
			ID: "non-finite", Enabled: boolPtr(true), Priority: 100,
			Weight: math.NaN(), CapacityHint: math.Inf(1),
			ModelDiscovery: ModelDiscoveryStaticOnly, StaticModels: []string{"shared"},
			Upstream: config.UpstreamConfig{BaseURL: "https://a.example/v1", ApiKey: "k", ProviderPreset: "openai"},
		},
		{
			ID: "normal", Enabled: boolPtr(true), Priority: 100, Weight: 1,
			ModelDiscovery: ModelDiscoveryStaticOnly, StaticModels: []string{"shared"},
			Upstream: config.UpstreamConfig{BaseURL: "https://b.example/v1", ApiKey: "k", ProviderPreset: "openai"},
		},
	}}
	rtr, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	for _, target := range rtr.targets {
		if math.IsNaN(target.Weight) || math.IsInf(target.Weight, 0) || math.IsNaN(target.CapacityHint) || math.IsInf(target.CapacityHint, 0) {
			t.Errorf("target %q kept weight=%v capacity_hint=%v, want the documented default", target.ID, target.Weight, target.CapacityHint)
		}
		if target.Weight != 1 || target.CapacityHint != 1 {
			t.Errorf("target %q has weight=%v capacity_hint=%v, want 1/1", target.ID, target.Weight, target.CapacityHint)
		}
	}

	// What the Monitor API marshals: a NaN anywhere here turns the response into a 500.
	if _, err := json.Marshal(rtr.Snapshots()); err != nil {
		t.Fatalf("json.Marshal(Snapshots()) error = %v; a non-finite value reached a JSON payload", err)
	}

	// The cost comparison has to stay a total order: with a NaN cost both directions compare as
	// "less", so the affected target wins against every candidate instead of one of them winning.
	left, right := rtr.targets[0], rtr.targets[1]
	leftScore := rtr.expectedCost(left, RequestFeatures{ModelName: "shared"})
	rightScore := rtr.expectedCost(right, RequestFeatures{ModelName: "shared"})
	forward := compareScore(left, leftScore, right, rightScore)
	backward := compareScore(right, rightScore, left, leftScore)
	if forward != -backward {
		t.Fatalf("compareScore() = %d one way and %d the other for the same pair (costs %v/%v); a non-finite value needs to fall back, not order candidates arbitrarily", forward, backward, leftScore, rightScore)
	}
	if forward == 0 && left.ID != right.ID {
		t.Fatalf("compareScore() = 0 for two distinct targets %q and %q; the ordering must be total", left.ID, right.ID)
	}
}
