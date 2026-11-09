package router

import (
	"context"
	"strings"

	"github.com/kingfs/Trajecta/pkg/route"
	"github.com/kingfs/Trajecta/pkg/xir"
)

func normalizePlanEngine(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "route":
		return "route"
	default:
		return "legacy"
	}
}

// pickRoute ranks the already-filtered candidate pool with pkg/route.Plan.
// The router still owns candidate filtering, health/cooldown exclusion,
// native-vs-conversion preference, affinity and all execution state; this
// method only replaces the final cost comparison. It is opt-in through
// router.selection.plan_engine=route.
func (r *Router) pickRoute(candidates []*Target, req RequestFeatures) (*Target, float64, bool) {
	if len(candidates) == 0 || req.Protocol == "" {
		return nil, 0, false
	}
	snap := route.Snapshot{Targets: make([]route.Target, 0, len(candidates))}
	for _, target := range candidates {
		target.mu.Lock()
		inflight := target.inflight
		latency := target.ttftFastMs
		target.mu.Unlock()
		snap.Targets = append(snap.Targets, route.Target{
			ID:           target.ID,
			ChannelID:    target.ChannelID,
			CredentialID: target.CredentialID,
			Model:        req.ModelName,
			Protocols:    []xir.Protocol{req.Protocol},
			Priority:     target.Priority,
			Weight:       target.Weight,
			Healthy:      true,
			Inflight:     int(inflight),
			LatencyMs:    latency,
		})
	}
	priority := route.PriorityCompat
	if r.strictPriority {
		priority = route.PriorityStrict
	}
	result, err := route.Plan(context.Background(), route.Request{
		Model:    req.ModelName,
		Protocol: req.Protocol,
		Stream:   req.Stream,
		Features: route.Features{Tools: req.HasTools, StructuredOutput: req.HasStructuredOutput, ContextTokens: int(req.EstPromptTokens)},
	}, snap, route.Policy{
		ModeOrder:    []route.Mode{route.ModeNative},
		PriorityMode: priority,
	})
	if err != nil {
		return nil, 0, false
	}
	for _, candidate := range candidates {
		if candidate.ID != result.Target.ID {
			continue
		}
		score := 0.0
		for _, evaluated := range result.Candidates {
			if evaluated.Target.ID == candidate.ID {
				score = evaluated.Score
				break
			}
		}
		return candidate, score, true
	}
	return nil, 0, false
}
