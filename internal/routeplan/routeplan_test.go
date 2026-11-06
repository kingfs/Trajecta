package routeplan

import (
	"encoding/json"
	"testing"
	"time"
)

// TestDecideResponses pins the one native-versus-local policy. The forwarding
// hot path and the Monitor's dry run both call it, so every branch here is a
// client-visible decision or rejection message.
func TestDecideResponses(t *testing.T) {
	const (
		noMatching      = "no matching Responses route is available for the requested model and strategy"
		nativePresent   = "native Responses upstream exists but is not selectable for this request"
		nativeOnly      = "responses_strategy native_only requires a matching native Responses upstream"
		localServerOnly = "responses_strategy local_server_only requires a matching chat completions backend"
	)

	tests := []struct {
		name       string
		in         ResponsesDecisionInput
		wantLocal  bool
		wantReject string
	}{
		{
			name:      "auto prefers a selectable native target",
			in:        ResponsesDecisionInput{Strategy: ResponsesStrategyAuto, NativeAvailable: true, NativePresent: true, LocalAvailable: true},
			wantLocal: false,
		},
		{
			name:       "auto rejects when a native target exists but is not selectable",
			in:         ResponsesDecisionInput{Strategy: ResponsesStrategyAuto, NativePresent: true, LocalAvailable: true},
			wantReject: nativePresent,
		},
		{
			name:      "auto falls back to the local runtime",
			in:        ResponsesDecisionInput{Strategy: ResponsesStrategyAuto, LocalAvailable: true},
			wantLocal: true,
		},
		{
			name:       "auto rejects when nothing is available",
			in:         ResponsesDecisionInput{Strategy: ResponsesStrategyAuto},
			wantReject: noMatching,
		},
		{
			name:      "prefer_native behaves like auto when native is selectable",
			in:        ResponsesDecisionInput{Strategy: ResponsesStrategyPreferNative, NativeAvailable: true, LocalAvailable: true},
			wantLocal: false,
		},
		{
			name:       "prefer_native still rejects for a present but unselectable native target",
			in:         ResponsesDecisionInput{Strategy: ResponsesStrategyPreferNative, NativePresent: true, LocalAvailable: true},
			wantReject: nativePresent,
		},
		{
			name:      "prefer_native falls back to local",
			in:        ResponsesDecisionInput{Strategy: ResponsesStrategyPreferNative, LocalAvailable: true},
			wantLocal: true,
		},
		{
			name:       "prefer_native rejects when nothing is available",
			in:         ResponsesDecisionInput{Strategy: ResponsesStrategyPreferNative},
			wantReject: noMatching,
		},
		{
			name:      "prefer_local_server prefers local",
			in:        ResponsesDecisionInput{Strategy: ResponsesStrategyPreferLocalServer, NativeAvailable: true, LocalAvailable: true},
			wantLocal: true,
		},
		{
			name:      "prefer_local_server falls back to native",
			in:        ResponsesDecisionInput{Strategy: ResponsesStrategyPreferLocalServer, NativeAvailable: true},
			wantLocal: false,
		},
		{
			name:       "prefer_local_server rejects when nothing is available",
			in:         ResponsesDecisionInput{Strategy: ResponsesStrategyPreferLocalServer, NativePresent: true},
			wantReject: noMatching,
		},
		{
			name:      "native_only accepts a native target",
			in:        ResponsesDecisionInput{Strategy: ResponsesStrategyNativeOnly, NativeAvailable: true, LocalAvailable: true},
			wantLocal: false,
		},
		{
			name:       "native_only rejects without a native target even when local is available",
			in:         ResponsesDecisionInput{Strategy: ResponsesStrategyNativeOnly, LocalAvailable: true},
			wantReject: nativeOnly,
		},
		{
			name:      "local_server_only accepts the local runtime",
			in:        ResponsesDecisionInput{Strategy: ResponsesStrategyLocalServerOnly, NativeAvailable: true, LocalAvailable: true},
			wantLocal: true,
		},
		{
			name:       "local_server_only rejects without a local backend even when native is available",
			in:         ResponsesDecisionInput{Strategy: ResponsesStrategyLocalServerOnly, NativeAvailable: true},
			wantReject: localServerOnly,
		},
		{
			name:      "empty strategy behaves as auto",
			in:        ResponsesDecisionInput{NativeAvailable: true},
			wantLocal: false,
		},
		{
			name:       "empty strategy falls back to auto rejection",
			in:         ResponsesDecisionInput{},
			wantReject: noMatching,
		},
		{
			name:      "unknown strategy behaves as auto",
			in:        ResponsesDecisionInput{Strategy: ResponsesStrategy("bogus"), LocalAvailable: true},
			wantLocal: true,
		},
		{
			name:       "unknown strategy behaves as auto on rejection",
			in:         ResponsesDecisionInput{Strategy: ResponsesStrategy("bogus"), NativePresent: true},
			wantReject: nativePresent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideResponses(tt.in)
			if got.UseLocal != tt.wantLocal {
				t.Fatalf("DecideResponses(%+v).UseLocal = %v, want %v", tt.in, got.UseLocal, tt.wantLocal)
			}
			if got.RejectReason != tt.wantReject {
				t.Fatalf("DecideResponses(%+v).RejectReason = %q, want %q", tt.in, got.RejectReason, tt.wantReject)
			}
		})
	}
}

// TestResultJSONKeepsTheInspectorShape pins the wire shape the Monitor frontend
// reads (result.plan and result.candidates). The package no longer plans, but
// the DTOs are the API contract.
func TestResultJSONKeepsTheInspectorShape(t *testing.T) {
	availableAt := time.Now().Add(time.Minute)
	result := Result{
		Plan: RoutePlan{
			ClientEntrypoint:        EntrypointResponses,
			RequestedModel:          "gpt-5",
			ResolvedModelCandidates: []ResolvedModelCandidate{{Model: "gpt-5", Source: "request"}},
			ExecutionMode:           ExecutionModeProxyPass,
			UpstreamEndpoint:        UpstreamEndpointResponses,
			SelectedCandidateID:     "primary",
			SelectedRouteTargetID:   "primary",
			SelectedChannelID:       "primary",
			UpstreamModel:           "gpt-5",
			Strategy:                ResponsesStrategyAuto,
			Reason:                  ReasonSelected,
			FallbacksConsidered: []PlanCandidate{{
				CandidateID:   "primary",
				Selectable:    true,
				Reason:        ReasonSelected,
				HealthState:   "healthy",
				AvailableAt:   &availableAt,
				ExecutionMode: ExecutionModeProxyPass,
			}},
		},
		Candidates: []PlanCandidate{{
			CandidateID:      "primary",
			Selectable:       true,
			Reason:           ReasonSelected,
			ExecutionMode:    ExecutionModeProxyPass,
			UpstreamEndpoint: UpstreamEndpointResponses,
		}},
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, key := range []string{"plan", "candidates"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("result JSON keys = %v, want %q (the frontend reads result.%s)", keysOf(decoded), key, key)
		}
	}
	var plan map[string]json.RawMessage
	if err := json.Unmarshal(decoded["plan"], &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	for _, key := range []string{
		"client_entrypoint", "requested_model", "resolved_model_candidates",
		"execution_mode", "upstream_endpoint", "selected_candidate_id",
		"selected_route_target_id", "selected_channel_id", "upstream_model",
		"strategy", "reason", "fallbacks_considered",
	} {
		if _, ok := plan[key]; !ok {
			t.Fatalf("plan JSON keys = %v, missing %q", keysOf(plan), key)
		}
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}
