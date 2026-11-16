package protocolconvert

// End-to-end wire-shape regressions for the P5 legacy-engine defects
// (.scratch/audit/p5-parity.md §4.1 ③⑧, .scratch/audit/p5-legacy-fix.md):
// Responses item text parts use the "text" member key, parallel Gemini
// functionCalls all survive, and functionCall.args reaches tool_use.input.

import (
	"encoding/json"
	"reflect"
	"testing"
)

type wireResponsesItem struct {
	Type      string           `json:"type"`
	Role      string           `json:"role"`
	Content   []map[string]any `json:"content"`
	CallID    string           `json:"call_id"`
	Name      string           `json:"name"`
	Arguments string           `json:"arguments"`
}

// assertResponsesTextPart pins the correct Responses content-part shape:
// {"type":"input_text"|"output_text","text":"..."} — never a payload member
// named after the part type (the S-INPUT-TEXT bug).
func assertResponsesTextPart(t *testing.T, part map[string]any, wantType, wantText string) {
	t.Helper()
	if part["type"] != wantType {
		t.Errorf("part type = %v, want %q", part["type"], wantType)
	}
	if part["text"] != wantText {
		t.Errorf("part text = %v, want %q (payload must live under the text member)", part["text"], wantText)
	}
	if _, ok := part["input_text"]; ok {
		t.Errorf("part carries an input_text payload member (invalid wire shape): %v", part)
	}
	if _, ok := part["output_text"]; ok {
		t.Errorf("part carries an output_text payload member (invalid wire shape): %v", part)
	}
}

func assertJSONEqual(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("got is not JSON: %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is not JSON: %q: %v", want, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("JSON mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestConvertRequestToResponsesTextMemberKey(t *testing.T) {
	sources := []struct {
		name string
		from Protocol
		body string
	}{
		{"chat", ProtocolChatCompletions, `{"model":"m","messages":[{"role":"user","content":"Hello"},{"role":"assistant","content":"Hi there"}]}`},
		{"messages", ProtocolMessages, `{"model":"m","max_tokens":16,"messages":[{"role":"user","content":"Hello"},{"role":"assistant","content":"Hi there"}]}`},
		{"gemini", ProtocolGemini, `{"contents":[{"role":"user","parts":[{"text":"Hello"}]},{"role":"model","parts":[{"text":"Hi there"}]}]}`},
	}
	for _, src := range sources {
		t.Run(src.name, func(t *testing.T) {
			result, err := ConvertRequest([]byte(src.body), src.from, ProtocolResponses, "wire-model", false)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Input []wireResponsesItem `json:"input"`
			}
			if err := json.Unmarshal(result.Body, &wire); err != nil {
				t.Fatalf("decode converted body: %v\n%s", err, result.Body)
			}
			if len(wire.Input) != 2 {
				t.Fatalf("input items = %d, want 2: %s", len(wire.Input), result.Body)
			}
			if wire.Input[0].Type != "message" || len(wire.Input[0].Content) != 1 {
				t.Fatalf("user item shape: %+v", wire.Input[0])
			}
			assertResponsesTextPart(t, wire.Input[0].Content[0], "input_text", "Hello")
			if wire.Input[1].Type != "message" || len(wire.Input[1].Content) != 1 {
				t.Fatalf("assistant item shape: %+v", wire.Input[1])
			}
			assertResponsesTextPart(t, wire.Input[1].Content[0], "output_text", "Hi there")
		})
	}
}

func TestConvertResponseToResponsesTextMemberKey(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"Hi there"},"finish_reason":"stop"}]}`)
	result, err := ConvertResponse(body, ProtocolChatCompletions, ProtocolResponses, "wire-model", false)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Output     []wireResponsesItem `json:"output"`
		OutputText string              `json:"output_text"`
	}
	if err := json.Unmarshal(result.Body, &wire); err != nil {
		t.Fatalf("decode converted body: %v\n%s", err, result.Body)
	}
	if len(wire.Output) != 1 || wire.Output[0].Type != "message" || len(wire.Output[0].Content) != 1 {
		t.Fatalf("output shape: %s", result.Body)
	}
	assertResponsesTextPart(t, wire.Output[0].Content[0], "output_text", "Hi there")
	// The envelope's top-level output_text convenience member is legitimate
	// and must stay.
	if wire.OutputText != "Hi there" {
		t.Errorf("envelope output_text = %q, want %q", wire.OutputText, "Hi there")
	}
}

const geminiParallelResponse = `{"candidates":[{"content":{"role":"model","parts":[{"text":"checking both"},{"functionCall":{"name":"weather","args":{"city":"SH"}}},{"functionCall":{"name":"weather","args":{"city":"BJ"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":9,"totalTokenCount":19}}`

func TestConvertGeminiParallelCallsToChat(t *testing.T) {
	result, err := ConvertResponse([]byte(geminiParallelResponse), ProtocolGemini, ProtocolChatCompletions, "wire-model", false)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(result.Body, &wire); err != nil {
		t.Fatalf("decode converted body: %v\n%s", err, result.Body)
	}
	if len(wire.Choices) != 1 {
		t.Fatalf("choices = %d, want 1: %s", len(wire.Choices), result.Body)
	}
	choice := wire.Choices[0]
	if len(choice.Message.ToolCalls) != 2 {
		t.Fatalf("tool_calls = %d, want 2 (parallel calls must all survive): %s", len(choice.Message.ToolCalls), result.Body)
	}
	first, second := choice.Message.ToolCalls[0], choice.Message.ToolCalls[1]
	if first.ID != "call_1" || second.ID != "call_2" {
		t.Errorf("synthesized ids = %q/%q, want call_1/call_2", first.ID, second.ID)
	}
	if first.Function.Name != "weather" || second.Function.Name != "weather" {
		t.Errorf("tool names = %q/%q, want weather/weather", first.Function.Name, second.Function.Name)
	}
	assertJSONEqual(t, first.Function.Arguments, `{"city":"SH"}`)
	assertJSONEqual(t, second.Function.Arguments, `{"city":"BJ"}`)
	if choice.FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", choice.FinishReason)
	}
	if choice.Message.Content != "checking both" {
		t.Errorf("text lost: %q", choice.Message.Content)
	}
}

func TestConvertGeminiCallArgsToMessages(t *testing.T) {
	body := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"weather","args":{"city":"SH"}}}]},"finishReason":"STOP"}]}`)
	result, err := ConvertResponse(body, ProtocolGemini, ProtocolMessages, "wire-model", false)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Content []struct {
			Type  string         `json:"type"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(result.Body, &wire); err != nil {
		t.Fatalf("decode converted body: %v\n%s", err, result.Body)
	}
	if len(wire.Content) != 1 || wire.Content[0].Type != "tool_use" {
		t.Fatalf("content shape: %s", result.Body)
	}
	block := wire.Content[0]
	if block.Name != "weather" {
		t.Errorf("tool name = %q, want weather", block.Name)
	}
	if block.ID == "" {
		t.Errorf("tool_use id must be synthesized, got empty")
	}
	want := map[string]any{"city": "SH"}
	if !reflect.DeepEqual(block.Input, want) {
		t.Errorf("tool_use.input = %v, want %v (functionCall.args must be carried)", block.Input, want)
	}
	if wire.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", wire.StopReason)
	}
}

