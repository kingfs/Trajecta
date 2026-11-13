// Package identify is the reusable, dependency-light detection surface of
// Trajecta's compiled Ragel DFA engine. It answers one question — "which
// sensitive shapes appear in these bytes, and where?" — without rewriting
// anything, without a plugin runtime, and without a database. Import it from
// another project to detect a single shape (a Chinese national ID, a mobile
// number, an API key) or to scan for every shape at once.
//
// # One algorithm, two views
//
// The detection algorithm lives in pkg/secretmask: a single Ragel union DFA
// (rules.rl) compiled from every credential and personal-data pattern, plus two
// native passes for the shapes a DFA cannot see (name-anchored environment
// assignments and DSN userinfo passwords). Ragel composes patterns at compile
// time into one machine, so a combined scan walks the bytes exactly once and
// runs every pattern together — there is no per-rule re-scan.
//
// This package exposes that one engine two ways:
//
//   - combined: Scan / ScanIndicators / ScanAll run the engine and return every
//     match;
//   - single-point: ChineseID, CNMobile, PII, ScanRule, ScanKind return a typed
//     view of the same scan, filtered to one shape.
//
// A single-point probe is a filter over the combined scan, not a second
// implementation: its verdicts are identical to the combined scan's by
// construction, which is what keeps a standalone caller and the full pipeline
// agreeing on what counts as a match. The engine's exact-literal prefilter
// gates the DFA, so a probe that cannot match the bytes returns without running
// the machine.
//
// # The two machines
//
// Credentials and personal data share the union DFA (Scan). Access indicators
// (URLs and local paths) are a second, deliberately separate machine
// (ScanIndicators): a Ragel scanner consumes what it matched, so a URL rule
// living in the credential machine would swallow a credential embedded in the
// URL and the masker would never see it. ScanAll runs both machines and merges
// the results; each machine still walks the bytes once.
//
// # Detection is not action
//
// A Match carries the evidence (score, confidence, reasons) and a Maskable
// flag, but this package never acts on it. Whether a match is replaced,
// reported, or indexed is the caller's policy — the same separation the
// Trajecta pipeline uses between its hot (replace) and audit (report) lanes.
package identify

import (
	"sort"

	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// Match is one detected shape and the evidence supporting it. Offsets are byte
// ranges into the scanned buffer, half-open [Start, End).
type Match struct {
	Start int `json:"start"`
	End   int `json:"end"`
	// Rule is the stable rule name, e.g. "pii.chinese_id" or "anthropic.api_key".
	// Callers persist and match on it.
	Rule string `json:"rule"`
	// Kind is the shape family, e.g. "national_id", "api_key", "url".
	Kind string `json:"kind"`
	// Vendor is the issuing organization, empty for vendor-neutral shapes.
	Vendor string `json:"vendor"`
	// Score is the engine's 0..100 evidence score.
	Score int `json:"score"`
	// Confidence is the score band mapped into [0,1]: high 1.0, medium 0.7,
	// low 0.4 — the same mapping the internal Scanner adapter uses.
	Confidence float64 `json:"confidence"`
	// Maskable reports whether the match is strong enough to rewrite
	// automatically. A report-only shape (URL, local path) is never maskable.
	// It is a policy hint; this package never rewrites bytes.
	Maskable bool `json:"maskable"`
	// Reasons renders the evidence flags, e.g. "strong_shape,validator".
	Reasons string `json:"reasons"`
}

// Len returns the match length in bytes.
func (m Match) Len() int { return m.End - m.Start }

// Bytes returns the matched bytes from src. It returns nil when the span does
// not fit, so a caller that kept a Match past the buffer's life fails safe.
func (m Match) Bytes(src []byte) []byte {
	if m.Start < 0 || m.End > len(src) || m.Start >= m.End {
		return nil
	}
	return src[m.Start:m.End]
}

// Scan runs the merged credential/PII DFA plus the native assignment and DSN
// passes over src in a single walk and returns every match, ordered by start.
// It detects API keys, tokens, private keys, JWT/JWE, bearer tokens, national
// IDs and mobile numbers — every shape in the union machine. It never rewrites
// src and retains no reference to it.
func Scan(src []byte) []Match {
	if len(src) == 0 {
		return nil
	}
	findings := secretmask.Detector{}.Scan(src)
	return convertFindings(findings)
}

// ScanIndicators runs the access-indicator machine over src and returns the
// URLs and local paths it finds, ordered by start. These shapes are report-only:
// every Match has Maskable false. This is a separate machine from Scan; see the
// package documentation for why.
func ScanIndicators(src []byte) []Match {
	if len(src) == 0 {
		return nil
	}
	return convertFindings(secretmask.Indicators(src))
}

// ScanAll runs both machines — the credential/PII union DFA and the access
// indicator DFA — and returns the merged matches ordered by start. Each machine
// walks src once. Use Scan or ScanIndicators alone when only one family is
// needed.
func ScanAll(src []byte) []Match {
	out := append(Scan(src), ScanIndicators(src)...)
	if len(out) == 0 {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].End < out[j].End
	})
	return out
}

func convertFindings(findings []secretmask.Finding) []Match {
	if len(findings) == 0 {
		return nil
	}
	out := make([]Match, 0, len(findings))
	for _, f := range findings {
		rule, _ := f.Rule()
		out = append(out, Match{
			Start:      f.Start,
			End:        f.End,
			Rule:       rule.Name,
			Kind:       rule.Kind.String(),
			Vendor:     rule.Vendor,
			Score:      f.Score,
			Confidence: confidence(f.Confidence),
			Maskable:   f.Maskable(),
			Reasons:    f.Reasons.String(),
		})
	}
	return out
}

func confidence(c secretmask.Confidence) float64 {
	switch c {
	case secretmask.ConfidenceHigh:
		return 1.0
	case secretmask.ConfidenceMedium:
		return 0.7
	default:
		return 0.4
	}
}
