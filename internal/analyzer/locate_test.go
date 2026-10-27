package analyzer

import (
	"context"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/observe"
)

func findingsByCategory(t *testing.T, obs observe.TraceObservation) map[string]observe.Finding {
	t.Helper()
	findings, err := NewRunner().Analyze(context.Background(), obs)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	out := make(map[string]observe.Finding, len(findings))
	for _, finding := range findings {
		out[finding.Category] = finding
	}
	return out
}

// TestLocateFindingReturnsThePIIValue pins the read-time highlight for the one
// finding whose stored excerpt deliberately hides the value: the match is
// recomputed from the observation, so the number can be marked without the
// findings table becoming a second copy of it.
func TestLocateFindingReturnsThePIIValue(t *testing.T) {
	node := observe.SemanticNode{
		ID:             "node-pii",
		NormalizedType: observe.NodeText,
		Path:           "$.messages[0].content",
		Text:           "我的身份证号是 " + testChineseID + "，请帮我核对。",
	}
	obs := observe.TraceObservation{
		TraceID:      "trace-pii",
		ExchangeKind: "entry",
		Request:      observe.ObservationRequest{Nodes: []observe.SemanticNode{node}},
	}
	findings := findingsByCategory(t, obs)
	finding, ok := findings["pii_leak"]
	if !ok {
		t.Fatalf("no pii finding: %+v", findings)
	}
	match, ok := LocateFinding(obs, finding)
	if !ok {
		t.Fatalf("LocateFinding() found no match for %+v", finding)
	}
	if match.Text != testChineseID {
		t.Errorf("match text = %q, want the identity number", match.Text)
	}
	if match.NodeID != node.ID || match.NodePath != node.Path || match.View != "text" {
		t.Errorf("match = %+v, want the node and its text view", match)
	}
	if match.Start < 0 || match.End <= match.Start || match.End > len(node.Text) {
		t.Errorf("match span = %d..%d, want a valid span in the text", match.Start, match.End)
	}
}

func TestLocateFindingReturnsACredential(t *testing.T) {
	key := "sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20)
	node := observe.SemanticNode{
		ID:             "node-key",
		NormalizedType: observe.NodeText,
		Path:           "$.messages[1].content",
		Text:           "use " + key + " for this call",
	}
	obs := observe.TraceObservation{
		TraceID: "trace-key",
		Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}},
	}
	finding, ok := findingsByCategory(t, obs)["credential_leak"]
	if !ok {
		t.Fatalf("no credential finding")
	}
	match, ok := LocateFinding(obs, finding)
	if !ok || match.Text != key {
		t.Fatalf("LocateFinding() = %+v, %v, want the key", match, ok)
	}
}

func TestLocateFindingReturnsTheShellMatch(t *testing.T) {
	node := observe.SemanticNode{
		ID:             "node-shell",
		NormalizedType: observe.NodeToolCall,
		Path:           "$.messages[0].tool_calls[0]",
		Text:           "sudo rm -rf /tmp/build",
	}
	obs := observe.TraceObservation{
		TraceID: "trace-shell",
		Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}},
	}
	finding, ok := findingsByCategory(t, obs)["filesystem_destructive_operation"]
	if !ok {
		t.Fatalf("no shell finding")
	}
	match, ok := LocateFinding(obs, finding)
	if !ok || match.Text == "" {
		t.Fatalf("LocateFinding() = %+v, %v, want a shell span", match, ok)
	}
	if !strings.Contains(strings.ToLower(match.Text), "rm") {
		t.Errorf("match text = %q, want the destructive command", match.Text)
	}
}

func TestLocateFindingReportsNoSpanForAWholeNodeJudgement(t *testing.T) {
	node := observe.SemanticNode{
		ID:             "node-error",
		NormalizedType: observe.NodeError,
		Path:           "$.error",
		Text:           "upstream unavailable",
	}
	obs := observe.TraceObservation{
		TraceID: "trace-error",
		Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}},
	}
	finding, ok := findingsByCategory(t, obs)["tool_result_error"]
	if !ok {
		t.Fatalf("no tool error finding")
	}
	if match, ok := LocateFinding(obs, finding); ok {
		t.Errorf("LocateFinding() = %+v, want no span for a whole-node finding", match)
	}
	if _, ok := LocateFinding(obs, observe.Finding{Category: "pii_leak", NodeID: "missing"}); ok {
		t.Errorf("LocateFinding() found a missing node")
	}
}
