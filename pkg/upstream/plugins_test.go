package upstream

import (
	"sort"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
)

func TestPluginsAreVisiblePresets(t *testing.T) {
	plugins := Plugins()
	visible := VisiblePresets()
	if len(plugins) != len(visible) {
		t.Fatalf("Plugins() = %d, VisiblePresets() = %d", len(plugins), len(visible))
	}
	if len(plugins) < 20 {
		t.Fatalf("Plugins() returned %d plugins, want at least 20", len(plugins))
	}
	ids := PluginIDs()
	if !sort.StringsAreSorted(ids) {
		t.Errorf("PluginIDs() not sorted: %v", ids)
	}
	for i, plugin := range plugins {
		if plugin.ID != visible[i].ID {
			t.Errorf("plugin[%d].ID = %q, visible preset %q", i, plugin.ID, visible[i].ID)
		}
		if plugin.PresetID != plugin.ID {
			t.Errorf("plugin %q PresetID = %q, want the canonical preset ID", plugin.ID, plugin.PresetID)
		}
		if plugin.Name == "" {
			t.Errorf("plugin %q has empty Name", plugin.ID)
		}
		if plugin.NameZh == "" {
			t.Errorf("plugin %q has empty NameZh (bilingual catalog)", plugin.ID)
		}
	}
	for _, id := range []string{"alibaba", "alibaba_intl"} {
		if _, ok := LookupPlugin(id); !ok {
			t.Errorf("LookupPlugin(%q) failed", id)
		}
	}
	if _, ok := LookupPlugin("qwen_coding"); ok {
		t.Error("hidden legacy preset must not be a plugin")
	}
	if _, ok := LookupPlugin("does-not-exist"); ok {
		t.Error("LookupPlugin returned an unknown plugin")
	}
}

func TestHiddenLegacyPresetsResolveToReplacements(t *testing.T) {
	legacy := map[string]PresetReplacement{
		"qwen_coding":                {PresetID: "alibaba", Plan: "coding"},
		"qwen_coding_anthropic":      {PresetID: "alibaba", Plan: "coding"},
		"qwen_coding_intl":           {PresetID: "alibaba_intl", Plan: "coding"},
		"qwen_coding_intl_anthropic": {PresetID: "alibaba_intl", Plan: "coding"},
		"qwen_token_plan":            {PresetID: "alibaba", Plan: "token"},
		"qwen_token_plan_intl":       {PresetID: "alibaba_intl", Plan: "token"},
		"zai_coding":                 {PresetID: "zai", Plan: "coding"},
		"zai_coding_anthropic":       {PresetID: "zai", Plan: "coding"},
		"zhipu_coding":               {PresetID: "zhipu", Plan: "coding"},
		"zhipu_coding_anthropic":     {PresetID: "zhipu", Plan: "coding"},
		"kimi_coding":                {PresetID: "moonshot", Plan: "kimi-code"},
	}
	for id, want := range legacy {
		preset, ok := LookupPreset(id)
		if !ok {
			t.Fatalf("legacy preset %q must stay resolvable", id)
		}
		if !preset.Hidden {
			t.Errorf("legacy preset %q must be Hidden", id)
		}
		if preset.Replacement == nil {
			t.Fatalf("legacy preset %q has no Replacement", id)
		}
		if *preset.Replacement != want {
			t.Errorf("legacy preset %q Replacement = %+v, want %+v", id, *preset.Replacement, want)
		}
		plugin, ok := PluginForPreset(id)
		if !ok {
			t.Fatalf("PluginForPreset(%q) failed", id)
		}
		if plugin.ID != want.PresetID {
			t.Errorf("PluginForPreset(%q) = %q, want %q", id, plugin.ID, want.PresetID)
		}
		// The replacement plan must exist on the canonical preset.
		canonical, _ := LookupPreset(want.PresetID)
		if _, ok := canonical.Plan(want.Plan); !ok {
			t.Errorf("canonical preset %q has no plan %q", want.PresetID, want.Plan)
		}
	}
	for _, preset := range Presets() {
		if preset.Hidden {
			continue
		}
		plugin, ok := PluginForPreset(preset.ID)
		if !ok || plugin.ID != preset.ID {
			t.Errorf("PluginForPreset(%q) = %q, %v", preset.ID, plugin.ID, ok)
		}
	}
	if _, ok := PluginForPreset("unknown-preset"); ok {
		t.Error("PluginForPreset resolved an unknown preset")
	}
}

