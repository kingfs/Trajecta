package protocolconvert

// Exhaustive conversion coverage: every ordered protocol pair for requests
// and responses, byte-identity for same-protocol conversion, tool calls and
// tool results, protocol-specific finish reasons, and malformed-input handling.
// These are the correctness guarantees the hot path relies on when it converts
// instead of passing through.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/llm"
)

var requestSamples = map[Protocol]string{
	ProtocolChatCompletions: `{"model":"m","messages":[{"role":"system","content":"Sys"},{"role":"user","content":"Hello"}],"tools":[{"type":"function","function":{"name":"weather","description":"d","parameters":{"type":"object"}}}],"temperature":0.5,"max_tokens":100}`,
	ProtocolResponses:       `{"model":"m","instructions":"Sys","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hello"}]}],"tools":[{"type":"function","name":"weather"}],"max_output_tokens":100}`,
	ProtocolMessages:        `{"model":"m","system":"Sys","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"Hello"}]}]}`,
	ProtocolGemini:          `{"systemInstruction":{"parts":[{"text":"Sys"}]},"contents":[{"role":"user","parts":[{"text":"Hello"}]}],"generationConfig":{"maxOutputTokens":100}}`,
}

var responseSamples = map[Protocol]string{
	ProtocolChatCompletions: `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"Hi there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
	ProtocolResponses:       `{"id":"resp_1","object":"response","created_at":1,"model":"m","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi there"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`,
	ProtocolMessages:        `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"Hi there"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`,
	ProtocolGemini:          `{"candidates":[{"content":{"role":"model","parts":[{"text":"Hi there"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`,
}

func TestConvertRequestMatrix(t *testing.T) {
	for _, from := range AllProtocols {
		for _, to := range AllProtocols {
			body := requestSamples[from]
			result, err := ConvertRequest([]byte(body), from, to, "m", false)
			if err != nil {
				t.Errorf("ConvertRequest %s -> %s: %v", from, to, err)
				continue
			}
			if !strings.Contains(string(result.Body), "Hello") {
				t.Errorf("ConvertRequest %s -> %s: output lost user text: %s", from, to, result.Body)
			}
			// Gemini bodies never carry the model (it travels in the path),
			// so the model check only applies to non-Gemini targets.
			if from != to && to != ProtocolGemini && from != ProtocolGemini && !strings.Contains(string(result.Body), `"m"`) {
				t.Errorf("ConvertRequest %s -> %s: output lost model: %s", from, to, result.Body)
			}
			// The converted body must itself be parseable in the target
			// protocol: convert it back to chat and require the text again.
			back, err := ConvertRequest(result.Body, to, ProtocolChatCompletions, "m", false)
			if err != nil {
				t.Errorf("round-trip %s -> %s -> chat: %v", from, to, err)
				continue
			}
			if !strings.Contains(string(back.Body), "Hello") {
				t.Errorf("round-trip %s -> %s -> chat lost text: %s", from, to, back.Body)
			}
		}
	}
}

func TestConvertResponseMatrix(t *testing.T) {
	wantFinish := map[Protocol]string{
		ProtocolChatCompletions: "stop",
		ProtocolMessages:        "end_turn",
		ProtocolGemini:          "STOP",
	}
	for _, from := range AllProtocols {
		for _, to := range AllProtocols {
			result, err := ConvertResponse([]byte(responseSamples[from]), from, to, "m", false)
			if err != nil {
				t.Errorf("ConvertResponse %s -> %s: %v", from, to, err)
				continue
			}
			if !strings.Contains(string(result.Body), "Hi there") {
				t.Errorf("ConvertResponse %s -> %s: output lost text: %s", from, to, result.Body)
			}
			if from != to && wantFinish[to] != "" && !strings.Contains(string(result.Body), wantFinish[to]) {
				t.Errorf("ConvertResponse %s -> %s: finish reason is not %q: %s", from, to, wantFinish[to], result.Body)
			}
			back, err := ConvertResponse(result.Body, to, ProtocolChatCompletions, "m", false)
			if err != nil {
				t.Errorf("response round-trip %s -> %s -> chat: %v", from, to, err)
				continue
			}
			if !strings.Contains(string(back.Body), "Hi there") {
				t.Errorf("response round-trip %s -> %s -> chat lost text: %s", from, to, back.Body)
			}
		}
	}
}

func TestConvertSameProtocolIsByteIdentical(t *testing.T) {
	// Same-protocol conversion must not go through the IR: unknown fields
	// and whitespace survive byte for byte.
	body := []byte("{ \"model\" : \"m\" , \"messages\" : [ ] , \"unknown_future_field\" : { \"x\" : 1 } }")
	for _, p := range AllProtocols {
		result, err := ConvertRequest(body, p, p, "", false)
		if err != nil {
			t.Fatalf("ConvertRequest %s -> %s: %v", p, p, err)
		}
		if string(result.Body) != string(body) {
			t.Errorf("ConvertRequest %s -> %s not byte-identical:\n got %s\nwant %s", p, p, result.Body, body)
		}
	}
	resp := []byte(`{"id":"x","future":true}`)
	result, err := ConvertResponse(resp, ProtocolChatCompletions, ProtocolChatCompletions, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Body) != string(resp) {
		t.Errorf("ConvertResponse same-protocol not byte-identical: %s", result.Body)
	}
}

func TestConvertRequestRejectsStreaming(t *testing.T) {
	for _, stream := range []string{`true`, `"true"`, `1`} {
		body := []byte(`{"model":"m","messages":[{"role":"user","content":"Hi"}],"stream":` + stream + `,"max_tokens":10}`)
		if _, err := ConvertRequest(body, ProtocolChatCompletions, ProtocolMessages, "m", false); err == nil {
			t.Fatalf("streaming conversion (stream=%s) must be rejected; a frame-by-frame translator is required", stream)
		}
	}
}

func TestConvertRequestMalformed(t *testing.T) {
	cases := []struct {
		name string
		body string
		from Protocol
		to   Protocol
	}{
		{"empty", "", ProtocolChatCompletions, ProtocolMessages},
		{"not-json", "hello", ProtocolChatCompletions, ProtocolMessages},
		{"json-array", `[1,2]`, ProtocolChatCompletions, ProtocolMessages},
		{"bad-from", `{"model":"m"}`, Protocol("nope"), ProtocolMessages},
		{"bad-to", `{"model":"m"}`, ProtocolChatCompletions, Protocol("nope")},
	}
	for _, tc := range cases {
		if _, err := ConvertRequest([]byte(tc.body), tc.from, tc.to, "", false); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
		if _, err := ConvertResponse([]byte(tc.body), tc.from, tc.to, "", false); err == nil {
			t.Errorf("%s response: expected error", tc.name)
		}
	}
}

// TestToolLoopMessagesToChat pins the Claude-Code direction: an Anthropic
// client's second turn carries the assistant tool_use and the user tool_result.
// Chat Completions needs the call as tool_calls and the result as a role "tool"
// message, never a user message with a tool_call_id.
func TestToolLoopMessagesToChat(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":10,"system":[{"type":"text","text":"Sys"}],"messages":[
		{"role":"user","content":"weather?"},
		{"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"toolu_1","name":"weather","input":{"city":"SH"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"sunny"}]}]}`)
	result, err := ConvertRequest(body, ProtocolMessages, ProtocolChatCompletions, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    any    `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(result.Body, &out); err != nil {
		t.Fatalf("converted body is not JSON: %v (%s)", err, result.Body)
	}
	if !strings.Contains(string(result.Body), `"role":"system"`) || !strings.Contains(string(result.Body), "Sys") {
		t.Errorf("system prompt lost: %s", result.Body)
	}
	var assistantTool bool
	var toolResult bool
	for _, message := range out.Messages {
		if message.Role == "assistant" && len(message.ToolCalls) == 1 && message.ToolCalls[0].Function.Name == "weather" {
			assistantTool = true
		}
		if message.Role == "tool" && message.ToolCallID == "toolu_1" && message.Content == "sunny" {
			toolResult = true
		}
		if message.Role == "user" && message.ToolCallID != "" {
			t.Errorf("tool_call_id leaked onto a user message: %s", result.Body)
		}
	}
	if !assistantTool || !toolResult {
		t.Fatalf("tool loop not preserved (assistantTool=%v toolResult=%v): %s", assistantTool, toolResult, result.Body)
	}
}

// TestToolLoopChatToMessages pins the reverse direction: a Chat client's tool
// result must become a user message with a tool_result block, not a "tool"
// role Messages rejects.
func TestToolLoopChatToMessages(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"weather?"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"SH\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"sunny"}]}`)
	result, err := ConvertRequest(body, ProtocolChatCompletions, ProtocolMessages, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
				Content   any    `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(result.Body, &out); err != nil {
		t.Fatalf("converted body is not JSON: %v (%s)", err, result.Body)
	}
	var sawToolResult bool
	for _, message := range out.Messages {
		if message.Role == "tool" {
			t.Errorf("Messages has no tool role: %s", result.Body)
		}
		for _, block := range message.Content {
			if block.Type == "tool_result" {
				sawToolResult = true
				if block.ToolUseID != "call_1" || block.Content != "sunny" {
					t.Errorf("tool_result not preserved: %+v", block)
				}
			}
		}
	}
	if !sawToolResult {
		t.Fatalf("tool_result lost: %s", result.Body)
	}
}

func TestConvertResponsePreservesToolCalls(t *testing.T) {
	chat := []byte(`{"id":"c1","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"SH\"}"}}]},"finish_reason":"tool_calls"}]}`)
	result, err := ConvertResponse(chat, ProtocolChatCompletions, ProtocolMessages, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Body), `"type":"tool_use"`) || !strings.Contains(string(result.Body), `"stop_reason":"tool_use"`) {
		t.Errorf("chat -> messages lost the tool call or stop reason: %s", result.Body)
	}

	messages := []byte(`{"id":"m1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"toolu_1","name":"weather","input":{"city":"SH"}}],"stop_reason":"tool_use"}`)
	result, err = ConvertResponse(messages, ProtocolMessages, ProtocolChatCompletions, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Body), `"tool_calls"`) || !strings.Contains(string(result.Body), `"finish_reason":"tool_calls"`) {
		t.Errorf("messages -> chat lost the tool call or finish reason: %s", result.Body)
	}
}

func TestConvertRequestPreservesAnthropicSystemBlocks(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":10,"system":[{"type":"text","text":"You are a helpful assistant.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`)
	result, err := ConvertRequest(body, ProtocolMessages, ProtocolChatCompletions, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Body), "You are a helpful assistant.") {
		t.Fatalf("system block lost: %s", result.Body)
	}
}

func TestConvertChatToResponsesShape(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"hello"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"result"}]}`)
	result, err := ConvertRequest(body, ProtocolChatCompletions, ProtocolResponses, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	// The IR has no tool_choice, so an absent directive must stay absent: an
	// empty string is not a valid Responses tool_choice.
	if strings.Contains(string(result.Body), `"tool_choice"`) {
		t.Errorf("empty tool_choice must be omitted: %s", result.Body)
	}
	// Assistant history uses output_text; tool results use function_call_output.
	if !strings.Contains(string(result.Body), `"type":"output_text"`) {
		t.Errorf("assistant history is not output_text: %s", result.Body)
	}
	if !strings.Contains(string(result.Body), `"type":"function_call_output"`) || !strings.Contains(string(result.Body), `"output":"result"`) {
		t.Errorf("tool result is not a function_call_output with content: %s", result.Body)
	}
	if strings.Contains(string(result.Body), `"role":"tool"`) {
		t.Errorf("Responses has no tool role: %s", result.Body)
	}
}

func TestConvertChatToGeminiToolFlow(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"weather?"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"SH\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"sunny"}]}`)
	result, err := ConvertRequest(body, ProtocolChatCompletions, ProtocolGemini, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	output := string(result.Body)
	if !strings.Contains(output, `"role":"model"`) || strings.Contains(output, `"role":"assistant"`) {
		t.Errorf("assistant role is not mapped to model: %s", result.Body)
	}
	if !strings.Contains(output, `"functionCall"`) || !strings.Contains(output, `"functionResponse"`) {
		t.Errorf("tool flow lost: %s", result.Body)
	}
	if strings.Contains(output, `"role":"tool"`) {
		t.Errorf("Gemini has no tool role: %s", result.Body)
	}
}

func TestGeminiResponseToolCallToChat(t *testing.T) {
	body := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"checking"},{"functionCall":{"name":"weather","args":{"city":"SH"}}}]},"finishReason":"STOP"}]}`)
	result, err := ConvertResponse(body, ProtocolGemini, ProtocolChatCompletions, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Body), `"tool_calls"`) || !strings.Contains(string(result.Body), `"name":"weather"`) {
		t.Errorf("Gemini tool call lost: %s", result.Body)
	}
	if !strings.Contains(string(result.Body), `"finish_reason":"tool_calls"`) {
		t.Errorf("Gemini STOP with a tool call must become tool_calls: %s", result.Body)
	}
}

func TestToolChoiceMapping(t *testing.T) {
	chatBody := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"f"}}}`)
	cases := []struct {
		to       Protocol
		contains string
	}{
		{ProtocolMessages, `"tool_choice":{"name":"f","type":"tool"}`},
		{ProtocolResponses, `"tool_choice":{"name":"f","type":"function"}`},
		{ProtocolGemini, `"allowedFunctionNames":["f"]`},
	}
	for _, tc := range cases {
		result, err := ConvertRequest(chatBody, ProtocolChatCompletions, tc.to, "m", false)
		if err != nil {
			t.Fatalf("%s: %v", tc.to, err)
		}
		if !strings.Contains(strings.ReplaceAll(string(result.Body), " ", ""), strings.ReplaceAll(tc.contains, " ", "")) {
			t.Errorf("%s tool_choice not mapped: %s", tc.to, result.Body)
		}
	}
}