func TestConvertGeminiParallelCallsToMessages(t *testing.T) {
	result, err := ConvertResponse([]byte(geminiParallelResponse), ProtocolGemini, ProtocolMessages, "wire-model", false)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Content []struct {
			Type  string         `json:"type"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result.Body, &wire); err != nil {
		t.Fatalf("decode converted body: %v\n%s", err, result.Body)
	}
	var toolUse []struct {
		Type  string         `json:"type"`
		ID    string         `json:"id"`
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	}
	for _, block := range wire.Content {
		if block.Type == "tool_use" {
			toolUse = append(toolUse, block)
		}
	}
	if len(toolUse) != 2 {
		t.Fatalf("tool_use blocks = %d, want 2: %s", len(toolUse), result.Body)
	}
	if toolUse[0].ID == toolUse[1].ID {
		t.Errorf("parallel tool_use ids must be distinct, both %q", toolUse[0].ID)
	}
	if !reflect.DeepEqual(toolUse[0].Input, map[string]any{"city": "SH"}) {
		t.Errorf("first input = %v, want city=SH", toolUse[0].Input)
	}
	if !reflect.DeepEqual(toolUse[1].Input, map[string]any{"city": "BJ"}) {
		t.Errorf("second input = %v, want city=BJ", toolUse[1].Input)
	}
}

func TestConvertGeminiParallelCallsToResponses(t *testing.T) {
	result, err := ConvertResponse([]byte(geminiParallelResponse), ProtocolGemini, ProtocolResponses, "wire-model", false)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Output []wireResponsesItem `json:"output"`
	}
	if err := json.Unmarshal(result.Body, &wire); err != nil {
		t.Fatalf("decode converted body: %v\n%s", err, result.Body)
	}
	var calls []wireResponsesItem
	for _, item := range wire.Output {
		if item.Type == "function_call" {
			calls = append(calls, item)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("function_call items = %d, want 2: %s", len(calls), result.Body)
	}
	if calls[0].CallID != "call_1" || calls[1].CallID != "call_2" {
		t.Errorf("call ids = %q/%q, want call_1/call_2", calls[0].CallID, calls[1].CallID)
	}
	assertJSONEqual(t, calls[0].Arguments, `{"city":"SH"}`)
	assertJSONEqual(t, calls[1].Arguments, `{"city":"BJ"}`)
}
