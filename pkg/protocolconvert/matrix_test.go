package protocolconvert

// Exhaustive conversion coverage: every ordered protocol pair for requests
// and responses, byte-identity for same-protocol conversion, stream-flag
// preservation, and malformed-input handling. These are the correctness
// guarantees the hot path relies on when it converts instead of passing
// through.

import (
	"strings"
	"testing"
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

func TestConvertRequestPreservesStreamFlag(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"Hi"}],"stream":true,"max_tokens":10}`)
	for _, to := range AllProtocols {
		result, err := ConvertRequest(body, ProtocolChatCompletions, to, "", false)
		if err != nil {
			t.Fatalf("-> %s: %v", to, err)
		}
		if !strings.Contains(string(result.Body), `"stream":true`) {
			t.Errorf("-> %s: stream flag lost: %s", to, result.Body)
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

func TestConvertResponsePreservesToolCalls(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Shanghai\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	result, err := ConvertResponse(body, ProtocolChatCompletions, ProtocolMessages, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Body), "tool_use") || !strings.Contains(string(result.Body), "weather") {
		t.Errorf("tool call lost in chat -> messages: %s", result.Body)
	}
}

func TestConvertStreamResponseAllTargets(t *testing.T) {
	body := []byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n")
	want := map[Protocol]string{
		ProtocolChatCompletions: "[DONE]",
		ProtocolResponses:       "response.completed",
		ProtocolMessages:        "message_stop",
		ProtocolGemini:          "candidates",
	}
	for _, to := range AllProtocols {
		result, err := ConvertStreamResponse(body, ProtocolChatCompletions, to, "", false)
		if err != nil {
			t.Errorf("-> %s: %v", to, err)
			continue
		}
		if !strings.Contains(string(result.Body), "Hello") {
			t.Errorf("-> %s: text lost: %s", to, result.Body)
		}
		if !strings.Contains(string(result.Body), want[to]) {
			t.Errorf("-> %s: missing %q in: %s", to, want[to], result.Body)
		}
	}
}

func TestPlanForRequestRejectsStreamingConversion(t *testing.T) {
	decision, err := PlanForRequest(PlanRequest{
		Client: ProtocolMessages,
		Caps:   Capabilities{ChatCompletions: true},
		Mode:   ModePassthroughPreferred,
		Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Reject() {
		t.Errorf("streaming conversion should reject, got %+v", decision)
	}
	// Non-streaming plans the same pair as convert.
	decision, err = PlanForRequest(PlanRequest{
		Client: ProtocolMessages,
		Caps:   Capabilities{ChatCompletions: true},
		Mode:   ModePassthroughPreferred,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Convert() {
		t.Errorf("non-streaming should convert, got %+v", decision)
	}
	// Streaming passthrough is unaffected.
	decision, err = PlanForRequest(PlanRequest{
		Client: ProtocolMessages,
		Caps:   Capabilities{Messages: true},
		Mode:   ModePassthroughPreferred,
		Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Passthrough() {
		t.Errorf("streaming passthrough should pass, got %+v", decision)
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

func TestConvertRequestVertexGemini(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"Hello"}],"max_tokens":10}`)
	result, err := ConvertRequest(body, ProtocolChatCompletions, ProtocolGemini, "gemini-2.5-flash", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Body), "Hello") {
		t.Errorf("vertex gemini conversion lost text: %s", result.Body)
	}
	if got := ProtocolGemini.RequestPath("gemini-2.5-flash", false, true); got != "/v1/publishers/models/gemini-2.5-flash:generateContent" {
		t.Errorf("vertex RequestPath = %q", got)
	}
}
