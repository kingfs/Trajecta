package observe

import (
	"encoding/json"
	"testing"
)

func TestToolResultFailedReadsOnlyStructuredState(t *testing.T) {
	toolResult := func(text string, metadata map[string]any) SemanticNode {
		return SemanticNode{
			ID:             "node-tool-result",
			ProviderType:   "function_call_output",
			NormalizedType: NodeToolResult,
			Text:           text,
			Metadata:       metadata,
		}
	}
	codeResult := func(outcome string) SemanticNode {
		raw, err := json.Marshal(map[string]string{"outcome": outcome})
		if err != nil {
			t.Fatalf("marshal outcome: %v", err)
		}
		return SemanticNode{
			ID:             "node-code-result",
			ProviderType:   "codeExecutionResult",
			NormalizedType: NodeCodeResult,
			Metadata:       map[string]any{"codeExecutionResult": json.RawMessage(raw)},
		}
	}

	cases := []struct {
		name string
		node SemanticNode
		want bool
	}{
		{
			name: "anthropic is_error true",
			node: toolResult("lookup failed", map[string]any{"is_error": "true"}),
			want: true,
		},
		{
			name: "anthropic is_error false",
			node: toolResult("lookup ok", map[string]any{"is_error": "false"}),
			want: false,
		},
		{
			name: "boolean is_error true",
			node: toolResult("lookup failed", map[string]any{"is_error": true}),
			want: true,
		},
		{
			name: "failed status",
			node: toolResult(`{"cwd":"/tmp"}`, map[string]any{"status": "failed"}),
			want: true,
		},
		{
			name: "error status",
			node: toolResult(`{"cwd":"/tmp"}`, map[string]any{"status": "error"}),
			want: true,
		},
		{
			name: "completed status with error token in output",
			node: toolResult("+ \"sandbox_id\", s.sandboxID, \"error\", err)", map[string]any{"status": "completed"}),
			want: false,
		},
		{
			name: "no status and error token in output",
			node: toolResult("diff --git a/x.go b/x.go\n+ \"error\", err", nil),
			want: false,
		},
		{
			name: "explicit error part type",
			node: SemanticNode{NormalizedType: NodeToolResult, ProviderType: "web_fetch_tool_error"},
			want: true,
		},
		{
			name: "provider error node is not a tool result node",
			node: SemanticNode{NormalizedType: NodeError, ProviderType: "error", Text: "overloaded"},
			want: false,
		},
		{
			name: "gemini code execution ok",
			node: codeResult("OUTCOME_OK"),
			want: false,
		},
		{
			name: "gemini code execution failed",
			node: codeResult("OUTCOME_FAILED"),
			want: true,
		},
		{
			name: "gemini code execution deadline exceeded",
			node: codeResult("OUTCOME_DEADLINE_EXCEEDED"),
			want: true,
		},
		{
			name: "non tool node",
			node: SemanticNode{NormalizedType: NodeText, Text: `{"error":true}`},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolResultFailed(tc.node); got != tc.want {
				t.Errorf("ToolResultFailed() = %v, want %v for %+v", got, tc.want, tc.node)
			}
		})
	}
}
