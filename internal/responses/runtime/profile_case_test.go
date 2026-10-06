package runtime

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/internal/responses/protocol"
)

// TestModelProfilesMatchModelNamesCaseInsensitively pins that a model profile keeps applying when a
// client spells the same model differently.
//
// Model names are compared case-insensitively in routing: a target that declares `gpt-4o` serves a
// request for `GPT-4O`. The runtime resolved its profiles case-sensitively, so the very same request
// silently lost its context window, compaction threshold and `upstream_model` rewrite - and
// internal/config.MatchResponsesModelProfile, which the CLI uses to report the effective profile,
// agreed with the runtime only because it had the same bug. The proxy's
// TestResponsesModelProfilesAgreeWithTheRuntime exercises the same rule across both implementations.
func TestModelProfilesMatchModelNamesCaseInsensitively(t *testing.T) {
	cfg := Config{
		CompactHistoryItemThreshold: 20,
		ModelProfiles: []ModelProfile{
			{
				Name:          "gpt-4o",
				UpstreamModel: "upstream-4o",
				Budget: ContextBudget{
					ContextWindowTokens:         128000,
					MaxOutputTokens:             4096,
					CompactHistoryItemThreshold: 3,
				},
			},
			{
				Pattern: "claude-3-*",
				Budget: ContextBudget{
					ContextWindowTokens: 200000,
				},
			},
		},
	}

	cases := []struct {
		model        string
		wantName     string
		wantPattern  string
		wantWindow   int
		wantMaxOut   int
		wantCompact  int
		wantUpstream string
	}{
		{model: "gpt-4o", wantName: "gpt-4o", wantWindow: 128000, wantMaxOut: 4096, wantCompact: 3, wantUpstream: "upstream-4o"},
		{model: "GPT-4O", wantName: "gpt-4o", wantWindow: 128000, wantMaxOut: 4096, wantCompact: 3, wantUpstream: "upstream-4o"},
		{model: "Gpt-4O", wantName: "gpt-4o", wantWindow: 128000, wantMaxOut: 4096, wantCompact: 3, wantUpstream: "upstream-4o"},
		{model: "  gpt-4o  ", wantName: "gpt-4o", wantWindow: 128000, wantMaxOut: 4096, wantCompact: 3, wantUpstream: "upstream-4o"},
		{model: "claude-3-5-sonnet", wantPattern: "claude-3-*", wantWindow: 200000, wantCompact: 20},
		{model: "CLAUDE-3-5-SONNET", wantPattern: "claude-3-*", wantWindow: 200000, wantCompact: 20},
		{model: "other-model", wantCompact: 20},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got := cfg.ContextBudgetForModel(tc.model)
			if tc.wantName == "" && tc.wantPattern == "" {
				if got.Profile != nil {
					t.Fatalf("ContextBudgetForModel(%q) profile = %#v, want no profile", tc.model, got.Profile)
				}
			} else {
				if got.Profile == nil {
					t.Fatalf("ContextBudgetForModel(%q) profile = nil, want the %q profile", tc.model, tc.wantName+tc.wantPattern)
				}
				if got.Profile.Name != tc.wantName || got.Profile.Pattern != tc.wantPattern {
					t.Fatalf("ContextBudgetForModel(%q) profile = name:%q pattern:%q, want name:%q pattern:%q", tc.model, got.Profile.Name, got.Profile.Pattern, tc.wantName, tc.wantPattern)
				}
			}
			if got.Budget.ContextWindowTokens != tc.wantWindow {
				t.Fatalf("ContextBudgetForModel(%q) context window = %d, want %d", tc.model, got.Budget.ContextWindowTokens, tc.wantWindow)
			}
			if got.Budget.MaxOutputTokens != tc.wantMaxOut {
				t.Fatalf("ContextBudgetForModel(%q) max output = %d, want %d", tc.model, got.Budget.MaxOutputTokens, tc.wantMaxOut)
			}
			if got.Budget.CompactHistoryItemThreshold != tc.wantCompact {
				t.Fatalf("ContextBudgetForModel(%q) compact threshold = %d, want %d", tc.model, got.Budget.CompactHistoryItemThreshold, tc.wantCompact)
			}
			if got.UpstreamModelOr(tc.model) != func() string {
				if tc.wantUpstream != "" {
					return tc.wantUpstream
				}
				return tc.model
			}() {
				t.Fatalf("UpstreamModelOr(%q) = %q, want %q", tc.model, got.UpstreamModelOr(tc.model), tc.wantUpstream)
			}
		})
	}
}

// TestRuntimeCreateAutoCompactUsesProfileForADifferentlyCasedModel drives the same rule through a
// real request: the profile's compact_history_item_threshold has to decide the auto-compact for
// `GPT-4O-MINI` exactly as it does for `gpt-4o-mini`, otherwise the same conversation is compacted
// (or not) depending on the client's spelling.
func TestRuntimeCreateAutoCompactUsesProfileForADifferentlyCasedModel(t *testing.T) {
	memStore := NewMemoryStore()
	seedResponseForAutoCompactTest(t, memStore, "resp_profile_case_target", "GPT-4O-MINI")
	client := &fakeChatClient{
		resps: []ChatCompletionResponse{
			{
				Choices: []ChatChoice{{
					Message:      ChatMessage{Role: "assistant", Content: "Profile compact summary."},
					FinishReason: "stop",
				}},
			},
			{
				Choices: []ChatChoice{{
					Message:      ChatMessage{Role: "assistant", Content: "new answer"},
					FinishReason: "stop",
				}},
			},
		},
	}
	events := &fakeExecutionEventRecorder{}
	rt := New(Config{
		DefaultModel:                "fallback-model",
		AutoCompact:                 true,
		CompactHistoryItemThreshold: 10,
		ModelProfiles: []ModelProfile{{
			Pattern: "gpt-4o*",
			Budget: ContextBudget{
				ContextWindowTokens:         128000,
				MaxOutputTokens:             4096,
				CompactHistoryItemThreshold: 1,
			},
		}},
	}, client, memStore, WithExecutionEventRecorder(events))

	resp, err := rt.Create(context.Background(), protocol.CreateResponseRequest{
		Model:              "GPT-4O-MINI",
		PreviousResponseID: "resp_profile_case_target",
		Input:              "new question",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if len(client.reqs) != 2 {
		t.Fatalf("chat requests = %d, want compact + create: the profile has to apply to GPT-4O-MINI exactly as it does to gpt-4o-mini", len(client.reqs))
	}
	if resp.PreviousResponseID == "" || resp.PreviousResponseID == "resp_profile_case_target" {
		t.Fatalf("response previous_response_id = %q, want generated compact response id", resp.PreviousResponseID)
	}
	got := findExecutionEvent(events.events, "response.compact", "auto_triggered")
	if got == nil || got.DetailsJSON["history_item_threshold"] != 1 {
		t.Fatalf("auto_triggered event = %#v, want profile threshold 1", got)
	}
	if got.DetailsJSON["history_item_threshold_source"] != "model_profile" {
		t.Fatalf("auto_triggered event = %#v, want model_profile threshold source", got)
	}
}
