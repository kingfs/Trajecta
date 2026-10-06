package runtime

import (
	"encoding/json"
	"testing"

	"github.com/kingfs/Trajecta/internal/responses/protocol"
)

// TestLedgerToChatMessagesMergesAssistantTextAndToolCalls pins the reconstructed
// conversation for a client-side function call. A stored turn that carries both
// assistant text and a function call used to become two consecutive assistant
// messages, which put the `tool` result after an assistant message that did not
// request it. A strict upstream rejects that shape with `invalid_request_error`,
// so the whole follow-up turn failed whenever the model narrated the call.
func TestLedgerToChatMessagesMergesAssistantTextAndToolCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		// first selects the ledger order: the native API may report the text
		// before or after the call within the same assistant turn.
		textFirst bool
	}{
		{name: "call then text", textFirst: false},
		{name: "text then call", textFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := protocol.OutputItem{
				Type:    "message",
				Role:    "assistant",
				Content: []protocol.ContentPart{{Type: "output_text", Text: "I'll check the weather."}},
			}
			call := protocol.OutputItem{
				Type:      "function_call",
				CallID:    "call_1",
				Name:      "get_weather",
				Arguments: `{"city":"Shanghai"}`,
			}
			ledger := []LedgerItem{{Output: &call}, {Output: &text}}
			if tc.textFirst {
				ledger = []LedgerItem{{Output: &text}, {Output: &call}}
			}

			messages := ledgerToChatMessages(ledger)
			if len(messages) != 1 {
				t.Fatalf("messages = %d, want 1 merged assistant message: %s", len(messages), dumpMessages(t, messages))
			}
			got := messages[0]
			if got.Role != "assistant" {
				t.Fatalf("role = %q, want assistant", got.Role)
			}
			if got.Content != "I'll check the weather." {
				t.Fatalf("content = %q, want the assistant text", got.Content)
			}
			if len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "call_1" {
				t.Fatalf("tool_calls = %+v, want the get_weather call", got.ToolCalls)
			}
		})
	}
}

// TestLedgerToChatMessagesKeepsTheToolResultAfterItsCall pins the ordering rule
// the fix exists for: the `tool` message must directly follow the assistant
// message carrying the call it answers.
func TestLedgerToChatMessagesKeepsTheToolResultAfterItsCall(t *testing.T) {
	call := protocol.OutputItem{Type: "function_call", CallID: "call_1", Name: "get_weather", Arguments: "{}"}
	text := protocol.OutputItem{Type: "message", Role: "assistant", Content: []protocol.ContentPart{{Type: "output_text", Text: "checking"}}}
	output := protocol.OutputItem{Type: "function_call_output", CallID: "call_1", Output: "sunny, 25C"}

	messages := ledgerToChatMessages([]LedgerItem{{Output: &text}, {Output: &call}, {Output: &output}})
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want assistant + tool: %s", len(messages), dumpMessages(t, messages))
	}
	if messages[0].Role != "assistant" || len(messages[0].ToolCalls) != 1 {
		t.Fatalf("first message = %+v, want the assistant call", messages[0])
	}
	if messages[1].Role != "tool" || messages[1].ToolCallID != "call_1" {
		t.Fatalf("second message = %+v, want the matching tool result", messages[1])
	}
}

func dumpMessages(t *testing.T, messages []ChatMessage) string {
	t.Helper()

	raw, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return string(raw)
}