func TestPresetIDsForPlugin(t *testing.T) {
	cases := map[string][]string{
		"alibaba":      {"alibaba", "qwen_coding", "qwen_coding_anthropic", "qwen_token_plan"},
		"alibaba_intl": {"alibaba_intl", "qwen_coding_intl", "qwen_coding_intl_anthropic", "qwen_token_plan_intl"},
		"zai":          {"zai", "zai_coding", "zai_coding_anthropic"},
		"zhipu":        {"zhipu", "zhipu_coding", "zhipu_coding_anthropic"},
		"moonshot":     {"moonshot", "kimi_coding"},
		"openai":       {"openai"},
	}
	for pluginID, want := range cases {
		got := PresetIDsForPlugin(pluginID)
		if len(got) != len(want) {
			t.Fatalf("PresetIDsForPlugin(%q) = %v, want %v", pluginID, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("PresetIDsForPlugin(%q)[%d] = %q, want %q", pluginID, i, got[i], want[i])
			}
		}
	}
	if got := PresetIDsForPlugin("does-not-exist"); got != nil {
		t.Errorf("PresetIDsForPlugin(unknown) = %v, want nil", got)
	}
}

func TestAlibabaPluginEndpointsAndPlans(t *testing.T) {
	cases := []struct {
		preset       string
		region       string
		nameZh       string
		paygChat     string
		paygMessages string
		codingChat   string
		codingMsg    string
		tokenChat    string
		tokenMsg     string
	}{
		{
			preset: "alibaba", region: "cn", nameZh: "阿里百炼-中国",
			paygChat: "https://dashscope.aliyuncs.com/compatible-mode/v1", paygMessages: "https://dashscope.aliyuncs.com/apps/anthropic",
			codingChat: "https://coding.dashscope.aliyuncs.com/v1", codingMsg: "https://coding.dashscope.aliyuncs.com/apps/anthropic",
			tokenChat: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", tokenMsg: "https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic",
		},
		{
			preset: "alibaba_intl", region: "intl", nameZh: "阿里百炼-国际",
			paygChat: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", paygMessages: "https://dashscope-intl.aliyuncs.com/apps/anthropic",
			codingChat: "https://coding-intl.dashscope.aliyuncs.com/v1", codingMsg: "https://coding-intl.dashscope.aliyuncs.com/apps/anthropic",
			tokenChat: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1", tokenMsg: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic",
		},
	}
	for _, tc := range cases {
		preset, ok := LookupPreset(tc.preset)
		if !ok {
			t.Fatalf("LookupPreset(%q) failed", tc.preset)
		}
		if preset.Region != tc.region || preset.NameZh != tc.nameZh {
			t.Errorf("%s region/nameZh = %q/%q", tc.preset, preset.Region, preset.NameZh)
		}
		if preset.Hidden {
			t.Errorf("%s must be visible", tc.preset)
		}
		if got := preset.Endpoints[xir.ProtocolChatCompletions]; got != tc.paygChat {
			t.Errorf("%s payg chat = %q, want %q", tc.preset, got, tc.paygChat)
		}
		if got := preset.Endpoints[xir.ProtocolMessages]; got != tc.paygMessages {
			t.Errorf("%s payg messages = %q, want %q", tc.preset, got, tc.paygMessages)
		}
		if preset.Auth.Header != "Authorization" || preset.Auth.Scheme != "Bearer" {
			t.Errorf("%s auth = %+v, want Bearer on every surface", tc.preset, preset.Auth)
		}
		coding, ok := preset.Plan("coding")
		if !ok {
			t.Fatalf("%s has no coding plan", tc.preset)
		}
		if !coding.Coding || coding.KeyPrefix != "sk-sp-" {
			t.Errorf("%s coding plan = %+v", tc.preset, coding)
		}
		if coding.Endpoints[xir.ProtocolChatCompletions] != tc.codingChat || coding.Endpoints[xir.ProtocolMessages] != tc.codingMsg {
			t.Errorf("%s coding endpoints = %v", tc.preset, coding.Endpoints)
		}
		if coding.Models == nil || !coding.Models.NoList {
			t.Errorf("%s coding plan must declare NoList models", tc.preset)
		}
		if coding.Description == "" || coding.DescriptionZh == "" {
			t.Errorf("%s coding plan needs bilingual descriptions", tc.preset)
		}
		token, ok := preset.Plan("token")
		if !ok {
			t.Fatalf("%s has no token plan", tc.preset)
		}
		if token.KeyPrefix != "sk-sp-" || token.Coding {
			t.Errorf("%s token plan = %+v", tc.preset, token)
		}
		if token.Endpoints[xir.ProtocolChatCompletions] != tc.tokenChat || token.Endpoints[xir.ProtocolMessages] != tc.tokenMsg {
			t.Errorf("%s token endpoints = %v", tc.preset, token.Endpoints)
		}
	}
}

