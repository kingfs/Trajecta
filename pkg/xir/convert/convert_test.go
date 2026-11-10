package convert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
)

func TestSameProtocolBytePassthrough(t *testing.T) {
	requests := map[xir.Protocol]string{
		xir.ProtocolChatCompletions: `{"model":"m",  "messages":[{"role":"user","content":"hi"}]}`,
		xir.ProtocolResponses:       `{"model":"m", "input":"hi"}`,
		xir.ProtocolMessages:        `{"model":"m",  "max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		xir.ProtocolGemini:          `{"contents":  [{"role":"user","parts":[{"text":"hi"}]}]}`,
	}
	responses := map[xir.Protocol]string{
		xir.ProtocolChatCompletions: `{"id":"c1", "choices":[]}`,
		xir.ProtocolResponses:       `{"id":"r1", "output": []}`,
		xir.ProtocolMessages:        `{"id":"m1",  "content":[]}`,
		xir.ProtocolGemini:          `{"candidates": [ ]}`,
	}
	for protocol := range requests {
		raw := []byte(requests[protocol])
		result, err := ConvertRequest(context.Background(), raw, protocol, protocol, Options{})
		if err != nil {
			t.Fatalf("%s same-protocol request: %v", protocol, err)
		}
		if !bytes.Equal(result.Bytes, raw) {
			t.Fatalf("%s request bytes changed:\n got %s\nwant %s", protocol, result.Bytes, raw)
		}
		if len(result.Bytes) != 0 && &result.Bytes[0] != &raw[0] {
			t.Fatalf("%s request was copied, want the input slice", protocol)
		}
		if len(result.Loss) != 0 {
			t.Fatalf("%s request losses = %v", protocol, result.Loss)
		}

		raw = []byte(responses[protocol])
		result, err = ConvertResponse(context.Background(), raw, protocol, protocol, Options{})
		if err != nil {
			t.Fatalf("%s same-protocol response: %v", protocol, err)
		}
		if !bytes.Equal(result.Bytes, raw) {
			t.Fatalf("%s response bytes changed:\n got %s\nwant %s", protocol, result.Bytes, raw)
		}
		if len(result.Loss) != 0 {
			t.Fatalf("%s response losses = %v", protocol, result.Loss)
		}
	}
}

// TestSameProtocolSkipsDecode proves the invariant with bytes that are not
// valid JSON: a same-protocol call must not parse or decode them.
func TestSameProtocolSkipsDecode(t *testing.T) {
	raw := []byte("this is not json  ")
	for _, protocol := range []xir.Protocol{
		xir.ProtocolChatCompletions,
		xir.ProtocolResponses,
		xir.ProtocolMessages,
		xir.ProtocolGemini,
	} {
		result, err := ConvertRequest(context.Background(), raw, protocol, protocol, Options{Stream: true})
		if err != nil {
			t.Fatalf("%s request: %v", protocol, err)
		}
		if !bytes.Equal(result.Bytes, raw) {
			t.Fatalf("%s request bytes = %q", protocol, result.Bytes)
		}
		result, err = ConvertResponse(context.Background(), raw, protocol, protocol, Options{Stream: true})
		if err != nil {
			t.Fatalf("%s response: %v", protocol, err)
		}
		if !bytes.Equal(result.Bytes, raw) {
			t.Fatalf("%s response bytes = %q", protocol, result.Bytes)
		}
	}
}

func TestChatRequestToResponsesText(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "be brief"},
			{"role": "user", "content": "Hello"}
		],
		"temperature": 0.2
	}`)
	result, err := ConvertRequest(context.Background(), raw, xir.ProtocolChatCompletions, xir.ProtocolResponses, Options{})
	if err != nil {
		t.Fatalf("ConvertRequest: %v", err)
	}
	if len(result.Loss) != 0 {
		t.Fatalf("unexpected losses: %v", result.Loss)
	}
	if result.From != xir.ProtocolChatCompletions || result.To != xir.ProtocolResponses {
		t.Fatalf("From/To = %q/%q", result.From, result.To)
	}
	if len(result.Steps) != 2 || result.Steps[0] != xir.ProtocolChatCompletions || result.Steps[1] != xir.ProtocolResponses {
		t.Fatalf("Steps = %v", result.Steps)
	}
	out := decodeObject(t, result.Bytes)
	if out["model"] != "gpt-4o" {
		t.Fatalf("model = %v", out["model"])
	}
	if out["temperature"] != 0.2 {
		t.Fatalf("temperature = %v", out["temperature"])
	}
	input, ok := out["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("input = %#v", out["input"])
	}
	if !bytes.Contains(result.Bytes, []byte("be brief")) || !bytes.Contains(result.Bytes, []byte("Hello")) {
		t.Fatalf("converted body does not carry the text: %s", result.Bytes)
	}
}

