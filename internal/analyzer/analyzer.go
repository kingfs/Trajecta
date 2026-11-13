package analyzer

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/secretmask"
)

const excerptLimit = 240
const (
	exchangeKindEntry  = "entry"
	exchangeKindModel  = "model"
	exchangeRoleLegacy = "legacy_model"
)

type Detector interface {
	Name() string
	Version() string
	Detect(context.Context, observe.TraceObservation) ([]observe.Finding, error)
}

type Runner struct {
	detectors []Detector
}

func NewRunner(detectors ...Detector) *Runner {
	if len(detectors) == 0 {
		detectors = DefaultDetectors()
	}
	return &Runner{detectors: detectors}
}

// NewRunnerList builds a runner from exactly the given detectors, with no
// fallback to DefaultDetectors. The detection-plugin layer uses it so that
// disabling every observation plugin yields an empty runner rather than silently
// restoring the builtin set.
func NewRunnerList(detectors []Detector) *Runner {
	return &Runner{detectors: detectors}
}

func DefaultDetectors() []Detector {
	return []Detector{
		DangerousShellDetector{},
		CredentialDetector{},
		PIIDetector{},
		ProviderSafetyDetector{},
		ToolErrorDetector{},
	}
}

func (r *Runner) Analyze(ctx context.Context, obs observe.TraceObservation) ([]observe.Finding, error) {
	var findings []observe.Finding
	// The credential and PII detectors scan the same node texts. One cache per
	// run lets the union DFA run once per distinct text instead of once per
	// detector (and once per duplicate Text/Raw view).
	ctx = withScanCache(ctx)
	scope := observationExchangeScope(obs)
	for _, detector := range r.detectors {
		if detector == nil {
			continue
		}
		if !detectorAllowedForScope(detector, scope) {
			continue
		}
		next, err := detector.Detect(ctx, obs)
		if err != nil {
			return nil, fmt.Errorf("%s detector: %w", detector.Name(), err)
		}
		for i := range next {
			if next[i].TraceID == "" {
				next[i].TraceID = obs.TraceID
			}
			if next[i].Detector == "" {
				next[i].Detector = detector.Name()
			}
			if next[i].DetectorVersion == "" {
				next[i].DetectorVersion = detector.Version()
			}
			if next[i].CreatedAt.IsZero() {
				next[i].CreatedAt = time.Now().UTC()
			}
			if next[i].ID == "" {
				next[i].ID = stableFindingID(next[i])
			}
			attachFindingExchangeScope(&next[i], scope)
		}
		findings = append(findings, next...)
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Severity == findings[j].Severity {
			return findings[i].ID < findings[j].ID
		}
		return severityRank(findings[i].Severity) > severityRank(findings[j].Severity)
	})
	return findings, nil
}

type exchangeScope struct {
	Kind string
	Role string
}

func observationExchangeScope(obs observe.TraceObservation) exchangeScope {
	kind := strings.TrimSpace(obs.ExchangeKind)
	role := strings.TrimSpace(obs.ExchangeRole)
	if kind == "" {
		kind = exchangeKindModel
	}
	if role == "" && kind == exchangeKindModel {
		role = exchangeRoleLegacy
	}
	return exchangeScope{Kind: kind, Role: role}
}

func detectorAllowedForScope(detector Detector, scope exchangeScope) bool {
	if scope.Kind != exchangeKindEntry {
		return true
	}
	switch detector.Name() {
	// A credential or a personal number in the request the client sent is the
	// signal an entry exchange exists to carry. The other detectors read the
	// model's response, which an entry exchange does not have. (The resource
	// index is not a detector: it runs in the parse worker for both kinds.)
	case CredentialDetector{}.Name(), PIIDetector{}.Name():
		return true
	default:
		return false
	}
}

func attachFindingExchangeScope(finding *observe.Finding, scope exchangeScope) {
	if finding.Metadata == nil {
		finding.Metadata = map[string]any{}
	}
	finding.Metadata["exchange_kind"] = scope.Kind
	if scope.Role != "" {
		finding.Metadata["exchange_role"] = scope.Role
	}
	finding.Metadata["exchange_scope"] = map[string]any{
		"kind": scope.Kind,
		"role": scope.Role,
	}
}

type DangerousShellDetector struct{}

