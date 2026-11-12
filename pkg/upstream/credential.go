package upstream

import (
	"fmt"
	"strings"
)

// planPresetMeta carries the credential facts of a legacy plan preset (a
// hidden preset that represented one billing plan before plans became
// PlanVariants of their vendor preset).
type planPresetMeta struct {
	Name      string
	Coding    bool
	KeyPrefix string
}

// planPresetMetadata is derived from the hidden legacy presets and their
// Replacement plans, so the catalog stays the single source of truth: adding a
// plan never requires a parallel credential table.
var planPresetMetadata = func() map[string]planPresetMeta {
	out := make(map[string]planPresetMeta)
	for _, preset := range builtinPresets {
		if !preset.Hidden || preset.Replacement == nil {
			continue
		}
		canonical, ok := LookupPreset(preset.Replacement.PresetID)
		if !ok {
			continue
		}
		plan, ok := canonical.Plan(preset.Replacement.Plan)
		if !ok {
			continue
		}
		out[preset.ID] = planPresetMeta{Name: preset.Name, Coding: plan.Coding, KeyPrefix: plan.KeyPrefix}
	}
	return out
}()

func planPresetMetaFor(id string) (planPresetMeta, bool) {
	meta, ok := planPresetMetadata[normalizeKey(id)]
	return meta, ok
}

// ValidateCodingPlanCredential rejects a credential that is clearly for the
// wrong surface. The check is deliberately narrow: it only knows facts the
// provider documents, so a custom/proxy key with an unfamiliar shape is left
// alone. It applies to the legacy plan presets; new channels validate through
// ValidatePlanCredential with an explicit plan.
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

// ValidatePlanCredential validates a credential against the selected plan of a
// preset. An empty planID falls back to the legacy plan-preset check, so
// channels created from hidden legacy presets keep their validation.
func ValidatePlanCredential(presetID, planID, apiKey string) error {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil
	}
	if strings.TrimSpace(planID) == "" {
		return ValidateCodingPlanCredential(presetID, key)
	}
	preset, ok := LookupPreset(presetID)
	if !ok {
		return nil
	}
	plan, ok := preset.Plan(planID)
	if !ok || plan.KeyPrefix == "" {
		return nil
	}
	if !strings.HasPrefix(key, plan.KeyPrefix) {
		return fmt.Errorf("%s %s requires a dedicated %s API key", preset.Name, plan.Name, plan.KeyPrefix)
	}
	return nil
}

// IsCodingPlanPreset reports whether id names a dedicated legacy coding-plan
// preset.
func IsCodingPlanPreset(id string) bool {
	meta, ok := planPresetMetaFor(id)
	return ok && meta.Coding
}

// IsCodingPlan reports whether preset+plan names a coding plan. An empty
// planID consults the legacy plan-preset table.
func IsCodingPlan(presetID, planID string) bool {
	if strings.TrimSpace(planID) == "" {
		return IsCodingPlanPreset(presetID)
	}
	preset, ok := LookupPreset(presetID)
	if !ok {
		return false
	}
	plan, ok := preset.Plan(planID)
	return ok && plan.Coding
}

// PlanNameForPreset returns the human name of a dedicated legacy plan preset,
// or "".
func PlanNameForPreset(id string) string {
	meta, ok := planPresetMetaFor(id)
	if !ok {
		return ""
	}
	return meta.Name
}

// PlanNameFor returns the display name of the selected plan, or "" when the
// preset/plan is unknown. An empty planID falls back to the legacy plan-preset
// name.
func PlanNameFor(presetID, planID string) string {
	if strings.TrimSpace(planID) == "" {
		return PlanNameForPreset(presetID)
	}
	preset, ok := LookupPreset(presetID)
	if !ok {
		return ""
	}
	plan, ok := preset.Plan(planID)
	if !ok {
		return ""
	}
	return plan.Name
}

// PlanNameZhFor returns the Simplified Chinese name of the selected plan,
// falling back to the English name. Legacy plan presets have no Chinese name.
func PlanNameZhFor(presetID, planID string) string {
	if strings.TrimSpace(planID) == "" {
		return PlanNameForPreset(presetID)
	}
	preset, ok := LookupPreset(presetID)
	if !ok {
		return ""
	}
	plan, ok := preset.Plan(planID)
	if !ok {
		return ""
	}
	if strings.TrimSpace(plan.NameZh) != "" {
		return plan.NameZh
	}
	return plan.Name
}

// PlanPresetIDs returns the legacy plan preset IDs in sorted order. It is used
// by tests and by callers that need to validate the metadata table.
func PlanPresetIDs() []string {
	out := make([]string, 0, len(planPresetMetadata))
	for id := range planPresetMetadata {
		out = append(out, id)
	}
	return out
}
