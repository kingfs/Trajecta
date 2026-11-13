package analyzer

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"testing"

	"github.com/kingfs/Trajecta/pkg/observe"
)

func TestDefaultDetectorsFindAuditSignals(t *testing.T) {
	toolCall := observe.SemanticNode{
		ID:             "node-shell",
		ProviderType:   "local_shell_call",
		NormalizedType: observe.NodeToolCall,
		Path:           "$.output[0]",
		Text:           `{"cmd":"rm -rf /"}`,
		Metadata:       map[string]any{"arguments": `{"cmd":"rm -rf /"}`},
	}
	secret := observe.SemanticNode{
		ID:             "node-secret",
		ProviderType:   "output_text",
		NormalizedType: observe.NodeText,
		Path:           "$.output[1].content[0]",
		Text:           "aws access key AKIAIOSFODNN7EXAMPLE",
	}
	refusal := observe.SemanticNode{
		ID:             "node-refusal",
		ProviderType:   "refusal",
		NormalizedType: observe.NodeRefusal,
		Path:           "$.output[2].content[0]",
		Text:           "I can't help with that.",
	}
	errorResult := observe.SemanticNode{
		ID:             "node-error",
		ProviderType:   "function_call_output",
		NormalizedType: observe.NodeToolResult,
		Path:           "$.output[3]",
		Text:           `{"error":"failed"}`,
		Metadata:       map[string]any{"status": "error"},
	}
	obs := observe.TraceObservation{
		TraceID: "trace-audit",
		Response: observe.ObservationResponse{
			Nodes: []observe.SemanticNode{toolCall, secret, refusal, errorResult},
		},
		Tools: observe.ObservationTools{
			Calls: []observe.ToolCallObservation{{
				ID:       "call-shell",
				Kind:     "local_shell_call",
				ArgsText: `{"cmd":"rm -rf /"}`,
				NodeID:   "node-shell",
			}},
			Results: []observe.ToolResultObservation{{
				ID:      "call-error",
				Kind:    "function_call_output",
				Text:    `{"error":"failed"}`,
				NodeID:  "node-error",
				IsError: true,
			}},
		},
	}

	findings, err := NewRunner().Analyze(context.Background(), obs)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	want := map[string]bool{
		"filesystem_destructive_operation": false,
		"credential_leak":                  false,
		"model_refusal":                    false,
		"tool_result_error":                false,
	}
	for _, finding := range findings {
		if _, ok := want[finding.Category]; ok {
			want[finding.Category] = true
			if finding.ID == "" || finding.EvidencePath == "" || finding.Detector == "" || finding.DetectorVersion == "" {
				t.Fatalf("incomplete finding = %+v", finding)
			}
			if finding.NodeID == "" {
				t.Fatalf("finding missing node id = %+v", finding)
			}
		}
	}
	for category, found := range want {
		if !found {
			t.Fatalf("missing finding category %q in %+v", category, findings)
		}
	}
}

func TestAnalyzeLegacyObservationUsesModelScope(t *testing.T) {
	toolCall := observe.SemanticNode{
		ID:             "node-shell",
		ProviderType:   "local_shell_call",
		NormalizedType: observe.NodeToolCall,
		Path:           "$.output[0]",
		Metadata:       map[string]any{"arguments": `{"cmd":"rm -rf /"}`},
	}
	obs := observe.TraceObservation{
		TraceID: "trace-legacy-model",
		Response: observe.ObservationResponse{
			Nodes: []observe.SemanticNode{toolCall},
		},
	}

	findings, err := NewRunner().Analyze(context.Background(), obs)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	finding := requireFindingCategory(t, findings, "filesystem_destructive_operation")
	if got := metadataStringValue(finding.Metadata, "exchange_kind"); got != "model" {
		t.Fatalf("exchange_kind metadata = %q, want model", got)
	}
	if got := metadataStringValue(finding.Metadata, "exchange_role"); got != "legacy_model" {
		t.Fatalf("exchange_role metadata = %q, want legacy_model", got)
	}
}

func TestAnalyzeEntryObservationScopesFindingsAndSkipsModelToolDetectors(t *testing.T) {
	toolCall := observe.SemanticNode{
		ID:             "node-entry-shell",
		ProviderType:   "local_shell_call",
		NormalizedType: observe.NodeToolCall,
		Path:           "$.response.output[0]",
		Metadata:       map[string]any{"arguments": `{"cmd":"rm -rf /"}`},
	}
	secret := observe.SemanticNode{
		ID:             "node-entry-secret",
		ProviderType:   "input_text",
		NormalizedType: observe.NodeText,
		Path:           "$.request.input",
		Text:           "aws access key AKIAIOSFODNN7EXAMPLE",
	}
	obs := observe.TraceObservation{
		TraceID:      "trace-entry",
		ExchangeKind: "entry",
		ExchangeRole: "client_request",
		Request: observe.ObservationRequest{
			Nodes: []observe.SemanticNode{secret},
		},
		Response: observe.ObservationResponse{
			Nodes: []observe.SemanticNode{toolCall},
		},
	}

	findings, err := NewRunner().Analyze(context.Background(), obs)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if hasFindingCategory(findings, "filesystem_destructive_operation") {
		t.Fatalf("entry observation produced model tool finding: %+v", findings)
	}
	finding := requireFindingCategory(t, findings, "credential_leak")
	if got := metadataStringValue(finding.Metadata, "exchange_kind"); got != "entry" {
		t.Fatalf("exchange_kind metadata = %q, want entry", got)
	}
	if got := metadataStringValue(finding.Metadata, "exchange_role"); got != "client_request" {
		t.Fatalf("exchange_role metadata = %q, want client_request", got)
	}
	scope, ok := finding.Metadata["exchange_scope"].(map[string]any)
	if !ok {
		t.Fatalf("exchange_scope metadata missing or wrong type: %+v", finding.Metadata)
	}
	if got := metadataStringValue(scope, "kind"); got != "entry" {
		t.Fatalf("exchange_scope.kind = %q, want entry", got)
	}
}