func TestResponsesResponseToChatTextAndToolCall(t *testing.T) {
	raw := []byte(`{
		"id": "resp_1",
		"object": "response",
		"model": "gpt-5",
		"output": [
			{"type": "message", "role": "assistant", "content": [
				{"type": "output_text", "text": "Calling weather"}
			]},
			{"type": "function_call", "call_id": "call_1", "name": "weather", "arguments": "{\"city\":\"Paris\"}"}
		],
		"usage": {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}
	}`)
	result, err := ConvertResponse(context.Background(), raw, xir.ProtocolResponses, xir.ProtocolChatCompletions, Options{})
	if err != nil {
		t.Fatalf("ConvertResponse: %v", err)
	}
	if len(result.Loss) != 0 {
		t.Fatalf("unexpected losses: %v", result.Loss)
	}
	out := decodeObject(t, result.Bytes)
	choices, ok := out["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("choices = %#v", out["choices"])
	}
	choice := choices[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if message["content"] != "Calling weather" {
		t.Fatalf("message.content = %#v", message["content"])
	}
	calls, ok := message["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("tool_calls = %#v", message["tool_calls"])
	}
	call := calls[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if call["id"] != "call_1" || fn["name"] != "weather" || fn["arguments"] != `{"city":"Paris"}` {
		t.Fatalf("tool call = %#v", call)
	}
	usage := out["usage"].(map[string]any)
	if usage["prompt_tokens"] != 10.0 || usage["completion_tokens"] != 5.0 || usage["total_tokens"] != 15.0 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestMessagesRequestToChatToolLoop(t *testing.T) {
	raw := []byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 128,
		"messages": [
			{"role": "user", "content": "What is the weather?"},
			{"role": "assistant", "content": [
				{"type": "text", "text": "Let me check."},
				{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "Paris"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_1", "content": "sunny"},
				{"type": "text", "text": "Thanks"}
			]}
		]
	}`)
	result, err := ConvertRequest(context.Background(), raw, xir.ProtocolMessages, xir.ProtocolChatCompletions, Options{})
	if err != nil {
		t.Fatalf("ConvertRequest: %v", err)
	}
	if len(result.Loss) != 0 {
		t.Fatalf("unexpected losses: %v", result.Loss)
	}
	out := decodeObject(t, result.Bytes)
	messages, ok := out["messages"].([]any)
	if !ok || len(messages) != 4 {
		t.Fatalf("messages = %#v", out["messages"])
	}
	if messages[0].(map[string]any)["content"] != "What is the weather?" {
		t.Fatalf("messages[0] = %#v", messages[0])
	}
	assistant := messages[1].(map[string]any)
	if assistant["content"] != "Let me check." {
		t.Fatalf("assistant content = %#v", assistant["content"])
	}
	calls := assistant["tool_calls"].([]any)
	call := calls[0].(map[string]any)
	if call["id"] != "toolu_1" || call["function"].(map[string]any)["name"] != "get_weather" {
		t.Fatalf("assistant tool call = %#v", call)
	}
	tool := messages[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "toolu_1" || tool["content"] != "sunny" {
		t.Fatalf("tool message = %#v", tool)
	}
	if messages[3].(map[string]any)["role"] != "user" || messages[3].(map[string]any)["content"] != "Thanks" {
		t.Fatalf("messages[3] = %#v", messages[3])
	}
}

func TestGeminiRequestToChatTextAndModelOverride(t *testing.T) {
	raw := []byte(`{
		"contents": [{"role": "user", "parts": [{"text": "Hello from Gemini"}]}],
		"generationConfig": {"temperature": 0.4}
	}`)
	result, err := ConvertRequest(context.Background(), raw, xir.ProtocolGemini, xir.ProtocolChatCompletions, Options{Model: "gemini-2.5-pro"})
	if err != nil {
		t.Fatalf("ConvertRequest: %v", err)
	}
	out := decodeObject(t, result.Bytes)
	if out["model"] != "gemini-2.5-pro" {
		t.Fatalf("model = %#v", out["model"])
	}
	if out["temperature"] != 0.4 {
		t.Fatalf("temperature = %#v", out["temperature"])
	}
	messages := out["messages"].([]any)
	message := messages[0].(map[string]any)
	if message["role"] != "user" || message["content"] != "Hello from Gemini" {
		t.Fatalf("message = %#v", message)
	}
}

func TestStatefulResponsesToChatRejectedUnderStrict(t *testing.T) {
	raw := []byte(`{"model":"gpt-5","input":"continue","previous_response_id":"resp_1"}`)
	result, err := ConvertRequest(context.Background(), raw, xir.ProtocolResponses, xir.ProtocolChatCompletions, Options{})
	if !errors.Is(err, ErrFatalLoss) {
		t.Fatalf("error = %v, want ErrFatalLoss", err)
	}
	var fatal *FatalLossError
	if !errors.As(err, &fatal) || len(fatal.Losses) == 0 {
		t.Fatalf("errors.As FatalLossError = %v", err)
	}
	if result.Bytes != nil {
		t.Fatalf("result bytes = %s, want nil on rejection", result.Bytes)
	}
	if !hasLoss(result.Loss, "state.previous_response_id", true) {
		t.Fatalf("losses = %#v", result.Loss)
	}

	warned, err := ConvertRequest(context.Background(), raw, xir.ProtocolResponses, xir.ProtocolChatCompletions, Options{Policy: Warn})
	if err != nil {
		t.Fatalf("warn: %v", err)
	}
	if len(warned.Bytes) == 0 {
		t.Fatal("warn returned no body")
	}
	if !hasLoss(warned.Loss, "state.previous_response_id", true) {
		t.Fatalf("warn losses = %#v", warned.Loss)
	}
	out := decodeObject(t, warned.Bytes)
	if out["model"] != "gpt-5" {
		t.Fatalf("model = %#v", out["model"])
	}
}

func TestStreamingConversionRejected(t *testing.T) {
	raw := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	_, err := ConvertRequest(context.Background(), raw, xir.ProtocolChatCompletions, xir.ProtocolResponses, Options{Stream: true})
	if !errors.Is(err, ErrStreamingConversion) {
		t.Fatalf("Options.Stream error = %v, want ErrStreamingConversion", err)
	}
	var streaming *StreamingConversionError
	if !errors.As(err, &streaming) {
		t.Fatalf("errors.As StreamingConversionError = %v", err)
	}

	streamed := []byte(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	_, err = ConvertRequest(context.Background(), streamed, xir.ProtocolChatCompletions, xir.ProtocolResponses, Options{})
	if !errors.Is(err, ErrStreamingConversion) {
		t.Fatalf("in-body stream error = %v, want ErrStreamingConversion", err)
	}

	sse := []byte("data: {\"id\":\"r1\"}\n\n")
	_, err = ConvertResponse(context.Background(), sse, xir.ProtocolResponses, xir.ProtocolChatCompletions, Options{})
	if !errors.Is(err, ErrStreamingConversion) {
		t.Fatalf("SSE response error = %v, want ErrStreamingConversion", err)
	}
}

func TestLossPolicyStrictWarnDrop(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hi"}],
		"response_format": {"type": "json_object"}
	}`)
	_, err := ConvertRequest(context.Background(), raw, xir.ProtocolChatCompletions, xir.ProtocolMessages, Options{})
	if !errors.Is(err, ErrFatalLoss) {
		t.Fatalf("strict error = %v, want ErrFatalLoss", err)
	}
	_, err = ConvertRequest(context.Background(), raw, xir.ProtocolChatCompletions, xir.ProtocolMessages, Options{Policy: Drop})
	if !errors.Is(err, ErrFatalLoss) {
		t.Fatalf("drop error = %v, want ErrFatalLoss for a fatal loss", err)
	}

	warned, err := ConvertRequest(context.Background(), raw, xir.ProtocolChatCompletions, xir.ProtocolMessages, Options{Policy: Warn})
	if err != nil {
		t.Fatalf("warn: %v", err)
	}
	if !hasLoss(warned.Loss, "params.response_format", true) {
		t.Fatalf("warn losses = %#v", warned.Loss)
	}
	if len(warned.Bytes) == 0 {
		t.Fatal("warn returned no body")
	}
}

func TestNonFatalLossDropSuppressesLedger(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hi"}],
		"prompt_cache_key": "cache-1"
	}`)
	strict, err := ConvertRequest(context.Background(), raw, xir.ProtocolChatCompletions, xir.ProtocolMessages, Options{})
	if err != nil {
		t.Fatalf("strict: %v", err)
	}
	if !hasLoss(strict.Loss, "state.prompt_cache_key", false) {
		t.Fatalf("strict losses = %#v", strict.Loss)
	}
	if len(strict.Bytes) == 0 {
		t.Fatal("strict returned no body")
	}

	dropped, err := ConvertRequest(context.Background(), raw, xir.ProtocolChatCompletions, xir.ProtocolMessages, Options{Policy: Drop})
	if err != nil {
		t.Fatalf("drop: %v", err)
	}
	if len(dropped.Loss) != 0 {
		t.Fatalf("drop losses = %#v, want none", dropped.Loss)
	}
	if len(dropped.Bytes) == 0 {
		t.Fatal("drop returned no body")
	}
}

func TestCodecMissing(t *testing.T) {
	_, err := ConvertRequest(context.Background(), []byte(`{}`), xir.Protocol("bogus"), xir.ProtocolChatCompletions, Options{})
	if !errors.Is(err, ErrCodecMissing) {
		t.Fatalf("error = %v, want ErrCodecMissing", err)
	}
	var missing *CodecMissingError
	if !errors.As(err, &missing) || missing.Direction != "source" {
		t.Fatalf("errors.As CodecMissingError = %#v", err)
	}
}

func decodeObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode JSON: %v (%s)", err, raw)
	}
	return out
}

func hasLoss(losses []xir.Loss, field string, fatal bool) bool {
	for _, loss := range losses {
		if loss.Field == field && loss.Fatal == fatal {
			return true
		}
	}
	return false
}

func TestStreamingConversionRejectedForEveryProtocolPair(t *testing.T) {
	protocols := []xir.Protocol{
		xir.ProtocolChatCompletions,
		xir.ProtocolResponses,
		xir.ProtocolMessages,
		xir.ProtocolGemini,
	}
	for _, from := range protocols {
		for _, to := range protocols {
			if from == to {
				continue
			}
			_, err := ConvertRequest(context.Background(), []byte(`{"model":"m","stream":true}`), from, to, Options{Stream: true})
			if !errors.Is(err, ErrStreamingConversion) {
				t.Fatalf("%s -> %s: err = %v, want ErrStreamingConversion", from, to, err)
			}
		}
	}
}
