package analyzer

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// The locate half of the analyzer: a finding says what was wrong and in which
// node, but not which bytes inside that node proved it. The stored excerpt is
// redacted for personal data and the offsets belong to one text view, so the
// match is recomputed on demand from the same observation and returned as the
// exact text the reader should see highlighted.
//
// Findings are not changed by this: the findings table keeps its redacted
// excerpt and carries no match column. The value is returned to an
// authenticated Monitor session for the length of one read, which is the same
// content the trace view already shows.
type FindingMatch struct {
	NodeID   string
	NodePath string
	View     string
	Start    int
	End      int
	Text     string
}

// nodeTextViews is the same view list the detectors scan, with the label the
// locate response reports. observe.NodeTexts returns text, arguments, raw and
// json in this order.
var nodeTextViews = [...]string{"text", "arguments", "raw", "json"}

// The shell detector reuses the credential_leak category for a command that
// reads a credential file, so the detector name is what tells the two apart.
var dangerousShellDetectorName = DangerousShellDetector{}.Name()

// LocateFinding finds the bytes one finding was proven by. It reports false for
// a finding whose category has no single span - a safety block or a tool error
// is a whole-node judgement - and for a node the current parse no longer has.
func LocateFinding(obs observe.TraceObservation, finding observe.Finding) (FindingMatch, bool) {
	node := findNodeByID(obs, finding.NodeID)
	if node.ID == "" {
		return FindingMatch{}, false
	}
	texts := nodeTexts(node)
	for index, text := range texts {
		if text == "" {
			continue
		}
		start, end, ok := locateInText(text, finding)
		if !ok {
			continue
		}
		view := "text"
		if index < len(nodeTextViews) {
			view = nodeTextViews[index]
		}
		return FindingMatch{
			NodeID:   node.ID,
			NodePath: node.Path,
			View:     view,
			Start:    start,
			End:      end,
			Text:     text[start:end],
		}, true
	}
	return FindingMatch{}, false
}

func locateInText(text string, finding observe.Finding) (int, int, bool) {
	switch finding.Category {
	case "pii_leak":
		return locatePII(text, finding)
	case "credential_leak":
		if finding.Detector == dangerousShellDetectorName {
			return locateLiteral(text, ".ssh/id_rsa", ".aws/credentials", "security find-generic-password", "env | curl")
		}
		return locateCredential(text)
	case "filesystem_destructive_operation":
		if loc := destructiveRMPattern.FindStringIndex(strings.ToLower(text)); loc != nil {
			return loc[0], loc[1], true
		}
		return locateLiteral(text, "sudo rm", "mkfs", "diskutil erase", "dd if=")
	case "dangerous_command":
		return locateLiteral(text, "chmod -r 777", "chown -r", "nc -e", "bash -i")
	case "unsafe_code_execution":
		if loc := shellPipePattern.FindStringIndex(text); loc != nil {
			return loc[0], loc[1], true
		}
		return locateLiteral(text, "| sh", "| bash")
	}
	return 0, 0, false
}

func locatePII(text string, finding observe.Finding) (int, int, bool) {
	kind := strings.TrimPrefix(strings.TrimSpace(finding.Title), "Personal data: ")
	kind = strings.TrimSpace(kind)
	for _, match := range secretmask.Scan([]byte(text)) {
		rule, ok := match.Rule()
		if !ok || !rule.Kind.PII() || !match.Maskable() {
			continue
		}
		if kind != "" && rule.Kind.String() != kind {
			continue
		}
		return match.Start, match.End, true
	}
	return 0, 0, false
}

func locateCredential(text string) (int, int, bool) {
	for _, pattern := range credentialPatterns {
		if loc := pattern.re.FindStringIndex(text); loc != nil {
			return loc[0], loc[1], true
		}
	}
	// The credentialed-URL rule has no fixed pattern: it parses the text and
	// looks at the userinfo. The narrow URL scan recovers the bytes to mark
	// without returning the whole node.
	for _, loc := range credentialURLPattern.FindAllStringIndex(text, -1) {
		candidate := text[loc[0]:loc[1]]
		parsed, err := url.Parse(candidate)
		if err == nil && parsed.User != nil && parsed.Scheme != "" && parsed.Host != "" {
			return loc[0], loc[1], true
		}
	}
	return 0, 0, false
}

// credentialURLPattern is the substring shape of a URL; credentialMatch parses
// a whole text, while a node may carry a URL inside a sentence.
var credentialURLPattern = regexp.MustCompile(`(?i)[a-z][a-z0-9+.\-]*://[^\s"'<>]+`)

var shellPipePattern = regexp.MustCompile(`(?i)(curl|wget)[^\n|]*\|\s*(sh|bash)`)

// locateLiteral returns the first of the lower-case literals found in text.
// The offsets are byte offsets; the literals are ASCII, so lowering the text
// cannot change the prefix length for the languages this console records.
func locateLiteral(text string, literals ...string) (int, int, bool) {
	lower := strings.ToLower(text)
	for _, literal := range literals {
		if index := strings.Index(lower, literal); index >= 0 {
			return index, index + len(literal), true
		}
	}
	return 0, 0, false
}