func (DangerousShellDetector) Name() string    { return "dangerous_shell" }
func (DangerousShellDetector) Version() string { return "0.1.0" }

func (d DangerousShellDetector) Detect(_ context.Context, obs observe.TraceObservation) ([]observe.Finding, error) {
	var findings []observe.Finding
	for _, tool := range obs.Tools.Calls {
		text := firstNonEmpty(tool.ArgsText, rawText(tool.ArgsJSON))
		if text == "" {
			continue
		}
		node := findNodeByID(obs, tool.NodeID)
		if finding, ok := d.detectText(obs.TraceID, node, tool.Kind, text); ok {
			findings = append(findings, finding)
		}
	}
	for _, node := range allNodes(obs) {
		if node.NormalizedType != observe.NodeToolCall && node.NormalizedType != observe.NodeServerToolCall && node.NormalizedType != observe.NodeCode {
			continue
		}
		text := firstNonEmpty(node.Text, metadataString(node.Metadata, "arguments"), rawText(node.Raw), rawText(node.JSON))
		if finding, ok := d.detectText(obs.TraceID, node, node.ProviderType, text); ok {
			findings = append(findings, finding)
		}
	}
	return dedupeFindings(findings), nil
}

func (d DangerousShellDetector) detectText(traceID string, node observe.SemanticNode, kind string, text string) (observe.Finding, bool) {
	lower := strings.ToLower(text)
	rules := []struct {
		category string
		severity observe.Severity
		title    string
		match    func(string) bool
	}{
		{"filesystem_destructive_operation", observe.SeverityCritical, "Destructive filesystem command", func(s string) bool {
			return destructiveRMPattern.MatchString(s) || strings.Contains(s, "sudo rm") ||
				strings.Contains(s, "mkfs") || strings.Contains(s, "diskutil erase") || strings.Contains(s, "dd if=")
		}},
		{"dangerous_command", observe.SeverityHigh, "Dangerous shell command", func(s string) bool {
			return strings.Contains(s, "chmod -r 777") || strings.Contains(s, "chown -r") ||
				strings.Contains(s, "nc -e") || strings.Contains(s, "bash -i")
		}},
		{"unsafe_code_execution", observe.SeverityHigh, "Downloaded script execution", func(s string) bool {
			return (strings.Contains(s, "curl") || strings.Contains(s, "wget")) &&
				(strings.Contains(s, "| sh") || strings.Contains(s, "| bash"))
		}},
		{"credential_leak", observe.SeverityHigh, "Credential file access from shell", func(s string) bool {
			return strings.Contains(s, ".ssh/id_rsa") || strings.Contains(s, ".aws/credentials") ||
				strings.Contains(s, "security find-generic-password") || strings.Contains(s, "env | curl")
		}},
	}
	for _, rule := range rules {
		if rule.match(lower) {
			return finding(traceID, rule.category, rule.severity, 0.92, rule.title,
				"Shell-like tool input matched a deterministic high-risk command pattern.",
				node, excerpt(text), d.Name(), d.Version()), true
		}
	}
	return observe.Finding{}, false
}

// CredentialDetector reports credential leakage found in a trace.
//
// It reuses the masking engine instead of carrying a second set of patterns:
// secretmask.Scan is one pass over the node text with the same compiled Ragel
// DFA the hot lane masks with, and only values the masking policy would rewrite
// are reported (Finding.Maskable, non-PII kinds). The audit and the rewrite
// therefore agree on what counts as a credential — there is no parallel regex
// implementation to drift out of sync. Personal data is the sibling
// PIIDetector's concern; the two partition the same scan by Kind.PII.
//
// Like PIIDetector, this works on the stored observation, so it also covers
// traces recorded before masking was enabled, re-analysis, and values that
// reached the provider while masking was off.
type CredentialDetector struct{}

func (CredentialDetector) Name() string    { return "credential" }
func (CredentialDetector) Version() string { return "0.2.0" }