func TestUnsupportedContentIsRejectedStructurally(t *testing.T) {
	cases := map[string]struct {
		body string
		from Protocol
		to   Protocol
	}{
		"chat image_url":          {`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://example/x.png"}}]}]}`, ProtocolChatCompletions, ProtocolMessages},
		"chat audio":              {`{"model":"m","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAA"}}]}]}`, ProtocolChatCompletions, ProtocolMessages},
		"messages image":          {`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]}]}`, ProtocolMessages, ProtocolChatCompletions},
		"messages thinking":       {`{"model":"m","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"secret plan"}]}]}`, ProtocolMessages, ProtocolChatCompletions},
		"gemini function history": {`{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"functionCall":{"name":"f","args":{}}}]}]}`, ProtocolGemini, ProtocolChatCompletions},
		"gemini inline data":      {`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"AAA"}}]}]}`, ProtocolGemini, ProtocolChatCompletions},
	}
	for name, tc := range cases {
		if _, err := ConvertRequest([]byte(tc.body), tc.from, tc.to, "m", false); err == nil {
			t.Errorf("%s: expected a structural rejection", name)
		}
	}
	// Ordinary prose containing the same words must still convert: a byte
	// blacklist used to reject this.
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"How does image_url and signature and thinking work?"}],"max_tokens":10}`)
	if _, err := ConvertRequest(body, ProtocolChatCompletions, ProtocolMessages, "m", false); err != nil {
		t.Errorf("plain prose must convert: %v", err)
	}
}

func TestFatalWarningsForSemanticLoss(t *testing.T) {
	cases := map[string]string{
		"response_format":  `{"model":"m","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`,
		"reasoning_effort": `{"model":"m","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`,
		"seed":             `{"model":"m","messages":[{"role":"user","content":"hi"}],"seed":42}`,
	}
	for field, body := range cases {
		result, err := ConvertRequest([]byte(body), ProtocolChatCompletions, ProtocolMessages, "m", false)
		if err != nil {
			t.Fatalf("%s: %v", field, err)
		}
		fatal := result.FatalWarnings()
		if len(fatal) == 0 || fatal[0].Field != field {
			t.Errorf("%s: expected a fatal warning, got %v", field, fatal)
		}
	}
	// A default-valued field is not a loss.
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"presence_penalty":0,"logprobs":false}`
	result, err := ConvertRequest([]byte(body), ProtocolChatCompletions, ProtocolMessages, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.FatalWarnings()) != 0 {
		t.Errorf("default-valued fields must not be fatal: %v", result.FatalWarnings())
	}
}

func TestDetectFromPathRejectsCountTokens(t *testing.T) {
	if _, err := DetectFromPath("/v1/messages/count_tokens"); err == nil {
		t.Error("count_tokens must not be convertible (different semantics/response shape)")
	}
}

func TestUnsupportedFieldsReportsDroppedKeys(t *testing.T) {
	body := []byte(`{"model":"m","messages":[],"presence_penalty":0.5,"reasoning_effort":"high"}`)
	fields := UnsupportedFields(body, ProtocolChatCompletions)
	joined := strings.Join(fields, ",")
	if !strings.Contains(joined, "presence_penalty") || !strings.Contains(joined, "reasoning_effort") {
		t.Errorf("expected dropped keys reported, got %v", fields)
	}
	if strings.Contains(joined, "model") {
		t.Errorf("model is carried, must not be reported: %v", fields)
	}
	if got := UnsupportedFields([]byte("not json"), ProtocolChatCompletions); len(got) != 0 {
		t.Errorf("malformed body should yield no fields, got %v", got)
	}
}

func TestRequestPathRejectsUnsafeModels(t *testing.T) {
	for _, model := range []string{"", "../../v1/messages", "a:b", "a b", "/leading", "trailing/"} {
		if _, err := ProtocolGemini.RequestPath(model, false, true); err == nil {
			t.Errorf("RequestPath(%q) must fail", model)
		}
	}
	if path, err := ProtocolGemini.RequestPath("gemini-2.5-flash", false, true); err != nil || path != "/v1/publishers/models/gemini-2.5-flash:generateContent" {
		t.Errorf("RequestPath = %q, %v", path, err)
	}
}

func TestConvertRequestVertexGemini(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"Hello"}],"max_tokens":10}`)
	result, err := ConvertRequest(body, ProtocolChatCompletions, ProtocolGemini, "gemini-2.5-flash", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Body), "Hello") {
		t.Errorf("vertex gemini conversion lost text: %s", result.Body)
	}
	var parsed llm.LLMRequest
	if err := json.Unmarshal(result.Body, &parsed); err != nil {
		t.Fatalf("converted body is not JSON: %v", err)
	}
}

