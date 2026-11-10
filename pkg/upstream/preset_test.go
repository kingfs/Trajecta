package upstream

import (
	"net/url"
	"sort"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
)

func TestBuiltinPresets(t *testing.T) {
	presets := Presets()
	if len(presets) < 20 {
		t.Fatalf("Presets() returned %d presets, want at least 20", len(presets))
	}
	required := []string{
		"openai", "anthropic", "deepseek", "openrouter", "google",
		"moonshot", "zhipu", "zai", "minimax", "xai", "groq", "mistral",
		"together", "fireworks", "cerebras", "github", "azure", "vertex",
		"vllm", "ollama", "qwen_coding", "qwen_coding_intl", "zai_coding", "zhipu_coding",
	}
	byID := make(map[string]Preset, len(presets))
	for _, preset := range presets {
		if preset.ID == "" {
			t.Fatal("preset with empty ID")
		}
		if _, exists := byID[preset.ID]; exists {
			t.Fatalf("duplicate preset ID %q", preset.ID)
		}
		byID[preset.ID] = preset
		if preset.Name == "" {
			t.Errorf("preset %q has empty Name", preset.ID)
		}
		if !preset.Kind.Valid() {
			t.Errorf("preset %q has invalid Kind %q", preset.ID, preset.Kind)
		}
		if !preset.Auth.Mode.Valid() {
			t.Errorf("preset %q has invalid Auth.Mode %q", preset.ID, preset.Auth.Mode)
		}
		if preset.ID != "azure" && len(preset.Endpoints) == 0 {
			t.Errorf("preset %q has no endpoints", preset.ID)
		}
		for protocol, raw := range preset.Endpoints {
			if !protocol.Valid() {
				t.Errorf("preset %q has invalid protocol %q", preset.ID, protocol)
			}
			assertAbsoluteHTTPURL(t, preset.ID+" endpoint "+string(protocol), raw)
		}
		planIDs := make(map[string]bool, len(preset.Plans))
		for _, plan := range preset.Plans {
			if plan.ID == "" {
				t.Errorf("preset %q has a plan with empty ID", preset.ID)
			}
			if planIDs[plan.ID] {
				t.Errorf("preset %q has duplicate plan ID %q", preset.ID, plan.ID)
			}
			planIDs[plan.ID] = true
			if plan.Name == "" {
				t.Errorf("preset %q plan %q has empty Name", preset.ID, plan.ID)
			}
			for protocol, raw := range plan.Endpoints {
				if !protocol.Valid() {
					t.Errorf("preset %q plan %q has invalid protocol %q", preset.ID, plan.ID, protocol)
				}
				assertAbsoluteHTTPURL(t, preset.ID+" plan "+plan.ID+" endpoint "+string(protocol), raw)
			}
		}
	}
	for _, id := range required {
		if _, ok := byID[id]; !ok {
			t.Errorf("required preset %q is missing", id)
		}
	}
}

func TestPresetEndpointDetails(t *testing.T) {
	tests := []struct {
		preset   string
		protocol xir.Protocol
		want     string
	}{
		{"openai", xir.ProtocolChatCompletions, "https://api.openai.com/v1"},
		{"openai", xir.ProtocolResponses, "https://api.openai.com/v1"},
		{"anthropic", xir.ProtocolMessages, "https://api.anthropic.com"},
		{"deepseek", xir.ProtocolChatCompletions, "https://api.deepseek.com/v1"},
		{"deepseek", xir.ProtocolResponses, "https://api.deepseek.com/v1"},
		{"deepseek", xir.ProtocolMessages, "https://api.deepseek.com/anthropic"},
		{"openrouter", xir.ProtocolChatCompletions, "https://openrouter.ai/api/v1"},
		{"openrouter", xir.ProtocolMessages, "https://openrouter.ai/api"},
		{"google", xir.ProtocolGemini, "https://generativelanguage.googleapis.com/v1beta"},
		{"google", xir.ProtocolChatCompletions, "https://generativelanguage.googleapis.com/v1beta/openai"},
		{"vertex", xir.ProtocolGemini, "https://aiplatform.googleapis.com/v1/publishers/google/models"},
		{"vllm", xir.ProtocolChatCompletions, "http://localhost:8000/v1"},
		{"ollama", xir.ProtocolChatCompletions, "http://localhost:11434/v1"},
	}
	for _, test := range tests {
		preset, ok := LookupPreset(test.preset)
		if !ok {
			t.Fatalf("LookupPreset(%q) failed", test.preset)
		}
		if got := preset.Endpoints[test.protocol]; got != test.want {
			t.Errorf("preset %q protocol %q endpoint = %q, want %q", test.preset, test.protocol, got, test.want)
		}
	}
}

