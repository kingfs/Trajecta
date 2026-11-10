package upstream

import (
	"fmt"
	"strings"
)

// planPresetMeta is the single source of truth for presets that represent a
// specific plan (coding plan, token plan). Keeping it here means adding a plan
// preset does not require three parallel switches.
type planPresetMeta struct {
	Name      string
	Coding    bool
	KeyPrefix string
}

var planPresetMetadata = map[string]planPresetMeta{
	"qwen_coding":                {Name: "Qwen Coding Plan (China)", Coding: true, KeyPrefix: "sk-sp-"},
	"qwen_coding_intl":           {Name: "Qwen Coding Plan (International)", Coding: true, KeyPrefix: "sk-sp-"},
	"qwen_coding_anthropic":      {Name: "Qwen Coding Plan (China, Anthropic)", Coding: true, KeyPrefix: "sk-sp-"},
	"qwen_coding_intl_anthropic": {Name: "Qwen Coding Plan (International, Anthropic)", Coding: true, KeyPrefix: "sk-sp-"},
	"qwen_token_plan":            {Name: "Qwen Token Plan (China)", KeyPrefix: "sk-sp-"},
	"qwen_token_plan_intl":       {Name: "Qwen Token Plan (International)", KeyPrefix: "sk-sp-"},
	"zai_coding":                 {Name: "Z.ai GLM Coding Plan", Coding: true},
	"zai_coding_anthropic":       {Name: "Z.ai GLM Coding Plan (Anthropic)", Coding: true},
	"zhipu_coding":               {Name: "Zhipu GLM Coding Plan", Coding: true},
	"zhipu_coding_anthropic":     {Name: "Zhipu GLM Coding Plan (Anthropic)", Coding: true},
	"kimi_coding":                {Name: "Kimi Code", Coding: true},
}

func planPresetMetaFor(id string) (planPresetMeta, bool) {
	meta, ok := planPresetMetadata[strings.ToLower(strings.TrimSpace(id))]
	return meta, ok
}

// ValidateCodingPlanCredential rejects a credential that is clearly for the
// wrong surface. The check is deliberately narrow: it only knows facts the
// provider documents, so a custom/proxy key with an unfamiliar shape is left
// alone.
func ValidateCodingPlanCredential(presetID, apiKey string) error {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil
	}
	meta, ok := planPresetMetaFor(presetID)
	if !ok || meta.KeyPrefix == "" {
		return nil
	}
	if !strings.HasPrefix(key, meta.KeyPrefix) {
		return fmt.Errorf("%s requires a dedicated %s API key", meta.Name, meta.KeyPrefix)
	}
	return nil
}

// IsCodingPlanPreset reports whether id names a dedicated coding-plan preset.
func IsCodingPlanPreset(id string) bool {
	meta, ok := planPresetMetaFor(id)
	return ok && meta.Coding
}

// PlanNameForPreset returns the human name of a dedicated plan preset, or "".
func PlanNameForPreset(id string) string {
	meta, ok := planPresetMetaFor(id)
	if !ok {
		return ""
	}
	return meta.Name
}

// PlanPresetIDs returns the plan preset IDs in sorted order. It is used by
// tests and by callers that need to validate the metadata table.
func PlanPresetIDs() []string {
	out := make([]string, 0, len(planPresetMetadata))
	for id := range planPresetMetadata {
		out = append(out, id)
	}
	return out
}
