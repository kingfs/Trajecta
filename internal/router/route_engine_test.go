package router

import (
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
)

func TestPlanEngineRouteSelectsLowerCost(t *testing.T) {
	r := &Router{planEngine: "route"}
	slow := &Target{ID: "slow", Weight: 1}
	slow.ttftFastMs = 1000
	fast := &Target{ID: "fast", Weight: 1}
	fast.ttftFastMs = 10
	picked, score, ok := r.pickRoute([]*Target{slow, fast}, RequestFeatures{
		Protocol:  xir.ProtocolChatCompletions,
		ModelName: "m",
	})
	if !ok {
		t.Fatal("pickRoute did not select")
	}
	if picked.ID != "fast" || score <= 0 {
		t.Fatalf("picked=%s score=%f", picked.ID, score)
	}
}

func TestPlanEngineRouteRespectsStrictPriority(t *testing.T) {
	r := &Router{planEngine: "route", strictPriority: true}
	high := &Target{ID: "high", Priority: 200, Weight: 1}
	high.ttftFastMs = 1000
	low := &Target{ID: "low", Priority: 100, Weight: 1}
	low.ttftFastMs = 10
	picked, _, ok := r.pickRoute([]*Target{low, high}, RequestFeatures{
		Protocol:  xir.ProtocolChatCompletions,
		ModelName: "m",
	})
	if !ok || picked.ID != "high" {
		t.Fatalf("picked=%v ok=%v", picked, ok)
	}
}

func TestNormalizePlanEngine(t *testing.T) {
	for input, want := range map[string]string{"": "legacy", "legacy": "legacy", "route": "route", "ROUTE": "route", "other": "legacy"} {
		if got := normalizePlanEngine(input); got != want {
			t.Fatalf("normalizePlanEngine(%q) = %q, want %q", input, got, want)
		}
	}
}
