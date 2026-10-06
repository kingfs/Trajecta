package routeplan

import (
	"errors"
	"testing"
	"time"
)

func healthChatRequest() Request {
	return Request{
		Entrypoint:     EntrypointChatCompletions,
		RequestedModel: "deepseek-flash",
	}
}

func healthCandidate(id string, health *CandidateHealth) UpstreamCandidate {
	return UpstreamCandidate{
		ID:                      id,
		RouteTargetID:           id,
		ChannelID:               id,
		Enabled:                 true,
		Models:                  []string{"deepseek-flash"},
		SupportsChatCompletions: true,
		Health:                  health,
	}
}

func TestPlanMarksOpenCircuitCandidateUnselectable(t *testing.T) {
	openUntil := time.Now().Add(10 * time.Second)
	result, err := Plan(healthChatRequest(), []UpstreamCandidate{
		healthCandidate("broken", &CandidateHealth{
			HealthState: "open",
			Selectable:  false,
			OpenUntil:   &openUntil,
		}),
		healthCandidate("healthy", &CandidateHealth{HealthState: "healthy", Selectable: true}),
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if result.Plan.SelectedChannelID != "healthy" {
		t.Fatalf("selected = %q, want healthy", result.Plan.SelectedChannelID)
	}
	for _, candidate := range result.Candidates {
		if candidate.ChannelID != "broken" {
			continue
		}
		if candidate.Selectable {
			t.Fatalf("open-circuit candidate reported selectable: %#v", candidate)
		}
		if candidate.Reason != ReasonTargetCircuitOpen {
			t.Fatalf("reason = %q, want %q", candidate.Reason, ReasonTargetCircuitOpen)
		}
		if !candidate.CircuitOpen || candidate.AvailableAt == nil {
			t.Fatalf("circuit details missing: %#v", candidate)
		}
	}
}

func TestPlanAllTargetsOpenIsDistinguishableFromNoCapableTarget(t *testing.T) {
	openUntil := time.Now().Add(10 * time.Second)
	_, err := Plan(healthChatRequest(), []UpstreamCandidate{
		healthCandidate("broken", &CandidateHealth{
			HealthState: "open",
			Selectable:  false,
			OpenUntil:   &openUntil,
		}),
	})
	var noRoute *NoRouteError
	if !errors.As(err, &noRoute) {
		t.Fatalf("Plan() error = %v, want NoRouteError", err)
	}
	if noRoute.Reason != ReasonAllTargetsOpen {
		t.Fatalf("reason = %q, want %q", noRoute.Reason, ReasonAllTargetsOpen)
	}

	// A channel that simply does not support the model keeps the configuration
	// reason, so callers can tell "not configured" from "temporarily open".
	_, err = Plan(healthChatRequest(), []UpstreamCandidate{{
		ID:      "other",
		Enabled: true,
		Models:  []string{"some-other-model"},
	}})
	noRoute = nil
	if !errors.As(err, &noRoute) {
		t.Fatalf("Plan() error = %v, want NoRouteError", err)
	}
	if noRoute.Reason != ReasonNoCandidate {
		t.Fatalf("reason = %q, want %q", noRoute.Reason, ReasonNoCandidate)
	}
}

func TestPlanWithoutHealthKeepsStaticDecision(t *testing.T) {
	result, err := Plan(healthChatRequest(), []UpstreamCandidate{healthCandidate("static", nil)})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if result.Plan.SelectedChannelID != "static" {
		t.Fatalf("selected = %q, want static", result.Plan.SelectedChannelID)
	}
	if !result.Candidates[0].Selectable {
		t.Fatalf("candidate without health must stay selectable: %#v", result.Candidates[0])
	}
}
