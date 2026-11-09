package route

import (
	"math"
	"time"
)

// Deterministic score constants.
//
// Score is a cost in latency-equivalent milliseconds: lower is better. The
// planner never randomizes it, never samples P2C and never consults wall-clock
// time beyond the single Snapshot.Now reading.
//
//	score = clamp(LatencyMs) + 25 * max(Inflight, 0) + 100 / weight
//
// where weight is Target.Weight when it is a positive finite number and 1
// otherwise. An unhealthy or cooling-down target adds scoreUnavailablePenalty
// so its diagnostic score is far above every selectable candidate; such a
// target is filtered before selection anyway, so the penalty only keeps an
// explanation honest.
const (
	scoreInflightMs         = 25.0
	scoreWeightBase         = 100.0
	scoreUnavailablePenalty = 1e9
)

// scoreTarget computes the deterministic cost of one target at now.
//
// Rationale for the terms:
//   - latency is the dominant real cost and is already in milliseconds;
//   - each in-flight request adds 25 ms, modelling queueing without ever
//     letting it dominate a large latency difference;
//   - 100/weight makes weight=1 neutral, weight=2 twice as attractive as
//     weight=0.5, and an explicit zero weight neutral rather than a division by
//     zero;
//   - priority is deliberately absent. Priority is a tier/tiebreak, not a cost.
func scoreTarget(target Target, now time.Time) float64 {
	score := 0.0

	if latency := target.LatencyMs; latency > 0 && !math.IsNaN(latency) && !math.IsInf(latency, 0) {
		score += latency
	}

	if target.Inflight > 0 {
		score += float64(target.Inflight) * scoreInflightMs
	}

	weight := target.Weight
	if weight <= 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
		weight = 1
	}
	score += scoreWeightBase / weight

	if !target.Healthy || onCooldown(target, now) {
		score += scoreUnavailablePenalty
	}

	return score
}