func TestResolvePlanOverridesEndpointsAndModels(t *testing.T) {
	resolved, err := Resolve(Config{PresetID: "alibaba", Plan: "coding"})
	if err != nil {
		t.Fatalf("Resolve(alibaba, coding): %v", err)
	}
	if resolved.Endpoints[xir.ProtocolChatCompletions] != "https://coding.dashscope.aliyuncs.com/v1" {
		t.Errorf("coding chat endpoint = %q", resolved.Endpoints[xir.ProtocolChatCompletions])
	}
	if resolved.Endpoints[xir.ProtocolMessages] != "https://coding.dashscope.aliyuncs.com/apps/anthropic" {
		t.Errorf("coding messages endpoint = %q", resolved.Endpoints[xir.ProtocolMessages])
	}
	if !resolved.Models.NoList {
		t.Error("coding plan must override the model source to NoList")
	}
	if resolved.Plan == nil || resolved.Plan.ID != "coding" {
		t.Errorf("resolved plan = %+v", resolved.Plan)
	}

	defaults, err := Resolve(Config{PresetID: "alibaba"})
	if err != nil {
		t.Fatalf("Resolve(alibaba): %v", err)
	}
	if defaults.Endpoints[xir.ProtocolChatCompletions] != "https://dashscope.aliyuncs.com/compatible-mode/v1" {
		t.Errorf("payg chat endpoint = %q", defaults.Endpoints[xir.ProtocolChatCompletions])
	}
	if defaults.Models.NoList || defaults.Models.ListPath != "/models" {
		t.Errorf("payg model source = %+v", defaults.Models)
	}

	// Explicit endpoint overrides still win over the plan.
	custom, err := Resolve(Config{
		PresetID:  "alibaba",
		Plan:      "coding",
		Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"},
	})
	if err != nil {
		t.Fatalf("Resolve with override: %v", err)
	}
	if custom.Endpoints[xir.ProtocolChatCompletions] != "https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1" {
		t.Errorf("explicit override lost: %q", custom.Endpoints[xir.ProtocolChatCompletions])
	}
	if custom.Endpoints[xir.ProtocolMessages] != "https://coding.dashscope.aliyuncs.com/apps/anthropic" {
		t.Errorf("plan messages endpoint lost: %q", custom.Endpoints[xir.ProtocolMessages])
	}
}

