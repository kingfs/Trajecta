package upstream

import (
	"path"
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
)

// Kind classifies where a preset's endpoints live.
type Kind string

const (
	// KindVendor is a first-party model vendor API.
	KindVendor Kind = "vendor"
	// KindRelay is a third-party aggregator or relay in front of several
	// vendors.
	KindRelay Kind = "relay"
	// KindLocal is a self-hosted or loopback server such as vLLM or Ollama.
	KindLocal Kind = "local"
)

// Valid reports whether k is a known preset kind.
func (k Kind) Valid() bool {
	switch k {
	case KindVendor, KindRelay, KindLocal:
		return true
	}
	return false
}

// Preset is one upstream provider catalog entry and, when visible, one channel
// plugin: the enable/disable unit of the catalog is the vendor site (vendor +
// region), never a billing plan or a protocol surface.
//
// A preset is data, not a configured channel: endpoints are base URLs, Auth is
// the default authentication specification, Plans are endpoint variants layered
// on top (a coding plan, for example) and Models describes how to list models.
// Resolve merges this data with a caller's Config.
type Preset struct {
	// ID is the stable canonical preset identifier.
	ID string
	// Name is the short English display name (vendor-region form, for example
	// "Alibaba Bailian-China"). Billing plans belong in Plans, not in the name.
	Name string
	// NameZh is the short Simplified Chinese display name (for example
	// "阿里百炼-中国"). Empty means Name is used for both languages.
	NameZh string
	// Kind classifies the provider.
	Kind Kind
	// Region distinguishes independent sites of one vendor ("cn", "intl", ...).
	// An empty region means the vendor has a single global site.
	Region string
	// Endpoints maps each natively served protocol to its API base URL. A preset
	// omits a protocol when the provider does not serve it. One channel carries
	// every declared endpoint with a single credential; a protocol surface is
	// never a separate preset.
	Endpoints map[xir.Protocol]string
	// Auth is the default authentication specification.
	Auth AuthSpec
	// Plans are endpoint variants selected with Config.Plan.
	Plans []PlanVariant
	// Models declares how the provider can list models.
	Models ModelSource
	// Headers are static headers applied to every request after auth.
	Headers map[string]string
	// DocsURL links to the provider's API documentation.
	DocsURL string
	// KeysURL links to the provider's API-key management page.
	KeysURL string
	// Notes records endpoint caveats a caller cannot infer from the data.
	Notes string
	// NotesZh is the Simplified Chinese description. Empty means Notes is used
	// for both languages.
	NotesZh string
	// Hidden keeps a legacy preset resolvable for existing channels while
	// excluding it from the plugin catalog UI. Hidden presets are never plugins.
	Hidden bool
	// Replacement points a hidden legacy preset at its canonical preset and
	// plan, so channel ownership and display can be derived from one mapping.
	Replacement *PresetReplacement
}

// PresetReplacement maps a hidden legacy preset to the canonical preset and
// plan that supersede it.
type PresetReplacement struct {
	// PresetID is the canonical preset identifier.
	PresetID string
	// Plan is the plan identifier within that preset ("" for the defaults).
	Plan string
}

// PlanVariant is one endpoint variant of a preset. A coding plan is the
// motivating case: it uses the same vendor but different base URLs (and possibly
// a different API key).
type PlanVariant struct {
	// ID is the stable plan identifier within its preset.
	ID string
	// Name is the human-readable plan name.
	Name string
	// NameZh is the Simplified Chinese plan name. Empty means Name is used for
	// both languages.
	NameZh string
	// Description explains the plan (billing model, key type, model scope).
	Description string
	// DescriptionZh is the Simplified Chinese description.
	DescriptionZh string
	// Endpoints overrides the preset endpoints for this plan.
	Endpoints map[xir.Protocol]string
	// ModelFilter is an optional case-insensitive glob (path.Match syntax) over
	// the client-facing model name. An empty filter matches every model.
	ModelFilter string
	// KeyPrefix is the documented API-key prefix for this plan (for example
	// "sk-sp-"). Empty means no prefix check applies.
	KeyPrefix string
	// Coding marks a coding-plan variant whose endpoints consume plan quota
	// rather than API balance.
	Coding bool
	// Models overrides the preset model source for this plan (nil = inherit).
	// Coding-plan endpoints commonly have no model-list API.
	Models *ModelSource
	// DocsURL overrides the preset documentation link for this plan.
	DocsURL string
	// KeysURL overrides the preset key-management link for this plan.
	KeysURL string
}

// MatchesModel reports whether the plan's ModelFilter selects model. An empty
// filter matches every model.
func (p PlanVariant) MatchesModel(model string) bool {
	filter := strings.TrimSpace(p.ModelFilter)
	if filter == "" {
		return true
	}
	matched, err := path.Match(strings.ToLower(filter), strings.ToLower(strings.TrimSpace(model)))
	return err == nil && matched
}

// Plan returns the plan with the given ID. The lookup is case-insensitive and
// treats '-' and ' ' as '_'.
func (p Preset) Plan(id string) (PlanVariant, bool) {
	want := normalizeKey(id)
	if want == "" {
		return PlanVariant{}, false
	}
	for _, plan := range p.Plans {
		if normalizeKey(plan.ID) == want {
			return clonePlan(plan), true
		}
	}
	return PlanVariant{}, false
}

// clonePreset returns a deep copy that a caller may mutate without touching the
// package-level catalog.
func clonePreset(p Preset) Preset {
	out := p
	out.Endpoints = cloneProtocolMap(p.Endpoints)
	out.Headers = cloneStringMap(p.Headers)
	out.Auth = cloneAuthSpec(p.Auth)
	out.Models.Static = append([]string(nil), p.Models.Static...)
	if p.Plans != nil {
		out.Plans = make([]PlanVariant, len(p.Plans))
		for i, plan := range p.Plans {
			out.Plans[i] = clonePlan(plan)
		}
	}
	if p.Replacement != nil {
		replacement := *p.Replacement
		out.Replacement = &replacement
	}
	return out
}

func clonePlan(p PlanVariant) PlanVariant {
	out := p
	out.Endpoints = cloneProtocolMap(p.Endpoints)
	if p.Models != nil {
		models := *p.Models
		models.Static = append([]string(nil), p.Models.Static...)
		out.Models = &models
	}
	return out
}

func cloneProtocolMap(input map[xir.Protocol]string) map[xir.Protocol]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[xir.Protocol]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