func TestPresetAuthDefaults(t *testing.T) {
	tests := []struct {
		preset string
		header string
		scheme string
	}{
		{"openai", "Authorization", "Bearer"},
		{"deepseek", "Authorization", "Bearer"},
		{"openrouter", "Authorization", "Bearer"},
		{"anthropic", "x-api-key", ""},
		{"google", "x-goog-api-key", ""},
		{"vertex", "x-goog-api-key", ""},
		{"azure", "api-key", ""},
	}
	for _, test := range tests {
		preset, ok := LookupPreset(test.preset)
		if !ok {
			t.Fatalf("LookupPreset(%q) failed", test.preset)
		}
		if preset.Auth.Mode != AuthModeAPIKey {
			t.Errorf("preset %q auth mode = %q, want api_key", test.preset, preset.Auth.Mode)
		}
		if preset.Auth.Header != test.header {
			t.Errorf("preset %q auth header = %q, want %q", test.preset, preset.Auth.Header, test.header)
		}
		if preset.Auth.Scheme != test.scheme {
			t.Errorf("preset %q auth scheme = %q, want %q", test.preset, preset.Auth.Scheme, test.scheme)
		}
	}
	for _, presetID := range []string{"vllm", "ollama"} {
		preset, ok := LookupPreset(presetID)
		if !ok {
			t.Fatalf("LookupPreset(%q) failed", presetID)
		}
		if preset.Auth.Mode != AuthModeNone {
			t.Errorf("preset %q auth mode = %q, want none", presetID, preset.Auth.Mode)
		}
	}
	anthropic, _ := LookupPreset("anthropic")
	if len(anthropic.Auth.Headers) != 1 {
		t.Fatalf("anthropic auth templates = %d, want 1", len(anthropic.Auth.Headers))
	}
	template := anthropic.Auth.Headers[0]
	if template.Name != "anthropic-version" || template.Value != "2023-06-01" || !template.IfAbsent {
		t.Errorf("anthropic version template = %+v", template)
	}
}

func TestLookupPresetAliasesAndCopies(t *testing.T) {
	aliases := map[string]string{
		"OpenAI":           "openai",
		"azure_openai":     "azure",
		"azure-openai":     "azure",
		"gemini":           "google",
		"google_genai":     "google",
		"github_models":    "github",
		"kimi":             "moonshot",
		"z.ai":             "zai",
		"z-ai":             "zai",
		"bigmodel":         "zhipu",
		"together_ai":      "together",
		"fireworks-ai":     "fireworks",
		"x_ai":             "xai",
		"mistralai":        "mistral",
		"google-ai-studio": "google",
	}
	for alias, want := range aliases {
		preset, ok := LookupPreset(alias)
		if !ok {
			t.Fatalf("LookupPreset(%q) failed", alias)
		}
		if preset.ID != want {
			t.Errorf("LookupPreset(%q).ID = %q, want %q", alias, preset.ID, want)
		}
	}
	if _, ok := LookupPreset("does-not-exist"); ok {
		t.Fatal("LookupPreset returned a preset for an unknown ID")
	}
	if _, ok := LookupPreset(""); ok {
		t.Fatal("LookupPreset returned a preset for an empty ID")
	}

	first, _ := LookupPreset("openai")
	first.Endpoints[xir.ProtocolChatCompletions] = "https://mutated.example/v1"
	first.Headers = map[string]string{"X-Mutated": "yes"}
	first.Plans = append(first.Plans, PlanVariant{ID: "mutated"})
	second, _ := LookupPreset("openai")
	if second.Endpoints[xir.ProtocolChatCompletions] != "https://api.openai.com/v1" {
		t.Errorf("catalog endpoint mutated through LookupPreset: %q", second.Endpoints[xir.ProtocolChatCompletions])
	}
	if _, ok := second.Headers["X-Mutated"]; ok {
		t.Error("catalog headers mutated through LookupPreset")
	}
	if len(second.Plans) != 0 {
		t.Errorf("catalog plans mutated through LookupPreset: %d", len(second.Plans))
	}

	list := Presets()
	list[0].Endpoints[xir.ProtocolChatCompletions] = "https://mutated.example/v1"
	again := Presets()
	if again[0].Endpoints[xir.ProtocolChatCompletions] == "https://mutated.example/v1" {
		t.Error("catalog endpoint mutated through Presets")
	}
}

