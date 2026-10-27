package analyzer

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// AccessDetector reports the local filesystem paths and URLs that appear in a
// trace's model input or output. It is an audit aid: nothing is rewritten, and
// the finding severity is info, so it never raises the risk of a trace or feeds
// the alerting path.
//
// It shares the masking engine's second Ragel machine
// (secretmask.Indicators) instead of carrying its own patterns, so a path or a
// URL is recognized with the same rules in detection and in any future masking
// decision. Detection is per value, not per occurrence: the same path echoed in
// the request and the response produces one finding, anchored at the first node
// it appeared in, with the number of model steps that carried it. That is what
// an auditor reads the directory list from, and it keeps a conversation that
// repeats a path on every turn from growing a row per turn.
//
// Findings are stable across reanalysis. Their ID hashes the trace, category and
// value rather than stableFindingID's node-and-evidence tuple: two different
// URLs in one node must remain two rows, and both must survive a rescan under
// the same ID.
type AccessDetector struct{}

const (
	accessCategoryURL       = "url_access"
	accessCategoryLocalPath = "local_path_access"

	// accessValueLimit bounds the value copied into a finding's description.
	// EvidenceExcerpt is clipped again by the store; the description is what
	// carries the whole value.
	accessValueLimit = 2048
)

func (AccessDetector) Name() string    { return "access" }
func (AccessDetector) Version() string { return "0.1.0" }

func (d AccessDetector) Detect(_ context.Context, obs observe.TraceObservation) ([]observe.Finding, error) {
	seen := make(map[string]*accessIndicator, 16)
	order := make([]string, 0, 16)
	for _, node := range allNodes(obs) {
		// nodeSeen keeps the count to one per model step. nodeTexts returns
		// overlapping views of the same node - the normalized text and the raw
		// JSON carry the same bytes - and a value echoed in more than one of
		// them is still one occurrence in that step.
		nodeSeen := make(map[string]struct{}, 8)
		for _, text := range nodeTexts(node) {
			if text == "" {
				continue
			}
			src := []byte(text)
			for _, match := range secretmask.Indicators(src) {
				rule, ok := match.Rule()
				if !ok || !rule.ReportOnly {
					continue
				}
				category, ok := accessCategoryFor(rule.Kind)
				if !ok {
					continue
				}
				value := normalizeAccessValue(rule.Kind, string(match.Bytes(src)))
				if value == "" {
					continue
				}
				key := category + "\x00" + value
				if _, ok := nodeSeen[key]; ok {
					continue
				}
				nodeSeen[key] = struct{}{}
				if item, ok := seen[key]; ok {
					item.steps++
					continue
				}
				seen[key] = &accessIndicator{
					node:     node,
					category: category,
					value:    value,
					kind:     rule.Kind,
					steps:    1,
				}
				order = append(order, key)
			}
		}
	}
	if len(order) == 0 {
		return nil, nil
	}
	findings := make([]observe.Finding, 0, len(order))
	for _, key := range order {
		findings = append(findings, seen[key].finding(obs.TraceID, d.Name(), d.Version()))
	}
	return findings, nil
}

// accessIndicator is one distinct value seen in a trace.
type accessIndicator struct {
	node     observe.SemanticNode
	category string
	value    string
	kind     secretmask.Kind
	// steps is the number of model nodes that carried the value. The same node
	// is scanned through several overlapping text views, so this is a count of
	// steps, not of raw matches.
	steps int
}

func (a *accessIndicator) finding(traceID, detector, version string) observe.Finding {
	title, label := accessCopy(a.category)
	f := observe.Finding{
		ID:              accessFindingID(traceID, a.category, a.value),
		TraceID:         traceID,
		Category:        a.category,
		Severity:        observe.SeverityInfo,
		Confidence:      0.7,
		Title:           title,
		Description:     fmt.Sprintf("The model context references this %s in %s: %s", label, accessSteps(a.steps), clipAccessValue(a.value)),
		EvidenceExcerpt: a.value,
		NodeID:          a.node.ID,
		Detector:        detector,
		DetectorVersion: version,
		Metadata: map[string]any{
			"value": a.value,
			"kind":  a.kind.String(),
			"steps": a.steps,
		},
		CreatedAt: time.Now().UTC(),
	}
	if a.node.ID != "" || a.node.Path != "" {
		f.EvidencePath = observe.EvidencePath(traceID, a.node)
	} else {
		f.EvidencePath = "trace#" + traceID
	}
	return f
}

func accessSteps(n int) string {
	if n == 1 {
		return "1 model step"
	}
	return fmt.Sprintf("%d model steps", n)
}

// accessCategoryFor maps the indicator kind to the finding category. The two
// categories are deliberately separate: a reviewer filtering for filesystem
// reads should not have to skip URLs.
func accessCategoryFor(kind secretmask.Kind) (string, bool) {
	switch kind {
	case secretmask.KindURL:
		return accessCategoryURL, true
	case secretmask.KindLocalPath:
		return accessCategoryLocalPath, true
	default:
		return "", false
	}
}

func accessCopy(category string) (title, label string) {
	if category == accessCategoryURL {
		return "URL referenced in model context", "URL"
	}
	return "Local filesystem path referenced in model context", "local path"
}

// accessFindingID derives the stable ID of one (trace, category, value). It
// intentionally does not include the node: the value is reported once per
// trace no matter how many nodes repeat it, and the anchor node is only where
// the first occurrence was seen.
func accessFindingID(traceID, category, value string) string {
	sum := sha1.Sum([]byte(strings.Join([]string{traceID, category, value}, "|")))
	return "finding_" + hex.EncodeToString(sum[:])[:16]
}

func clipAccessValue(value string) string {
	if len(value) <= accessValueLimit {
		return value
	}
	// The limit is in bytes and a path or URL may be non-ASCII; cutting inside
	// a rune would put invalid UTF-8 into the description column and into JSON.
	cut := accessValueLimit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…"
}

// normalizeAccessValue turns the raw matched bytes into the value an operator
// reads. A Windows path inside a tool call's arguments is JSON text, so every
// real backslash is doubled (C:\\Users\\alice); the finding records the path
// with one separator instead. A decoded UNC path also starts with a doubled
// backslash, but its network root is the only doubled run there, which is how
// the two forms are told apart.
func normalizeAccessValue(kind secretmask.Kind, value string) string {
	if kind != secretmask.KindLocalPath || !strings.Contains(value, `\\`) {
		return value
	}
	if strings.HasPrefix(value, `\\`) && !strings.Contains(value[2:], `\\`) {
		return value
	}
	return strings.ReplaceAll(value, `\\`, `\`)
}
