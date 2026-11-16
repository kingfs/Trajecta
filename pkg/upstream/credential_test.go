package upstream

import "testing"

func TestValidateCodingPlanCredential(t *testing.T) {
	if err := ValidateCodingPlanCredential("qwen_coding", "sk-sp-abc"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCodingPlanCredential("qwen_coding_intl", "sk-abc"); err == nil {
		t.Fatal("standard DashScope key accepted for Qwen Coding Plan")
	}
	if err := ValidateCodingPlanCredential("qwen_token_plan", "sk-sp-abc"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCodingPlanCredential("qwen_token_plan_intl", "sk-abc"); err == nil {
		t.Fatal("standard DashScope key accepted for Qwen Token Plan")
	}
	if err := ValidateCodingPlanCredential("zai_coding", "id.secret"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCodingPlanCredential("openai", "any"); err != nil {
		t.Fatal(err)
	}
}

func TestPlanPresetMetadataMatchesPresets(t *testing.T) {
	// Every hidden preset with a Replacement feeds the derived plan metadata
	// table; each must stay resolvable and carry a display name.
	for _, preset := range Presets() {
		if !preset.Hidden || preset.Replacement == nil {
			continue
		}
		if _, ok := LookupPreset(preset.ID); !ok {
			t.Fatalf("plan metadata %q has no preset", preset.ID)
		}
		if PlanNameForPreset(preset.ID) == "" {
			t.Fatalf("plan %q has no display name", preset.ID)
		}
	}
}
