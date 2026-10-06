package observe

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/pkg/llm"
)

// geminiKindBody carries the three kinds of Gemini response part: answer text, a `thought: true`
// reasoning part and a complete `functionCall` part.
const geminiKindBody = `{
	"candidates":[{
		"index":0,
		"content":{"role":"model","parts":[
			{"text":"weighing options","thought":true},
			{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}},
			{"text":"It is 22C."}
		]},
		"finishReason":"STOP"
	}],
	"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":4,"totalTokenCount":13}
}`

// TestGeminiPartKindsAgreeAcrossParsers measures the observer against the llm mapper on one response.
//
// They are independent readers of the same bytes: the observation IR feeds analysis and the Monitor's
// Gemini decoration, while `llm.GeminiToLLM` feeds the parsed response and the recorder timeline. The
// llm side used to read only `text`, so it reported the reasoning as answer text and dropped the tool
// call, while the observation reported both correctly. This gate keeps the two readings of one body
// identical instead of asserting each side against a hand-written expectation.
func TestGeminiPartKindsAgreeAcrossParsers(t *testing.T) {
	parser := NewGeminiParser()
	observation, err := parser.Parse(context.Background(), ParseInput{
		TraceID:      "trace-gemini-kinds",
		Header:       geminiTestHeader("google_genai", "/v1beta/models:generateContent", false),
		ResponseBody: []byte(geminiKindBody),
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(observation.Response.Reasoning) != 1 || observation.Response.Reasoning[0].Text != "weighing options" {
		t.Fatalf("observation reasoning = %+v, want the thought part", observation.Response.Reasoning)
	}

	mapped, err := llm.ParseResponse(llm.ProviderGoogleGenAI, "/v1beta/models:generateContent", []byte(geminiKindBody))
	if err != nil {
		t.Fatalf("ParseResponse() error = %v", err)
	}
	if len(mapped.Candidates) != 1 {
		t.Fatalf("ParseResponse() candidates = %d, want 1", len(mapped.Candidates))
	}
	reasoning := ""
	text := ""
	for _, part := range mapped.Candidates[0].Content {
		switch part.Type {
		case "thinking":
			reasoning += part.Text
		case "text":
			text += part.Text
		}
	}
	if reasoning != observation.Response.Reasoning[0].Text {
		t.Fatalf("mapped reasoning = %q, observation reasoning = %q: the two readers must agree",
			reasoning, observation.Response.Reasoning[0].Text)
	}
	observationText := ""
	for _, flat := range FlattenNodes(observation.Response.Nodes) {
		if flat.Node.NormalizedType == NodeText {
			observationText += flat.Node.Text
		}
	}
	if observationText != text {
		t.Fatalf("mapped text = %q, observation text = %q: the two readers must agree", text, observationText)
	}

	observationCalls := map[string]bool{}
	for _, call := range observation.Tools.Calls {
		if call.Name != "" {
			observationCalls[call.Name] = true
		}
	}
	if !observationCalls["get_weather"] {
		t.Fatalf("observation tool calls = %+v, want get_weather", observation.Tools.Calls)
	}
	if len(mapped.Candidates[0].ToolCalls) != 1 || !observationCalls[mapped.Candidates[0].ToolCalls[0].Name] {
		t.Fatalf("mapped tool calls = %+v, observation tool calls = %+v", mapped.Candidates[0].ToolCalls, observation.Tools.Calls)
	}
}
