package upstream

import "testing"

func TestValidateCodingPlanCredential(t *testing.T) {
	if err := ValidateCodingPlanCredential("qwen_coding", "sk-sp-abc"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCodingPlanCredential("qwen_coding_intl", "sk-abc"); err == nil {
		t.Fatal("standard DashScope key accepted for Qwen Coding Plan")
	}
	if err := ValidateCodingPlanCredential("zai_coding", "id.secret"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCodingPlanCredential("openai", "any"); err != nil {
		t.Fatal(err)
	}
}
