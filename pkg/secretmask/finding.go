package secretmask

import "strings"

// Reason records which evidence contributed to a finding, so an audit entry can
// explain why a value was rewritten without re-running the detector.
type Reason uint16

const (
	// ReasonStrongShape means the rule's shape alone is conclusive.
	ReasonStrongShape Reason = 1 << iota
	// ReasonAnchoredShape means a vendor-specific prefix matched.
	ReasonAnchoredShape
	// ReasonGenericShape means only a prefix shared with ordinary text matched.
	ReasonGenericShape
	// ReasonExactLength means the rule pins an exact length and the match had it.
	ReasonExactLength
	// ReasonValidator means an offline structural check confirmed the value.
	ReasonValidator
	// ReasonContext means credential vocabulary appeared near the match.
	ReasonContext
)

var reasonNames = []struct {
	bit  Reason
	name string
}{
	{ReasonStrongShape, "strong_shape"},
	{ReasonAnchoredShape, "anchored_shape"},
	{ReasonGenericShape, "generic_shape"},
	{ReasonExactLength, "exact_length"},
	{ReasonValidator, "validator"},
	{ReasonContext, "context"},
}

// String renders the set as a comma-separated list, for logs and audit records.
func (r Reason) String() string {
	if r == 0 {
		return "none"
	}
	var b strings.Builder
	for _, e := range reasonNames {
		if r&e.bit == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(e.name)
	}
	return b.String()
}

// Confidence is the band a finding's score falls into.
type Confidence uint8

const (
	ConfidenceLow Confidence = iota
	ConfidenceMedium
	ConfidenceHigh
)

func (c Confidence) String() string {
	switch c {
	case ConfidenceHigh:
		return "high"
	case ConfidenceMedium:
		return "medium"
	default:
		return "low"
	}
}

// Scoring weights. They are deliberately few and coarse: the point is a policy
// a reviewer can predict, not a tuned classifier.
const (
	scoreStrong   = 70
	scoreAnchored = 35
	scoreGeneric  = 10
	scoreExact    = 10
	scoreContext  = 40
	// scoreProven is the floor a passing validator earns on its own: an offline
	// structural proof does not need corroboration.
	scoreProven = 70

	// ConfidenceHighFrom and ConfidenceMediumFrom are the band edges.
	ConfidenceHighFrom   = 70
	ConfidenceMediumFrom = 45
)

// ConfidenceFor maps a score to its band.
func ConfidenceFor(score int) Confidence {
	switch {
	case score >= ConfidenceHighFrom:
		return ConfidenceHigh
	case score >= ConfidenceMediumFrom:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

// Finding is one detected credential plus the evidence supporting it.
type Finding struct {
	Span
	// Score is 0..100. Higher means more evidence.
	Score      int
	Confidence Confidence
	Reasons    Reason
}

// Maskable reports whether the finding is strong enough to be rewritten
// automatically. Low-confidence findings are still reported so that an audit
// can show what was seen but deliberately left alone.
//
// A ReportOnly rule is never maskable regardless of its score: a local path or
// a URL is audited as the exact bytes the agent used, and a stand-in would erase
// the fact under audit. The check is here as well as in the indicator scanner so
// that a caller which mixes the two finding sets still cannot rewrite one.
func (f Finding) Maskable() bool {
	if rule, ok := f.Rule(); ok && rule.ReportOnly {
		return false
	}
	return f.Confidence >= ConfidenceMedium
}

// scoreFinding turns the evidence for one match into a score and a reason set.
// validatorPassed reports whether the rule's validator accepted the bytes.
func scoreFinding(r *Rule, ctx bool, validatorPassed bool) (int, Reason) {
	var score int
	var why Reason
	switch r.Strength {
	case StrengthStrong:
		score, why = scoreStrong, ReasonStrongShape
	case StrengthAnchored:
		score, why = scoreAnchored, ReasonAnchoredShape
	default:
		score, why = scoreGeneric, ReasonGenericShape
	}
	if r.MaxLen > 0 && r.MinLen == r.MaxLen {
		score += scoreExact
		why |= ReasonExactLength
	}
	if ctx {
		score += scoreContext
		why |= ReasonContext
	}
	if validatorPassed {
		if score < scoreProven {
			score = scoreProven
		}
		why |= ReasonValidator
	}
	if score > 100 {
		score = 100
	}
	return score, why
}