func TestUnknownToolChoiceIsRejected(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"allowed_tools","tools":[]}}`)
	if _, err := ConvertRequest(body, ProtocolChatCompletions, ProtocolMessages, "m", false); err == nil {
		t.Fatal("an unrecognized tool_choice shape must be rejected, not silently dropped")
	}
	// The empty-string spelling emitted by some Responses clients means "no
	// directive" and must not be treated as an unknown shape.
	okBody := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tool_choice":""}`)
	if _, err := ConvertRequest(okBody, ProtocolChatCompletions, ProtocolMessages, "m", false); err != nil {
		t.Fatalf("empty tool_choice must be treated as absent: %v", err)
	}
}

func TestConvertResponseRefusal(t *testing.T) {
	body := []byte(`{"id":"resp_1","object":"response","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"I cannot help with that"}]}]}`)

	chat, err := ConvertResponse(body, ProtocolResponses, ProtocolChatCompletions, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(chat.Body), `"refusal":"I cannot help with that"`) || !strings.Contains(string(chat.Body), `"finish_reason":"content_filter"`) {
		t.Errorf("refusal -> chat not preserved: %s", chat.Body)
	}

	messages, err := ConvertResponse(body, ProtocolResponses, ProtocolMessages, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(messages.Body), `"stop_reason":"refusal"`) || !strings.Contains(string(messages.Body), "I cannot help with that") {
		t.Errorf("refusal -> messages not preserved: %s", messages.Body)
	}

	gemini, err := ConvertResponse(body, ProtocolResponses, ProtocolGemini, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gemini.Body), `"finishReason":"SAFETY"`) || !strings.Contains(string(gemini.Body), "I cannot help with that") {
		t.Errorf("refusal -> gemini not preserved: %s", gemini.Body)
	}
}

