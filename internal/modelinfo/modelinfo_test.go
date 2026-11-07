package modelinfo

import "testing"

func TestLookupResolvesRegistryIDsAndAliases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		model     string
		wantID    string
		wantMatch bool
	}{
		{name: "canonical id", model: "openai/gpt-4o", wantID: "openai/gpt-4o", wantMatch: true},
		{name: "short alias", model: "gpt-4o", wantID: "openai/gpt-4o", wantMatch: true},
		{name: "uppercase alias", model: "GPT-4O", wantID: "openai/gpt-4o", wantMatch: true},
		{name: "uppercase canonical id", model: "OPENAI/GPT-4O", wantID: "openai/gpt-4o", wantMatch: true},
		{name: "routing prefix", model: "azure/gpt-4o", wantID: "openai/gpt-4o", wantMatch: true},
		{name: "nested routing prefix", model: "openrouter/openai/gpt-4o", wantID: "openai/gpt-4o", wantMatch: true},
		{name: "dashed claude version", model: "claude-3-5-sonnet", wantID: "anthropic/claude-3.5-sonnet", wantMatch: true},
		{name: "bedrock deployment id", model: "anthropic.claude-3-5-sonnet-20240620-v1:0", wantID: "anthropic/claude-3.5-sonnet", wantMatch: true},
		{name: "regional bedrock deployment id", model: "us.anthropic.claude-3-5-sonnet-20241022-v2:0", wantID: "anthropic/claude-3.5-sonnet", wantMatch: true},
		{name: "dated snapshot", model: "gpt-4o-2024-08-06", wantID: "openai/gpt-4o-2024-08-06", wantMatch: true},
		{name: "unknown model", model: "not-a-real-model-name"},
		{name: "empty name"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			facts, ok := Lookup(tc.model)
			if ok != tc.wantMatch {
				t.Fatalf("Lookup(%q) matched = %v, want %v (facts = %+v)", tc.model, ok, tc.wantMatch, facts)
			}
			if facts.Matched != tc.wantMatch {
				t.Fatalf("Lookup(%q).Matched = %v, want %v", tc.model, facts.Matched, tc.wantMatch)
			}
			if !tc.wantMatch {
				if facts.Query != tc.model {
					t.Fatalf("Lookup(%q).Query = %q", tc.model, facts.Query)
				}
				return
			}
			if facts.ID != tc.wantID {
				t.Fatalf("Lookup(%q).ID = %q, want %q", tc.model, facts.ID, tc.wantID)
			}
			if facts.Name == "" || facts.Provider == "" {
				t.Fatalf("Lookup(%q) returned incomplete facts: %+v", tc.model, facts)
			}
		})
	}
}

func TestLookupKeepsLocalizedDescription(t *testing.T) {
	t.Parallel()

	facts, ok := Lookup("gpt-4o")
	if !ok {
		t.Fatal("Lookup(gpt-4o) did not match")
	}
	if facts.Description == "" {
		t.Fatalf("facts.Description is empty: %+v", facts)
	}
	if len(facts.DescriptionCN) == 0 {
		t.Logf("catalogue has no Chinese description for %s yet", facts.ID)
	}
	if len(facts.Capabilities) == 0 {
		t.Fatalf("facts.Capabilities is empty: %+v", facts)
	}
}
