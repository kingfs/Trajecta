package upstream

import (
	"errors"
	"strings"
)

// ValidateCodingPlanCredential rejects a credential that is clearly for the
// wrong surface. The check is deliberately narrow: it only knows facts the
// provider documents, so a custom/proxy key with an unfamiliar shape is left
// alone.
func ValidateCodingPlanCredential(presetID, apiKey string) error {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(presetID)) {
	case "qwen_coding", "qwen_coding_intl", "qwen_coding_anthropic", "qwen_coding_intl_anthropic", "qwen_token_plan", "qwen_token_plan_intl":
		if !strings.HasPrefix(key, "sk-sp-") {
			return errors.New("Qwen Coding Plan requires a dedicated sk-sp- API key; a standard sk- DashScope key uses dashscope.aliyuncs.com instead")
		}
	}
	return nil
}

// IsCodingPlanPreset reports whether id names a dedicated coding-plan preset.
func IsCodingPlanPreset(id string) bool {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "qwen_coding", "qwen_coding_intl", "qwen_coding_anthropic", "qwen_coding_intl_anthropic", "zai_coding", "zai_coding_anthropic", "zhipu_coding", "zhipu_coding_anthropic", "kimi_coding":
		return true
	}
	return false
}

// PlanNameForPreset returns the human name of a dedicated plan preset, or "".
func PlanNameForPreset(id string) string {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "qwen_coding":
		return "Qwen Coding Plan (China)"
	case "qwen_coding_intl":
		return "Qwen Coding Plan (International)"
	case "qwen_coding_anthropic":
		return "Qwen Coding Plan (China, Anthropic)"
	case "qwen_coding_intl_anthropic":
		return "Qwen Coding Plan (International, Anthropic)"
	case "zai_coding_anthropic":
		return "Z.ai GLM Coding Plan (Anthropic)"
	case "zhipu_coding_anthropic":
		return "Zhipu GLM Coding Plan (Anthropic)"
	case "qwen_token_plan":
		return "Qwen Token Plan (China)"
	case "qwen_token_plan_intl":
		return "Qwen Token Plan (International)"
	case "zai_coding":
		return "Z.ai GLM Coding Plan"
	case "zhipu_coding":
		return "Zhipu GLM Coding Plan"
	case "kimi_coding":
		return "Kimi Code"
	default:
		return ""
	}
}