func TestPresetIDsSorted(t *testing.T) {
	ids := PresetIDs()
	if len(ids) < 20 {
		t.Fatalf("PresetIDs() returned %d IDs", len(ids))
	}
	if !sort.StringsAreSorted(ids) {
		t.Errorf("PresetIDs() is not sorted: %v", ids)
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			t.Errorf("PresetIDs() contains duplicate %q", id)
		}
		seen[id] = true
	}
}

func TestPresetPlanLookup(t *testing.T) {
	preset, ok := LookupPreset("zai")
	if !ok {
		t.Fatal("LookupPreset(zai) failed")
	}
	plan, ok := preset.Plan("Coding")
	if !ok {
		t.Fatal("Preset.Plan(Coding) failed")
	}
	if plan.ID != "coding" || plan.Name == "" {
		t.Errorf("plan = %+v", plan)
	}
	if got := plan.Endpoints[xir.ProtocolResponses]; got != "https://api.z.ai/api/v1" {
		t.Errorf("plan responses endpoint = %q", got)
	}
	plan.Endpoints[xir.ProtocolResponses] = "https://mutated.example"
	again, _ := preset.Plan("coding")
	if again.Endpoints[xir.ProtocolResponses] == "https://mutated.example" {
		t.Error("plan lookup aliases stored endpoints")
	}
	if _, ok := preset.Plan("missing"); ok {
		t.Error("Preset.Plan returned a missing plan")
	}
}

func TestPlanMatchesModel(t *testing.T) {
	plan := PlanVariant{ModelFilter: "glm-*"}
	if !plan.MatchesModel("glm-5.3") {
		t.Error("glm-5.3 should match glm-*")
	}
	if !plan.MatchesModel("GLM-5.3") {
		t.Error("matching should be case-insensitive")
	}
	if plan.MatchesModel("gpt-5") {
		t.Error("gpt-5 should not match glm-*")
	}
	empty := PlanVariant{}
	if !empty.MatchesModel("anything") {
		t.Error("empty filter should match every model")
	}
}

func assertAbsoluteHTTPURL(t *testing.T, label string, raw string) {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Errorf("%s: url.Parse(%q): %v", label, raw, err)
		return
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		t.Errorf("%s: %q is not an absolute URL", label, raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		t.Errorf("%s: %q has scheme %q", label, raw, parsed.Scheme)
	}
	if parsed.Fragment != "" {
		t.Errorf("%s: %q has a fragment", label, raw)
	}
	if parsed.RawQuery != "" {
		t.Errorf("%s: %q has a query", label, raw)
	}
	if parsed.Path != "" && parsed.Path != "/" && parsed.Path[len(parsed.Path)-1] == '/' {
		t.Errorf("%s: %q has a trailing slash", label, raw)
	}
}

func TestCodingPlanPresetsUseDedicatedEndpoints(t *testing.T) {
	cases := []struct {
		id   string
		chat string
	}{
		{"qwen_coding", "https://coding.dashscope.aliyuncs.com/v1"},
		{"qwen_coding_intl", "https://coding-intl.dashscope.aliyuncs.com/v1"},
		{"zai_coding", "https://api.z.ai/api/coding/paas/v4"},
		{"zhipu_coding", "https://open.bigmodel.cn/api/coding/paas/v4"},
	}
	for _, tc := range cases {
		preset, ok := LookupPreset(tc.id)
		if !ok {
			t.Fatalf("LookupPreset(%q) not found", tc.id)
		}
		if got := preset.Endpoints[xir.ProtocolChatCompletions]; got != tc.chat {
			t.Fatalf("%s chat endpoint = %q, want %q", tc.id, got, tc.chat)
		}
		if preset.Auth.Mode != AuthModeAPIKey || preset.Auth.Scheme != "Bearer" {
			t.Fatalf("%s auth = %+v", tc.id, preset.Auth)
		}
	}
}
