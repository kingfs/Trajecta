package analyzer

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// piiExcerptRadius is how much context either side of a match the evidence
// keeps. It is deliberately smaller than excerptLimit: the point of the excerpt
// is to locate the value in the trace, not to reproduce the record around it.
const piiExcerptRadius = 60

// PIIDetector reports personal data found in a trace.
//
// It reuses the masking engine instead of carrying a second set of patterns:
// secretmask.Scan is one pass over the node text, and a rule with a validator
// reports only values the offline check proved. That is the property this
// detector needs - an audit may not accuse an eighteen-digit order number of
// being an identity number, and a checksum, a calendar and a province prefix
// are exactly the evidence that separates the two.
//
// The masking path already records every replacement in
// credential_mask_audits. This detector answers a different question: it works
// on the stored observation, so it also covers traces recorded before masking
// was enabled, re-analysis, and values that reached the provider while masking
// was off.
type PIIDetector struct{}

func (PIIDetector) Name() string    { return "pii" }
func (PIIDetector) Version() string { return "0.1.0" }

func (d PIIDetector) Detect(_ context.Context, obs observe.TraceObservation) ([]observe.Finding, error) {
	var findings []observe.Finding
	for _, node := range allNodes(obs) {
		texts := []string{node.Text, metadataString(node.Metadata, "arguments"), rawText(node.Raw), rawText(node.JSON)}
		seen := map[secretmask.RuleID]bool{}
		for _, text := range texts {
			if text == "" {
				continue
			}
			for _, match := range secretmask.Scan([]byte(text)) {
				rule, ok := match.Rule()
				if !ok || !rule.Kind.PII() || seen[match.ID] {
					continue
				}
				seen[match.ID] = true
				findings = append(findings, finding(obs.TraceID, "pii_leak", observe.SeverityHigh, 0.95,
					"Personal data: "+rule.Kind.String(),
					"A value matching "+rule.Name+" passed its offline validator and appears in this trace.",
					node, excerptRedacted(text, match.Start, match.End), d.Name(), d.Version()))
			}
		}
	}
	return dedupeFindings(findings), nil
}

// excerptRedacted returns a short window around the match with the value itself
// replaced by a placeholder. The findings table is read by every monitor
// session and is exported; it must not become a second copy of the number the
// masking path exists to keep local. The cassette and the audit table already
// hold the real value for an operator who needs it.
func excerptRedacted(text string, start, end int) string {
	if start < 0 || end > len(text) || start >= end {
		return excerpt(text)
	}
	lo := start - piiExcerptRadius
	if lo < 0 {
		lo = 0
	}
	hi := end + piiExcerptRadius
	if hi > len(text) {
		hi = len(text)
	}
	// The offsets are bytes; a fixed window can land inside a multi-byte rune,
	// and an excerpt is display text, not a byte range.
	for lo > 0 && !utf8.RuneStart(text[lo]) {
		lo--
	}
	for hi < len(text) && !utf8.RuneStart(text[hi]) {
		hi++
	}
	var b strings.Builder
	if lo > 0 {
		b.WriteString("…")
	}
	b.WriteString(text[lo:start])
	b.WriteString("[redacted]")
	b.WriteString(text[end:hi])
	if hi < len(text) {
		b.WriteString("…")
	}
	return strings.TrimSpace(b.String())
}
