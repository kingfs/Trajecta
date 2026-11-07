package protocolconvert

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDetectFromPath(t *testing.T) {
	cases := map[string]Protocol{
		"/v1/chat/completions": ProtocolChatCompletions,
		"/v1/responses":        ProtocolResponses,
		"/v1/messages":         ProtocolMessages,
		"/v1beta/models/gemini-2.5-flash:generateContent": ProtocolGemini,
	}
	for path, want := range cases {
		got, err := DetectFromPath(path)
		if err != nil {
			t.Fatalf("DetectFromPath(%q): %v", path, err)
		}
		if got != want {
			t.Errorf("DetectFromPath(%q) = %q, want %q", path, got, want)
		}
	}
	if _, err := DetectFromPath("/v1/embeddings"); err == nil {
		t.Error("DetectFromPath(/v1/embeddings) expected error")
	}
}

func TestPlanPassthroughPreferred(t *testing.T) {
	decision, err := Plan(ProtocolResponses, Capabilities{ChatCompletions: true, Responses: true}, ModePassthroughPreferred)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Passthrough() || decision.UpstreamProtocol != ProtocolResponses {
		t.Errorf("native support should passthrough, got %+v", decision)
	}

	decision, err = Plan(ProtocolResponses, Capabilities{ChatCompletions: true, Messages: true}, ModePassthroughPreferred)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Convert() || decision.UpstreamProtocol != ProtocolChatCompletions {
		t.Errorf("expected convert to chat, got %+v", decision)
	}

	decision, err = Plan(ProtocolChatCompletions, Capabilities{Messages: true}, ModePassthroughPreferred)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Convert() || decision.UpstreamProtocol != ProtocolMessages {
		t.Errorf("expected convert to messages, got %+v", decision)
	}
}

func TestPlanPassthroughOnlyAndReject(t *testing.T) {
	decision, err := Plan(ProtocolMessages, Capabilities{ChatCompletions: true}, ModePassthroughOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Reject() {
		t.Errorf("passthrough_only mismatch should reject, got %+v", decision)
	}

	decision, err = Plan(ProtocolChatCompletions, Capabilities{}, ModePassthroughPreferred)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Reject() {
		t.Errorf("no convertible protocol should reject, got %+v", decision)
	}

	if _, err = Plan("", Capabilities{ChatCompletions: true}, ModePassthroughPreferred); err == nil {
		t.Error("empty client protocol should error")
	}
}

func TestConvertRequestChatToMessages(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"Hello"}],"temperature":0.2}`)
	result, err := ConvertRequest(body, ProtocolChatCompletions, ProtocolMessages, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(result.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["model"] != "m" {
		t.Errorf("model = %v", decoded["model"])
	}
	if decoded["max_tokens"] != float64(4096) {
		t.Errorf("max_tokens = %v, want Anthropic default 4096", decoded["max_tokens"])
	}
	if len(result.Body) == 0 {
		t.Error("empty body")
	}
}

func TestConvertRequestMessagesToChat(t *testing.T) {
	body := []byte(`{"model":"claude","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`)
	result, err := ConvertRequest(body, ProtocolMessages, ProtocolChatCompletions, "wire-model", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Body), "wire-model") || !strings.Contains(string(result.Body), "Hi") {
		t.Errorf("converted body = %s", result.Body)
	}
}

func TestConvertRequestChatToGemini(t *testing.T) {
	body := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"Hello"}],"max_tokens":50}`)
	result, err := ConvertRequest(body, ProtocolChatCompletions, ProtocolGemini, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Body), "Hello") {
		t.Errorf("converted body = %s", result.Body)
	}
	if got, err := ProtocolGemini.RequestPath("gemini-2.5-flash", false, false); err != nil || got != "/v1beta/models/gemini-2.5-flash:generateContent" {
		t.Errorf("RequestPath = %q, %v", got, err)
	}
}

func TestConvertResponseChatToResponses(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"Hello there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	result, err := ConvertResponse(body, ProtocolChatCompletions, ProtocolResponses, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Body), "Hello there") || !strings.Contains(string(result.Body), `"object":"response"`) {
		t.Errorf("converted body = %s", result.Body)
	}
}

func TestRoundTripChatResponsesChat(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"Ping"}],"max_tokens":10}`)
	toResponses, err := ConvertRequest(body, ProtocolChatCompletions, ProtocolResponses, "", false)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ConvertRequest(toResponses.Body, ProtocolResponses, ProtocolChatCompletions, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(back.Body), "Ping") {
		t.Errorf("round-trip body = %s", back.Body)
	}
}

func TestParseProtocolAliases(t *testing.T) {
	p, err := ParseProtocol("generate_content")
	if err != nil || p != ProtocolGemini {
		t.Errorf("generate_content -> %q, %v", p, err)
	}
	p, err = ParseProtocol("chat-completions")
	if err != nil || p != ProtocolChatCompletions {
		t.Errorf("chat-completions -> %q, %v", p, err)
	}
	if _, err = ParseProtocol("embeddings"); err == nil {
		t.Error("embeddings should error")
	}
}

func TestPlanRejectsUnknownMode(t *testing.T) {
	if _, err := Plan(ProtocolChatCompletions, Capabilities{Messages: true}, Mode("nope")); err == nil {
		t.Fatal("an unknown conversion mode must be rejected")
	}
}
