package analyzer

import (
	"context"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/observe"
)

const testChineseID = "11010519491231002X"

func piiFindings(t *testing.T, obs observe.TraceObservation) []observe.Finding {
	t.Helper()
	findings, err := NewRunner().Analyze(context.Background(), obs)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	var out []observe.Finding
	for _, f := range findings {
		if f.Category == "pii_leak" {
			out = append(out, f)
		}
	}
	return out
}

func TestPIIDetectorFindsAProvenIdentityNumber(t *testing.T) {
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

	findings := piiFindings(t, obs)
	if len(findings) != 1 {
		t.Fatalf("pii findings = %d, want 1: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Severity != observe.SeverityHigh {
		t.Errorf("severity = %q, want high", f.Severity)
	}
	if f.Detector != "pii" || f.DetectorVersion == "" {
		t.Errorf("detector = %q/%q, want pii with a version", f.Detector, f.DetectorVersion)
	}
	if f.NodeID != node.ID || f.EvidencePath == "" {
		t.Errorf("finding lost its node: %+v", f)
	}
	if f.Metadata["exchange_kind"] != "entry" {
		t.Errorf("metadata exchange_kind = %v, want entry", f.Metadata["exchange_kind"])
	}
	if strings.Contains(f.EvidenceExcerpt, testChineseID) {
		t.Errorf("evidence excerpt repeats the identity number: %q", f.EvidenceExcerpt)
	}
	if !strings.Contains(f.EvidenceExcerpt, "[redacted]") {
		t.Errorf("evidence excerpt does not mark the removed value: %q", f.EvidenceExcerpt)
	}
	if !strings.Contains(f.EvidenceExcerpt, "身份证号") {
		t.Errorf("evidence excerpt lost the surrounding context: %q", f.EvidenceExcerpt)
	}
}

// TestPIIDetectorIgnoresUnprovenNumbers pins the precision contract: the
// detector only reports values the masking engine's validator proved. An order
// number, a nanosecond timestamp and a seventeen-digit prefix are not personal
// data, and a finding is an accusation.
func TestPIIDetectorIgnoresUnprovenNumbers(t *testing.T) {
	texts := []string{
		"订单号 123456789012345678",
		"时间 1700000000000000000",
		"身份证 11010519491231002",
		"身份证 110105194912310021",
		"流水 711010519491231002X",
	}
	for _, text := range texts {
		node := observe.SemanticNode{ID: "node", NormalizedType: observe.NodeText, Text: text}
		obs := observe.TraceObservation{
			TraceID: "trace-pii-negative",
			Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}},
		}
		if findings := piiFindings(t, obs); len(findings) != 0 {
			t.Errorf("%q produced %+v, want no PII finding", text, findings)
		}
	}
}

// TestPIIDetectorReportsOneFindingPerNode covers a node that carries the same
// class twice: the finding is per node and class, so a reviewer sees one entry
// per place the data appears rather than one per occurrence.
func TestPIIDetectorReportsOneFindingPerNode(t *testing.T) {
	other := "440524188001010014"
	node := observe.SemanticNode{
		ID:             "node-many",
		NormalizedType: observe.NodeText,
		Text:           "主号 " + testChineseID + " 备用 " + other,
	}
	obs := observe.TraceObservation{
		TraceID: "trace-pii-many",
		Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}},
	}
	if findings := piiFindings(t, obs); len(findings) != 1 {
		t.Fatalf("pii findings = %d, want 1: %+v", len(findings), findings)
	}
}

// TestPIIDetectorHoldsOnAModelExchange makes sure the detector is not limited to
// the entry exchange: a model response that echoes a number is worth a finding
// too, which is the scope rule the other response detectors use.
func TestPIIDetectorHoldsOnAModelExchange(t *testing.T) {
	node := observe.SemanticNode{ID: "node-out", NormalizedType: observe.NodeText, Text: "号码 " + testChineseID}
	obs := observe.TraceObservation{
		TraceID:      "trace-pii-model",
		ExchangeKind: "model",
		Response:     observe.ObservationResponse{Nodes: []observe.SemanticNode{node}},
	}
	if findings := piiFindings(t, obs); len(findings) != 1 {
		t.Fatalf("pii findings = %d, want 1", len(findings))
	}
}

// TestPIIDetectorReadsToolArguments covers the source that is not node.Text: a
// tool call carries its arguments in metadata, and the credential detector has
// always scanned them. A number in a tool call is the shape most likely to
// reach a downstream service, so the PII detector has to see it too.
func TestPIIDetectorReadsToolArguments(t *testing.T) {
	node := observe.SemanticNode{
		ID:             "node-tool-args",
		ProviderType:   "function_call",
		NormalizedType: observe.NodeToolCall,
		Path:           "$.output[0]",
		Metadata:       map[string]any{"arguments": `{"id":"` + testChineseID + `"}`},
	}
	obs := observe.TraceObservation{
		TraceID:      "trace-pii-tool",
		ExchangeKind: "model",
		Response:     observe.ObservationResponse{Nodes: []observe.SemanticNode{node}},
	}
	findings := piiFindings(t, obs)
	if len(findings) != 1 {
		t.Fatalf("pii findings = %d, want 1: %+v", len(findings), findings)
	}
	if strings.Contains(findings[0].EvidenceExcerpt, testChineseID) {
		t.Errorf("evidence excerpt repeats the identity number: %q", findings[0].EvidenceExcerpt)
	}
}