func (d CredentialDetector) Detect(ctx context.Context, obs observe.TraceObservation) ([]observe.Finding, error) {
	var findings []observe.Finding
	for _, node := range allNodes(obs) {
		seen := map[secretmask.RuleID]bool{}
		for _, text := range nodeTexts(node) {
			if text == "" {
				continue
			}
			for _, match := range scanText(ctx, text) {
				rule, ok := match.Rule()
				if !ok || rule.Kind.PII() || !match.Maskable() || seen[match.ID] {
					continue
				}
				seen[match.ID] = true
				evidence := "A value matching " + rule.Name + " is strong enough to mask but appears in this trace."
				switch {
				case match.Reasons&secretmask.ReasonValidator != 0:
					evidence = "A value matching " + rule.Name + " passed its offline validator and appears in this trace."
				case match.Reasons&secretmask.ReasonContext != 0:
					evidence = "A value matching " + rule.Name + " appears next to matching vocabulary in this trace."
				}
				findings = append(findings, finding(obs.TraceID, "credential_leak", observe.SeverityHigh, 0.9,
					"Credential: "+rule.Name, evidence,
					node, excerptRedacted(text, match.Start, match.End), d.Name(), d.Version()))
			}
		}
	}
	return dedupeFindings(findings), nil
}

type ProviderSafetyDetector struct{}

func (ProviderSafetyDetector) Name() string    { return "provider_safety" }
func (ProviderSafetyDetector) Version() string { return "0.1.0" }

func (d ProviderSafetyDetector) Detect(_ context.Context, obs observe.TraceObservation) ([]observe.Finding, error) {
	var findings []observe.Finding
	for _, node := range allNodes(obs) {
		switch node.NormalizedType {
		case observe.NodeSafety:
			findings = append(findings, finding(obs.TraceID, "provider_safety_block", observe.SeverityMedium, 0.95, "Provider safety signal",
				"Provider response contained a structured safety or content-filter signal.",
				node, excerpt(firstNonEmpty(node.Text, rawText(node.Raw), rawText(node.JSON))), d.Name(), d.Version()))
		case observe.NodeRefusal:
			findings = append(findings, finding(obs.TraceID, "model_refusal", observe.SeverityLow, 0.95, "Model refusal",
				"Model response contained a refusal node.",
				node, excerpt(firstNonEmpty(node.Text, rawText(node.Raw), rawText(node.JSON))), d.Name(), d.Version()))
		}
	}
	if obs.Safety.Blocked {
		findings = append(findings, finding(obs.TraceID, "provider_safety_block", observe.SeverityMedium, 0.9, "Provider safety block",
			"Observation safety summary indicates the provider blocked or filtered the response.",
			observe.SemanticNode{}, "blocked=true", d.Name(), d.Version()))
	}
	if obs.Safety.Refused {
		findings = append(findings, finding(obs.TraceID, "model_refusal", observe.SeverityLow, 0.9, "Model refusal",
			"Observation safety summary indicates a model refusal.",
			observe.SemanticNode{}, "refused=true", d.Name(), d.Version()))
	}
	return dedupeFindings(findings), nil
}

type ToolErrorDetector struct{}

func (ToolErrorDetector) Name() string    { return "tool_error" }
func (ToolErrorDetector) Version() string { return "0.2.0" }

// Detect reports a tool execution failure only when the exchange itself says the
// execution failed. The previous rule also searched every tool-result text for
// the literal token `"error"`, which turned any successful command that printed
// or returned source code, a log line or a schema containing that token into a
// "Tool or provider error" finding. Tool output is arbitrary bytes and is not an
// execution-status channel; the protocol's explicit error state is.
//
// One finding is emitted per failing node. Tools.Results carries the parser's
// protocol-derived IsError flag for both request history and response items on
// Anthropic and Gemini; the node walk covers OpenAI Responses, whose
// function_call_output history items are not fed into Tools.Results, and every
// explicit provider error node.
func (d ToolErrorDetector) Detect(_ context.Context, obs observe.TraceObservation) ([]observe.Finding, error) {
	var findings []observe.Finding
	seen := make(map[string]struct{}, len(obs.Tools.Results))
	record := func(node observe.SemanticNode, confidence float64, title, description, evidence string) {
		if node.ID != "" {
			if _, ok := seen[node.ID]; ok {
				return
			}
			seen[node.ID] = struct{}{}
		}
		findings = append(findings, finding(obs.TraceID, "tool_result_error", observe.SeverityMedium, confidence, title, description,
			node, excerpt(evidence), d.Name(), d.Version()))
	}

	for _, result := range obs.Tools.Results {
		if !result.IsError {
			continue
		}
		node := findNodeByID(obs, result.NodeID)
		record(node, 0.95, "Tool result error",
			"Tool execution was marked as failed by the provider protocol.",
			firstNonEmpty(node.Text, result.Text, rawText(result.JSON)))
	}

	for _, node := range allNodes(obs) {
		if node.NormalizedType == observe.NodeError {
			record(node, 0.9, "Tool or provider error",
				"Provider output contains an explicit error node.",
				firstNonEmpty(node.Text, rawText(node.Raw), rawText(node.JSON)))
			continue
		}
		if !observe.ToolResultFailed(node) {
			continue
		}
		record(node, 0.95, "Tool result error",
			"Tool execution was marked as failed by the provider protocol.",
			firstNonEmpty(node.Text, rawText(node.Raw), rawText(node.JSON)))
	}
	return dedupeFindings(findings), nil
}