func TestValidatePlanCredential(t *testing.T) {
	cases := []struct {
		preset  string
		plan    string
		key     string
		wantErr bool
	}{
		{"alibaba", "coding", "sk-sp-abc", false},
		{"alibaba", "coding", "sk-standard", true},
		{"alibaba", "token", "sk-sp-abc", false},
		{"alibaba", "token", "sk-standard", true},
		{"alibaba", "", "sk-standard", false}, // pay-as-you-go has no prefix rule
		{"alibaba_intl", "coding", "sk-standard", true},
		{"alibaba_intl", "coding", "sk-sp-abc", false},
		{"alibaba", "missing-plan", "sk-standard", false},
		{"unknown-preset", "coding", "sk-standard", false},
		{"alibaba", "coding", "", false},
		{"zai", "coding", "id.secret", false}, // GLM plan has no prefix rule
		// Legacy presets keep their validation without a plan argument.
		{"qwen_coding", "", "sk-standard", true},
		{"qwen_coding", "", "sk-sp-abc", false},
		{"qwen_coding_anthropic", "", "sk-standard", true},
		{"qwen_token_plan_intl", "", "sk-standard", true},
	}
	for _, tc := range cases {
		err := ValidatePlanCredential(tc.preset, tc.plan, tc.key)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidatePlanCredential(%q, %q, %q) = %v, wantErr %v", tc.preset, tc.plan, tc.key, err, tc.wantErr)
		}
	}
}

func TestPlanDisplayFacts(t *testing.T) {
	if !IsCodingPlan("alibaba", "coding") {
		t.Error("alibaba coding should be a coding plan")
	}
	if IsCodingPlan("alibaba", "token") {
		t.Error("token plan is not a coding plan")
	}
	if !IsCodingPlan("qwen_coding", "") {
		t.Error("legacy qwen_coding should still be a coding plan")
	}
	if !IsCodingPlan("kimi_coding", "") {
		t.Error("legacy kimi_coding should still be a coding plan")
	}
	if got := PlanNameFor("alibaba", "coding"); got != "Coding Plan" {
		t.Errorf("PlanNameFor = %q", got)
	}
	if got := PlanNameZhFor("alibaba", "coding"); got != "Coding Plan" {
		t.Errorf("PlanNameZhFor = %q", got)
	}
	if got := PlanNameFor("qwen_coding", ""); got != "Qwen Coding Plan (China)" {
		t.Errorf("legacy PlanNameFor = %q", got)
	}
	if got := PlanNameFor("alibaba", ""); got != "" {
		t.Errorf("payg PlanNameFor = %q, want empty", got)
	}
	if got := PlanNameFor("alibaba", "missing"); got != "" {
		t.Errorf("unknown plan PlanNameFor = %q", got)
	}
}

func TestLegacyPlanMetadataDerivedFromCatalog(t *testing.T) {
	ids := PlanPresetIDs()
	if len(ids) != 11 {
		t.Fatalf("PlanPresetIDs() = %d entries, want 11: %v", len(ids), ids)
	}
	for _, id := range ids {
		preset, ok := LookupPreset(id)
		if !ok || !preset.Hidden || preset.Replacement == nil {
			t.Fatalf("legacy plan preset %q is not a hidden preset with a replacement", id)
		}
	}
	// The derived table must keep the historical names and prefix facts.
	if got := PlanNameForPreset("qwen_coding_anthropic"); got != "Qwen Coding Plan (China, Anthropic)" {
		t.Errorf("legacy name = %q", got)
	}
	if err := ValidateCodingPlanCredential("zai_coding", "any-key"); err != nil {
		t.Errorf("zai_coding has no key prefix rule, got %v", err)
	}
}

func TestReplacementDeepCopy(t *testing.T) {
	preset, _ := LookupPreset("qwen_coding")
	preset.Replacement.PresetID = "mutated"
	again, _ := LookupPreset("qwen_coding")
	if again.Replacement.PresetID != "alibaba" {
		t.Errorf("Replacement mutated through LookupPreset: %q", again.Replacement.PresetID)
	}
	canonical, _ := LookupPreset("alibaba")
	coding, _ := canonical.Plan("coding")
	coding.Models.NoList = false
	againCanonical, _ := LookupPreset("alibaba")
	againCoding, _ := againCanonical.Plan("coding")
	if !againCoding.Models.NoList {
		t.Error("plan Models mutated through LookupPreset")
	}
}
