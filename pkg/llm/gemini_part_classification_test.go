package llm

import (
	"testing"
)

// geminiThoughtAndCallStream is a Gemini/Vertex stream (`:streamGenerateContent?alt=sse`) that carries
// one reasoning part, one complete function call and one answer part.
const geminiThoughtAndCallStream = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"weighing options","thought":true}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"It is 22C."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":4,"totalTokenCount":13}}

`

// TestGeminiPartsKeepTheirKind pins the Gemini classification in the parser and the recorder.
//
// A Gemini part is `text`, `thought: true` (internal reasoning) or `functionCall` (a whole tool call in
// one part). The observation parser already maps those to reasoning and tool-call nodes, while the
// parse and record paths only looked at `text`: reasoning was folded into the answer, and a function
// call was dropped entirely - so the parsed response and the cassette described a different response
// than the observation derived from the same bytes.
func TestGeminiPartsKeepTheirKind(t *testing.T) {
	parsed, err := ParseStreamResponse(ProviderGoogleGenAI, "/v1beta/models:streamGenerateContent", []byte(geminiThoughtAndCallStream))
	if err != nil {
		t.Fatalf("ParseStreamResponse() error = %v", err)
	}
	if len(parsed.Candidates) != 1 {
		t.Fatalf("ParseStreamResponse() candidates = %d, want 1", len(parsed.Candidates))
	}
	candidate := parsed.Candidates[0]
	text := ""
	reasoning := ""
	for _, part := range candidate.Content {
		switch part.Type {
		case "text":
			text += part.Text
		case "thinking":
			reasoning += part.Text
		}
	}
	if text != "It is 22C." {
		t.Fatalf("parsed text = %q, want %q: thought parts are not answer text", text, "It is 22C.")
	}
	if reasoning != "weighing options" {
		t.Fatalf("parsed reasoning = %q, want %q", reasoning, "weighing options")
	}
	if len(candidate.ToolCalls) != 1 {
		t.Fatalf("parsed tool calls = %+v, want the functionCall part", candidate.ToolCalls)
	}
	if candidate.ToolCalls[0].Name != "get_weather" {
		t.Fatalf("parsed tool call name = %q, want get_weather", candidate.ToolCalls[0].Name)
	}
	if got := candidate.ToolCalls[0].ArgsText; got != `{"city":"Paris"}` {
		t.Fatalf("parsed tool call arguments = %q, want the compact argument object", got)
	}

	// The non-stream mapper sees the same parts, so it has to classify them the same way.
	nonStream := GeminiToLLM(GeminiResponse{Candidates: []GeminiCandidate{{
		Content: GeminiContent{Role: "model", Parts: []GeminiPart{
			{Text: "weighing options", Thought: true},
			{FunctionCall: &GeminiFunctionCall{Name: "get_weather", Args: []byte(`{"city":"Paris"}`)}},
			{Text: "It is 22C."},
		}},
		FinishReason: "STOP",
	}}})
	if len(nonStream.Candidates) != 1 {
		t.Fatalf("GeminiToLLM() candidates = %d, want 1", len(nonStream.Candidates))
	}
	nonStreamText := ""
	nonStreamReasoning := ""
	for _, part := range nonStream.Candidates[0].Content {
		switch part.Type {
		case "text":
			nonStreamText += part.Text
		case "thinking":
			nonStreamReasoning += part.Text
		}
	}
	if nonStreamText != text || nonStreamReasoning != reasoning {
		t.Fatalf("GeminiToLLM() text/reasoning = %q/%q, want %q/%q: the two Gemini readers must agree",
			nonStreamText, nonStreamReasoning, text, reasoning)
	}
	if len(nonStream.Candidates[0].ToolCalls) != 1 || nonStream.Candidates[0].ToolCalls[0].ArgsText != `{"city":"Paris"}` {
		t.Fatalf("GeminiToLLM() tool calls = %+v, want the functionCall part", nonStream.Candidates[0].ToolCalls)
	}
}

// TestRecorderKeepsGeminiThoughtAndCall covers the writer half: the cassette timeline has to name the
// same kinds the parsed response and the observation do.
func TestRecorderKeepsGeminiThoughtAndCall(t *testing.T) {
	pipeline := NewResponsePipeline(ProviderGoogleGenAI, "/v1beta/models:streamGenerateContent", true)
	pipeline.Feed([]byte(geminiThoughtAndCallStream))
	pipeline.Finalize()

	var reasoning, text string
	toolCalls := 0
	for _, event := range pipeline.Events() {
		switch event.Type {
		case "llm.reasoning.delta":
			reasoning += event.Message
		case "llm.output_text.delta":
			text += event.Message
		case "llm.tool_call.delta":
			toolCalls++
			if event.Attributes["name"] != "get_weather" {
				t.Fatalf("recorded tool call = %+v, want name get_weather", event.Attributes)
			}
		}
	}
	if reasoning != "weighing options" {
		t.Fatalf("recorded reasoning = %q, want %q: thought parts are reasoning, not output text", reasoning, "weighing options")
	}
	if text != "It is 22C." {
		t.Fatalf("recorded output text = %q, want %q", text, "It is 22C.")
	}
	if toolCalls != 1 {
		t.Fatalf("recorded tool call events = %d, want 1: a functionCall part must reach the cassette", toolCalls)
	}
}