var destructiveRMPattern = regexp.MustCompile("(^|[\\s;&|:\"'])rm\\s+-[a-z]*r[a-z]*f[a-z]*\\s+(/|~)([\\s\"'`,}$]|$|[;&|])")

func finding(traceID, category string, severity observe.Severity, confidence float64, title, description string, node observe.SemanticNode, evidence, detector, version string) observe.Finding {
	f := observe.Finding{
		TraceID:         traceID,
		Category:        category,
		Severity:        severity,
		Confidence:      confidence,
		Title:           title,
		Description:     description,
		EvidenceExcerpt: evidence,
		NodeID:          node.ID,
		Detector:        detector,
		DetectorVersion: version,
		CreatedAt:       time.Now().UTC(),
	}
	if node.ID != "" || node.Path != "" {
		f.EvidencePath = observe.EvidencePath(traceID, node)
	} else {
		f.EvidencePath = "trace#" + traceID
	}
	f.ID = stableFindingID(f)
	return f
}

// stableFindingID is the finding's identity across reparses. The title carries
// the rule ("Credential: aws.access_key_id", "Personal data: national_id"), so
// two rules that hit the same node are two findings rather than one: a finding
// per node/category silently dropped every rule after the first.
func stableFindingID(f observe.Finding) string {
	key := strings.Join([]string{f.TraceID, f.Category, string(f.Severity), f.NodeID, f.EvidencePath, f.Title, f.Detector, f.DetectorVersion}, "|")
	sum := sha1.Sum([]byte(key))
	return "finding_" + hex.EncodeToString(sum[:])[:16]
}

func allNodes(obs observe.TraceObservation) []observe.SemanticNode {
	return observe.TraceNodes(obs)
}

// nodeTexts returns the text a detector scans for one node. It is the IR's
// shared view list so a detector and a derived index cannot disagree about
// which bytes a node carries.
func nodeTexts(node observe.SemanticNode) []string {
	return observe.NodeTexts(node)
}

func findNodeByID(obs observe.TraceObservation, id string) observe.SemanticNode {
	if id == "" {
		return observe.SemanticNode{}
	}
	for _, node := range allNodes(obs) {
		if node.ID == id {
			return node
		}
	}
	return observe.SemanticNode{ID: id}
}

func dedupeFindings(findings []observe.Finding) []observe.Finding {
	seen := make(map[string]struct{}, len(findings))
	out := make([]observe.Finding, 0, len(findings))
	for _, finding := range findings {
		if finding.ID == "" {
			finding.ID = stableFindingID(finding)
		}
		if _, ok := seen[finding.ID]; ok {
			continue
		}
		seen[finding.ID] = struct{}{}
		out = append(out, finding)
	}
	return out
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.RawMessage:
		return string(typed)
	case []byte:
		return string(typed)
	default:
		buf, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(buf)
	}
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return string(raw)
}

func excerpt(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= excerptLimit {
		return text
	}
	return text[:excerptLimit]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func severityRank(severity observe.Severity) int {
	switch severity {
	case observe.SeverityCritical:
		return 5
	case observe.SeverityHigh:
		return 4
	case observe.SeverityMedium:
		return 3
	case observe.SeverityLow:
		return 2
	case observe.SeverityInfo:
		return 1
	default:
		return 0
	}
}
