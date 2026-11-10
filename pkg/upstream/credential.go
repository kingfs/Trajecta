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
	case "qwen_coding", "qwen_coding_intl", "qwen_token_plan", "qwen_token_plan_intl":
		if !strings.HasPrefix(key, "sk-sp-") {
			return errors.New("Qwen Coding Plan requires a dedicated sk-sp- API key; a standard sk- DashScope key uses dashscope.aliyuncs.com instead")
		}
	}
	return nil
}

// IsCodingPlanPreset reports whether id names a dedicated coding-plan preset.
func IsCodingPlanPreset(id string) bool {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "qwen_coding", "qwen_coding_intl", "qwen_token_plan", "qwen_token_plan_intl", "zai_coding", "zhipu_coding", "kimi_coding":
		return true
	}
	return false
}