func TestConvertChatToResponsesEmptyToolOutput(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":""}]}`)
	result, err := ConvertRequest(body, ProtocolChatCompletions, ProtocolResponses, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Body), `"output":""`) {
		t.Errorf("an empty tool result must keep a present output field: %s", result.Body)
	}
}

func TestFatalWarningsCoverAdditionalSemanticFields(t *testing.T) {
	cases := map[string]struct {
		body      string
		from      Protocol
		to        Protocol
		wantField string
	}{
		"n":           {`{"model":"m","messages":[{"role":"user","content":"hi"}],"n":2}`, ProtocolChatCompletions, ProtocolMessages, "n"},
		"logit_bias":  {`{"model":"m","messages":[{"role":"user","content":"hi"}],"logit_bias":{"50256":-100}}`, ProtocolChatCompletions, ProtocolMessages, "logit_bias"},
		"text.format": {`{"model":"m","input":"hi","text":{"format":{"type":"json_schema"}}}`, ProtocolResponses, ProtocolChatCompletions, "text.format"},
		"background":  {`{"model":"m","input":"hi","background":true}`, ProtocolResponses, ProtocolChatCompletions, "background"},
	}
	for name, tc := range cases {
		result, err := ConvertRequest([]byte(tc.body), tc.from, tc.to, "m", false)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		found := false
		for _, warning := range result.FatalWarnings() {
			if warning.Field == tc.wantField {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: expected fatal warning %q, got %v", name, tc.wantField, result.FatalWarnings())
		}
	}
}

func TestFatalWarningsAllowDefaultResponsesTextFormat(t *testing.T) {
	body := []byte(`{"model":"m","input":"hi","text":{"format":{"type":"text"}}}`)
	result, err := ConvertRequest(body, ProtocolResponses, ProtocolChatCompletions, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.FatalWarnings()) != 0 {
		t.Errorf("the default Responses text format is not a loss: %v", result.FatalWarnings())
	}
}

func TestStructuredToolResultBecomesProtocolText(t *testing.T) {
	body := []byte(`{"model":"m","input":[
		{"type":"function_call","call_id":"call_1","name":"f","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_1","output":{"cwd":"/tmp"}}]}`)

	messages, err := ConvertRequest(body, ProtocolResponses, ProtocolMessages, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Messages []struct {
			Content []struct {
				Type    string `json:"type"`
				Content string `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(messages.Body, &parsed); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, messages.Body)
	}
	var content string
	for _, message := range parsed.Messages {
		for _, block := range message.Content {
			if block.Type == "tool_result" {
				content = block.Content
			}
		}
	}
	if !strings.Contains(content, "cwd") {
		t.Fatalf("structured tool result not rendered as text: %s", messages.Body)
	}

	chat, err := ConvertRequest(body, ProtocolResponses, ProtocolChatCompletions, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(chat.Body), `"role":"tool"`) || !strings.Contains(string(chat.Body), "cwd") {
		t.Fatalf("structured tool result lost in chat conversion: %s", chat.Body)
	}
}

func TestChatReasoningHistoryIsRejected(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"assistant","content":"look","reasoning_content":"hidden plan"},{"role":"user","content":"go on"}]}`)
	if _, err := ConvertRequest(body, ProtocolChatCompletions, ProtocolMessages, "m", false); err == nil {
		t.Fatal("assistant reasoning_content must be rejected, not silently dropped")
	}
	// Prose that merely names the field still converts.
	prose := []byte(`{"model":"m","messages":[{"role":"user","content":"what is reasoning_content?"}],"max_tokens":10}`)
	if _, err := ConvertRequest(prose, ProtocolChatCompletions, ProtocolMessages, "m", false); err != nil {
		t.Fatalf("prose must convert: %v", err)
	}
}

func TestConvertResponseLengthToResponsesIsIncomplete(t *testing.T) {
	body := []byte(`{"id":"c1","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}]}`)
	result, err := ConvertResponse(body, ProtocolChatCompletions, ProtocolResponses, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Status            string `json:"status"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	}
	if err := json.Unmarshal(result.Body, &parsed); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, result.Body)
	}
	if parsed.Status != "incomplete" || parsed.IncompleteDetails.Reason != "max_output_tokens" {
		t.Fatalf("length must be an incomplete Responses result: %s", result.Body)
	}
}

func TestResponsesToolDeclarationsUseFlatShape(t *testing.T) {
	responsesBody := []byte(`{"model":"m","input":"hi","tools":[{"type":"function","name":"weather","description":"d","parameters":{"type":"object"}}]}`)
	chat, err := ConvertRequest(responsesBody, ProtocolResponses, ProtocolChatCompletions, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(chat.Body), `"function":{"name":"weather"`) {
		t.Fatalf("Responses flat tool not mapped to Chat nested tool: %s", chat.Body)
	}

	chatBody := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"weather","description":"d","parameters":{"type":"object"}}}]}`)
	responses, err := ConvertRequest(chatBody, ProtocolChatCompletions, ProtocolResponses, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(responses.Body), `"name":"weather"`) || strings.Contains(string(responses.Body), `"function":{"name"`) {
		t.Fatalf("Chat nested tool not mapped to Responses flat tool: %s", responses.Body)
	}
}

func TestStrictToolToAnthropicIsFatal(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","strict":true,"parameters":{"type":"object"}}}]}`)
	result, err := ConvertRequest(body, ProtocolChatCompletions, ProtocolMessages, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(result.FatalWarnings(), "tools.strict") {
		t.Fatalf("strict tool loss must be fatal: %v", result.FatalWarnings())
	}
	// To a protocol that supports strict tools it is carried, not fatal.
	responses, err := ConvertRequest(body, ProtocolChatCompletions, ProtocolResponses, "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(responses.Body), `"strict":false`) || !strings.Contains(string(responses.Body), `"strict":true`) {
		t.Fatalf("strict must be carried to Responses: %s", responses.Body)
	}
}

func hasWarning(warnings []Warning, field string) bool {
	for _, warning := range warnings {
		if warning.Field == field {
			return true
		}
	}
	return false
}
