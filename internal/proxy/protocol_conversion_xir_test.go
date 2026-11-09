package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/protocolconvert"
)

func TestXIRRequestConversionChatToResponses(t *testing.T) {
	conv := &protocolConversion{
		clientProtocol:   protocolconvert.ProtocolChatCompletions,
		upstreamProtocol: protocolconvert.ProtocolResponses,
		wireModel:        "m",
		engine:           "xir",
	}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, err := conv.convertRequestBody(body)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v (%s)", err, out)
	}
	if payload["model"] != "m" {
		t.Fatalf("model = %v", payload["model"])
	}
	if _, ok := payload["input"]; !ok {
		t.Fatalf("converted body has no input: %s", out)
	}
}

func TestXIRResponseConversionResponsesToChat(t *testing.T) {
	conv := &protocolConversion{
		clientProtocol:   protocolconvert.ProtocolChatCompletions,
		upstreamProtocol: protocolconvert.ProtocolResponses,
		wireModel:        "m",
		engine:           "xir",
	}
	body := []byte(`{
		"id":"r1","object":"response","status":"completed","model":"m",
		"output":[{"type":"message","id":"msg","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],
		"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}
	}`)
	out, warnings, err := conv.convertResponseBodyXIR(body)
	if err != nil {
		t.Fatal(err)
	}
	// A response status is a Responses-only field; converting it to Chat
	// reports a non-fatal loss, which must not block the conversion.
	if len(warnings) == 0 {
		t.Fatal("expected the status loss to be reported")
	}
	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("invalid JSON: %v (%s)", err, out)
	}
	if len(payload.Choices) != 1 || payload.Choices[0].Message.Content != "hi" {
		t.Fatalf("choices = %+v (%s)", payload.Choices, out)
	}
	if payload.Usage.PromptTokens != 2 || payload.Usage.CompletionTokens != 1 {
		t.Fatalf("usage = %+v", payload.Usage)
	}
}

func TestXIRRequestConversionRejectsStatefulResponses(t *testing.T) {
	conv := &protocolConversion{
		clientProtocol:   protocolconvert.ProtocolResponses,
		upstreamProtocol: protocolconvert.ProtocolChatCompletions,
		wireModel:        "m",
		engine:           "xir",
	}
	body := []byte(`{"model":"m","input":"hi","previous_response_id":"r1"}`)
	_, err := conv.convertRequestBody(body)
	if err == nil {
		t.Fatal("stateful conversion succeeded, want error")
	}
	if !strings.Contains(err.Error(), "previous_response_id") {
		t.Fatalf("error = %v, want it to name previous_response_id", err)
	}
}

func TestXIRProtocolMapping(t *testing.T) {
	cases := []struct {
		from, to         protocolconvert.Protocol
		wantFrom, wantTo string
	}{
		{protocolconvert.ProtocolChatCompletions, protocolconvert.ProtocolResponses, "chat_completions", "responses"},
		{protocolconvert.ProtocolMessages, protocolconvert.ProtocolGemini, "messages", "gemini"},
	}
	for _, tc := range cases {
		from, to, err := xirProtocols(tc.from, tc.to)
		if err != nil {
			t.Fatal(err)
		}
		if string(from) != tc.wantFrom || string(to) != tc.wantTo {
			t.Fatalf("xirProtocols(%q,%q) = %q,%q", tc.from, tc.to, from, to)
		}
	}
}
