package stream

import (
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
)

// TestAssemblerResponseMidStreamFlushes pins the contract that Response() returns
// the live value: a reader halfway through a stream sees the deltas received so
// far, and later deltas continue the same part instead of starting a new one.
func TestAssemblerResponseMidStreamFlushes(t *testing.T) {
	assembler := NewAssembler(xir.ProtocolChatCompletions)
	apply := func(event *xir.StreamEvent) {
		t.Helper()
		if err := assembler.Apply(event); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	text := func(response *xir.Response) string {
		for _, part := range response.Parts {
			if part.Kind == xir.PartText {
				return part.Text
			}
		}
		return ""
	}

	apply(&xir.StreamEvent{Kind: xir.StreamText, Text: "hello "})
	if got := text(assembler.Response()); got != "hello " {
		t.Fatalf("mid-stream text = %q", got)
	}
	apply(&xir.StreamEvent{Kind: xir.StreamText, Text: "world"})
	if got := text(assembler.Response()); got != "hello world" {
		t.Fatalf("resumed text = %q", got)
	}

	apply(&xir.StreamEvent{Kind: xir.StreamReasoning, Reasoning: &xir.Reasoning{Text: "plan"}})
	apply(&xir.StreamEvent{Kind: xir.StreamReasoning, Reasoning: &xir.Reasoning{Text: " more"}})
	response := assembler.Response()
	var reasoning string
	for _, part := range response.Parts {
		if part.Kind == xir.PartReasoning && part.Reasoning != nil {
			reasoning = part.Reasoning.Text
		}
	}
	if reasoning != "plan more" {
		t.Fatalf("reasoning = %q", reasoning)
	}

	apply(&xir.StreamEvent{Kind: xir.StreamToolStart, ToolID: "call_1", ToolName: "read_file"})
	apply(&xir.StreamEvent{Kind: xir.StreamToolArgs, Text: `{"path":`})
	assembler.Response() // mid-arguments flush must not lose the fragment
	apply(&xir.StreamEvent{Kind: xir.StreamToolArgs, Text: `"a"}`})
	response = assembler.Response()
	var args string
	for _, part := range response.Parts {
		if part.Kind == xir.PartToolCall && part.ToolCall != nil {
			args = part.ToolCall.ArgsText
		}
	}
	if args != `{"path":"a"}` {
		t.Fatalf("tool args = %q", args)
	}
}