func requireFindingCategory(t *testing.T, findings []observe.Finding, category string) observe.Finding {
	t.Helper()
	for _, finding := range findings {
		if finding.Category == category {
			return finding
		}
	}
	t.Fatalf("missing finding category %q in %+v", category, findings)
	return observe.Finding{}
}

func hasFindingCategory(findings []observe.Finding, category string) bool {
	for _, finding := range findings {
		if finding.Category == category {
			return true
		}
	}
	return false
}

func metadataStringValue(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return value
}

// TestToolErrorDetectorIgnoresErrorTokenInSuccessfulOutput is the regression for
// the recorded trace that reported a successful `git diff` as a tool error: the
// diff contained a slog key `"error"` in source code, and the old rule searched
// the output text for that literal token. Tool output is data, not execution
// status.
func TestToolErrorDetectorIgnoresErrorTokenInSuccessfulOutput(t *testing.T) {
	node := observe.SemanticNode{
		ID:             "node-diff",
		ProviderType:   "function_call_output",
		NormalizedType: observe.NodeToolResult,
		Path:           "$.input[220]",
		Text:           " pkg/sandboxinit/egress_linux.go | 2 +-\n+ \"sandbox_id\", s.sandboxID, \"error\", err)",
	}
	obs := observe.TraceObservation{
		TraceID: "trace-tool-error-text",
		Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{node}},
	}

	findings, err := NewRunner(ToolErrorDetector{}).Analyze(context.Background(), obs)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("successful output produced tool error findings: %+v", findings)
	}
}

func TestToolErrorDetectorReportsOnlyStructuredFailures(t *testing.T) {
	toolResult := func(id, text string, metadata map[string]any) observe.SemanticNode {
		return observe.SemanticNode{
			ID:             id,
			ProviderType:   "function_call_output",
			NormalizedType: observe.NodeToolResult,
			Path:           "$.input[0]",
			Text:           text,
			Metadata:       metadata,
		}
	}
	codeResult := func(id, outcome string) observe.SemanticNode {
		raw, err := json.Marshal(map[string]string{"outcome": outcome})
		if err != nil {
			t.Fatalf("marshal outcome: %v", err)
		}
		return observe.SemanticNode{
			ID:             id,
			ProviderType:   "codeExecutionResult",
			NormalizedType: observe.NodeCodeResult,
			Path:           "$.output[0]",
			Metadata:       map[string]any{"codeExecutionResult": json.RawMessage(raw)},
		}
	}

	cases := []struct {
		name string
		obs  observe.TraceObservation
		want []string
	}{
		{
			name: "request-side failed status",
			obs: observe.TraceObservation{
				TraceID: "trace-failed-status",
				Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{
					toolResult("node-failed", `{"cwd":"/tmp"}`, map[string]any{"status": "failed"}),
				}},
			},
			want: []string{"node-failed"},
		},
		{
			name: "anthropic is_error flag",
			obs: observe.TraceObservation{
				TraceID: "trace-is-error",
				Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{
					toolResult("node-is-error", "boom", map[string]any{"is_error": "true"}),
				}},
			},
			want: []string{"node-is-error"},
		},
		{
			name: "provider error node",
			obs: observe.TraceObservation{
				TraceID: "trace-provider-error",
				Response: observe.ObservationResponse{Nodes: []observe.SemanticNode{{
					ID:             "node-provider",
					ProviderType:   "error",
					NormalizedType: observe.NodeError,
					Path:           "$.error",
					Text:           "overloaded",
				}}},
			},
			want: []string{"node-provider"},
		},
		{
			name: "gemini code execution failure",
			obs: observe.TraceObservation{
				TraceID: "trace-code-failed",
				Response: observe.ObservationResponse{Nodes: []observe.SemanticNode{
					codeResult("node-code", "OUTCOME_FAILED"),
				}},
			},
			want: []string{"node-code"},
		},
		{
			name: "result flag and node flag emit one finding",
			obs: observe.TraceObservation{
				TraceID: "trace-dedupe",
				Response: observe.ObservationResponse{Nodes: []observe.SemanticNode{
					toolResult("node-dedupe", "boom", map[string]any{"status": "failed"}),
				}},
				Tools: observe.ObservationTools{Results: []observe.ToolResultObservation{{
					ID:      "call-dedupe",
					NodeID:  "node-dedupe",
					Text:    "boom",
					IsError: true,
				}}},
			},
			want: []string{"node-dedupe"},
		},
		{
			name: "result flag without a parsed node",
			obs: observe.TraceObservation{
				TraceID: "trace-missing-node",
				Tools: observe.ObservationTools{Results: []observe.ToolResultObservation{{
					ID:      "call-missing",
					NodeID:  "node-missing",
					Text:    "boom",
					IsError: true,
				}}},
			},
			want: []string{"node-missing"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings, err := NewRunner(ToolErrorDetector{}).Analyze(context.Background(), tc.obs)
			if err != nil {
				t.Fatalf("Analyze() error = %v", err)
			}
			got := make([]string, 0, len(findings))
			for _, finding := range findings {
				got = append(got, finding.NodeID)
			}
			sort.Strings(got)
			want := slices.Clone(tc.want)
			sort.Strings(want)
			if !slices.Equal(got, want) {
				t.Fatalf("finding nodes = %v, want %v (findings=%+v)", got, want, findings)
			}
		})
	}
}
