package proxy

import (
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	responsesruntime "github.com/kingfs/Trajecta/internal/responses/runtime"
)

// TestResponsesModelProfilesAgreeWithTheRuntime pins the profile rule in both of its
// implementations.
//
// `responses_server.model_profiles` is matched twice: internal/config.MatchResponsesModelProfile
// answers it for the CLI (`config show`, `doctor`, model listing) and
// internal/responses/runtime.ContextBudgetForModel answers it for the request that is actually
// served. Operator-visible reporting and runtime behaviour therefore have to agree, including the
// mixed-case spellings that routing already accepts for the same model. The expectations below are
// explicit so that a case-sensitive regression on either side fails this gate.
func TestResponsesModelProfilesAgreeWithTheRuntime(t *testing.T) {
	cfg := &config.Config{
		ResponsesServer: config.ResponsesServerConfig{
			CompactHistoryItemThreshold: 12,
			ModelProfiles: []config.ResponsesModelProfileConfig{
				{
					Name:                        "gpt-4o",
					ContextWindowTokens:         128000,
					MaxOutputTokens:             4096,
					CompactHistoryItemThreshold: 3,
					UpstreamModel:               "upstream-4o",
				},
				{
					Pattern:             "gpt-4o-mini-*",
					ContextWindowTokens: 64000,
				},
				{
					Pattern:             "claude-3-*",
					ContextWindowTokens: 200000,
				},
			},
		},
	}

	runtimeProfiles, err := responsesRuntimeModelProfiles(cfg, nil)
	if err != nil {
		t.Fatalf("responsesRuntimeModelProfiles() error = %v", err)
	}
	runtimeConfig := responsesruntime.Config{
		CompactHistoryItemThreshold: cfg.ResponsesCompactHistoryItemThreshold(),
		ModelProfiles:               runtimeProfiles,
	}

	cases := []struct {
		model       string
		wantMatched bool
		wantKind    string
		wantWindow  int
		wantCompact int
	}{
		{model: "gpt-4o", wantMatched: true, wantKind: "exact", wantWindow: 128000, wantCompact: 3},
		{model: "GPT-4O", wantMatched: true, wantKind: "exact", wantWindow: 128000, wantCompact: 3},
		{model: "gpt-4o-mini-2024-07-18", wantMatched: true, wantKind: "pattern", wantWindow: 64000, wantCompact: 12},
		{model: "GPT-4O-MINI-2024-07-18", wantMatched: true, wantKind: "pattern", wantWindow: 64000, wantCompact: 12},
		{model: "claude-3-5-sonnet", wantMatched: true, wantKind: "pattern", wantWindow: 200000, wantCompact: 12},
		{model: "CLAUDE-3-5-SONNET", wantMatched: true, wantKind: "pattern", wantWindow: 200000, wantCompact: 12},
		{model: "GLM-5.1", wantMatched: false, wantKind: "", wantWindow: 0, wantCompact: 12},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			match := cfg.MatchResponsesModelProfile(tc.model)
			resolved := runtimeConfig.ContextBudgetForModel(tc.model)

			if match.Matched != tc.wantMatched {
				t.Fatalf("MatchResponsesModelProfile(%q).Matched = %v, want %v", tc.model, match.Matched, tc.wantMatched)
			}
			// The unmatched result reports no kind ("exact" / "pattern" only for a match).
			if tc.wantMatched && match.Kind != tc.wantKind {
				t.Fatalf("MatchResponsesModelProfile(%q).Kind = %q, want %q", tc.model, match.Kind, tc.wantKind)
			}
			if !tc.wantMatched && match.Kind != "" {
				t.Fatalf("MatchResponsesModelProfile(%q).Kind = %q, want no kind", tc.model, match.Kind)
			}
			if match.Matched != (resolved.Profile != nil) {
				t.Fatalf("config reports matched=%v for %q while the runtime resolved profile=%v: the CLI and the served request must agree", match.Matched, tc.model, resolved.Profile)
			}
			if resolved.Budget.ContextWindowTokens != tc.wantWindow {
				t.Fatalf("runtime context window for %q = %d, want %d", tc.model, resolved.Budget.ContextWindowTokens, tc.wantWindow)
			}
			if resolved.Budget.CompactHistoryItemThreshold != tc.wantCompact {
				t.Fatalf("runtime compact threshold for %q = %d, want %d", tc.model, resolved.Budget.CompactHistoryItemThreshold, tc.wantCompact)
			}
			if !match.Matched {
				return
			}
			if got := match.Profile.ContextWindowTokens; got != tc.wantWindow {
				t.Fatalf("config context window for %q = %d, want %d", tc.model, got, tc.wantWindow)
			}
			if resolved.Profile.Budget.ContextWindowTokens != tc.wantWindow {
				t.Fatalf("runtime profile context window for %q = %d, want %d", tc.model, resolved.Profile.Budget.ContextWindowTokens, tc.wantWindow)
			}
			if match.Profile.Name != resolved.Profile.Name || match.Profile.Pattern != resolved.Profile.Pattern {
				t.Fatalf("config matched name:%q pattern:%q for %q while the runtime matched name:%q pattern:%q", match.Profile.Name, match.Profile.Pattern, tc.model, resolved.Profile.Name, resolved.Profile.Pattern)
			}
		})
	}
}
