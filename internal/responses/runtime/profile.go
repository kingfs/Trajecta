package runtime

import "strings"

type ContextBudget struct {
	ContextWindowTokens         int
	MaxOutputTokens             int
	CompactHistoryItemThreshold int
}

type ModelProfile struct {
	Name          string
	Pattern       string
	UpstreamModel string
	Budget        ContextBudget
}

type ResolvedModelProfile struct {
	Profile *ModelProfile
	Budget  ContextBudget
}

func (r ResolvedModelProfile) UpstreamModelOr(model string) string {
	model = strings.TrimSpace(model)
	if r.Profile == nil {
		return model
	}
	upstreamModel := strings.TrimSpace(r.Profile.UpstreamModel)
	if upstreamModel == "" {
		return model
	}
	return upstreamModel
}

func (c Config) ContextBudgetForModel(model string) ResolvedModelProfile {
	resolved := ResolvedModelProfile{
		Budget: ContextBudget{
			CompactHistoryItemThreshold: c.CompactHistoryItemThreshold,
		},
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return resolved
	}
	for i := range c.ModelProfiles {
		profile := c.ModelProfiles[i].normalized()
		if profile.Name == "" || !strings.EqualFold(profile.Name, model) {
			continue
		}
		return resolved.withProfile(profile)
	}
	for i := range c.ModelProfiles {
		profile := c.ModelProfiles[i].normalized()
		if profile.Pattern == "" || !wildcardMatch(profile.Pattern, model) {
			continue
		}
		return resolved.withProfile(profile)
	}
	return resolved
}

func (r ResolvedModelProfile) withProfile(profile ModelProfile) ResolvedModelProfile {
	r.Profile = &profile
	if profile.Budget.ContextWindowTokens > 0 {
		r.Budget.ContextWindowTokens = profile.Budget.ContextWindowTokens
	}
	if profile.Budget.MaxOutputTokens > 0 {
		r.Budget.MaxOutputTokens = profile.Budget.MaxOutputTokens
	}
	if profile.Budget.CompactHistoryItemThreshold > 0 {
		r.Budget.CompactHistoryItemThreshold = profile.Budget.CompactHistoryItemThreshold
	}
	return r
}

func (p ModelProfile) normalized() ModelProfile {
	p.Name = strings.TrimSpace(p.Name)
	p.Pattern = strings.TrimSpace(p.Pattern)
	p.UpstreamModel = strings.TrimSpace(p.UpstreamModel)
	return p
}

// wildcardMatch matches a model-name pattern (`*`, `?`) case-insensitively. Model names are
// compared case-insensitively throughout routing, so a profile must not silently stop applying when
// a client spells the same model differently; internal/config.MatchResponsesModelProfile (what the
// CLI reports) implements the same rule, and the proxy's parity gate compares the two.
func wildcardMatch(pattern, value string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	value = strings.ToLower(strings.TrimSpace(value))
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	pi, vi := 0, 0
	star, match := -1, 0
	for vi < len(value) {
		if pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == value[vi]) {
			pi++
			vi++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			star = pi
			match = vi
			pi++
			continue
		}
		if star != -1 {
			pi = star + 1
			match++
			vi = match
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}
