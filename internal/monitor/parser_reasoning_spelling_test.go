package monitor

import "testing"

// TestTraceViewKeepsBothReasoningSpellings pins the trace-view half of the reasoning-field fix.
//
// The Monitor renders the recorded response itself, and its chat-completions reader only looked at
// `reasoning_content` while the observation parser (pkg/observe) accepted `reasoning` too. A trace from
// an OpenRouter-style upstream therefore showed an empty thinking block in the UI even though the
// semantic observation derived from the same bytes carried the text.
func TestTraceViewKeepsBothReasoningSpellings(t *testing.T) {
	nonStream := []byte(`{"choices":[{"message":{"role":"assistant","content":"answer","reasoning":"router"}}]}`)
	content, reasoning, toolCalls := parseChatCompletionsOutput(nonStream, false)
	if content != "answer" {
		t.Fatalf("non-stream content = %q, want %q", content, "answer")
	}
	if reasoning != "router" {
		t.Fatalf("non-stream reasoning = %q, want %q from the `reasoning` field", reasoning, "router")
	}
	if len(toolCalls) != 0 {
		t.Fatalf("non-stream tool calls = %d, want 0", len(toolCalls))
	}

	stream := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"deep\"}}]}\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"router\"}}]}\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"}}]}\n" +
		"data: [DONE]\n")
	content, reasoning, _ = parseChatCompletionsOutput(stream, true)
	if content != "answer" {
		t.Fatalf("stream content = %q, want %q", content, "answer")
	}
	if reasoning != "deeprouter" {
		t.Fatalf("stream reasoning = %q, want %q: both spellings must reach the trace view", reasoning, "deeprouter")
	}

	// `reasoning_content` still wins when both are present, which is what pkg/observe does as well.
	both := []byte(`{"choices":[{"message":{"content":"answer","reasoning_content":"deep","reasoning":"router"}}]}`)
	if _, reasoning, _ = parseChatCompletionsOutput(both, false); reasoning != "deep" {
		t.Fatalf("reasoning with both spellings present = %q, want the `reasoning_content` value", reasoning)
	}
}
