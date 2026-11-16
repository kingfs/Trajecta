package convert_test

// Regression tests for the B1-B4 switch blockers and the policy decisions
// taken with them. Each test names the production failure it prevents:
//
//	B1 responses envelope members are not a fatal loss
//	B2 a Messages target always receives max_tokens
//	B3 answer-changing chat fields are carried or reported, and a stream
//	   spelling is not converted as if it were non-streaming
//	B4 Gemini tool calls get ids the next turn can quote, and every
//	   candidate is carried
//
// plus the companion fixes: a response-side sealed reasoning block is dropped
// with a ledger entry instead of failing the response, and top_k is not sent to
// protocols that do not define it.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
	convert "github.com/kingfs/Trajecta/pkg/xir/convert"
)

func decodeBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode converted body: %v (%s)", err, raw)
	}
	return body
}

func lossFields(losses []xir.Loss) map[string]xir.Loss {
	out := make(map[string]xir.Loss, len(losses))
	for _, loss := range losses {
		out[loss.Field] = loss
	}
	return out
}

func TestResponsesEnvelopeIsNotFatal(t *testing.T) {
	// A real Responses envelope always carries created_at and sibling members
	// the IR does not model. Strict used to abort here, so every responses
	// upstream conversion 502'd.
	raw := []byte(`{"id":"resp_1","object":"response","created_at":1,"model":"m","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi there"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)
	result, err := convert.ConvertResponse(context.Background(), raw, xir.ProtocolResponses, xir.ProtocolChatCompletions, convert.Options{Model: "wire-model", Policy: convert.Strict})
	if err != nil {
		t.Fatalf("responses response rejected: %v", err)
	}
	body := decodeBody(t, result.Bytes)
	choices := body["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	if got := message["content"]; got != "Hi there" {
		t.Fatalf("content = %v", got)
	}
	loss, ok := lossFields(result.Loss)["extensions.responses"]
	if !ok || loss.Fatal {
		t.Fatalf("extensions.responses loss = %+v, want a non-fatal ledger entry", loss)
	}
}

func TestMessagesTargetGetsMaxTokens(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		raw := []byte(`{"model":"m","messages":[{"role":"user","content":"Hello"}]}`)
		result, err := convert.ConvertRequest(context.Background(), raw, xir.ProtocolChatCompletions, xir.ProtocolMessages, convert.Options{Model: "wire-model", Policy: convert.Strict})
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		body := decodeBody(t, result.Bytes)
		if got := body["max_tokens"]; got != float64(4096) {
			t.Fatalf("max_tokens = %v, want 4096", got)
		}
	})
	t.Run("max_completion_tokens maps", func(t *testing.T) {
		raw := []byte(`{"model":"m","max_completion_tokens":77,"messages":[{"role":"user","content":"Hello"}]}`)
		result, err := convert.ConvertRequest(context.Background(), raw, xir.ProtocolChatCompletions, xir.ProtocolMessages, convert.Options{Model: "wire-model", Policy: convert.Strict})
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		body := decodeBody(t, result.Bytes)
		if got := body["max_tokens"]; got != float64(77) {
			t.Fatalf("max_tokens = %v, want 77", got)
		}
	})
}

func TestAnswerChangingChatFieldsAreReported(t *testing.T) {
	cases := []struct {
		name string
		body string
		to   xir.Protocol
		loss string
	}{
		{"n", `{"model":"m","n":2,"messages":[{"role":"user","content":"hi"}]}`, xir.ProtocolMessages, "params.n"},
		{"logit_bias", `{"model":"m","logit_bias":{"50256":-100},"messages":[{"role":"user","content":"hi"}]}`, xir.ProtocolGemini, "params.logit_bias"},
		{"unknown_tool_choice", `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"auto","tools":[]}}}`, xir.ProtocolGemini, "tool_choice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := convert.ConvertRequest(context.Background(), []byte(tc.body), xir.ProtocolChatCompletions, tc.to, convert.Options{Model: "wire-model", Policy: convert.Strict})
			var fatal *convert.FatalLossError
			if !errors.As(err, &fatal) {
				t.Fatalf("err = %v, want FatalLossError", err)
			}
			if _, ok := lossFields(fatal.Losses)[tc.loss]; !ok {
				t.Fatalf("fatal losses = %+v, want %s", fatal.Losses, tc.loss)
			}
		})
	}
}

func TestStreamSpellingsRejectConversion(t *testing.T) {
	for _, stream := range []string{`true`, `"true"`, `1`} {
		raw := []byte(`{"model":"m","stream":` + stream + `,"messages":[{"role":"user","content":"hi"}]}`)
		_, err := convert.ConvertRequest(context.Background(), raw, xir.ProtocolChatCompletions, xir.ProtocolMessages, convert.Options{Model: "wire-model", Policy: convert.Strict})
		if !errors.Is(err, convert.ErrStreamingConversion) {
			t.Fatalf("stream=%s err = %v, want ErrStreamingConversion", stream, err)
		}
	}
}

func TestGeminiToolCallsGetIDs(t *testing.T) {
	// A Gemini response identifies a call by name only. Converting it used to
	// emit tool_calls without an id, so the client could not reference the
	// call in the next turn.
	raw := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"weather","args":{"city":"SH"}}},{"functionCall":{"name":"weather","args":{"city":"BJ"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`)
	result, err := convert.ConvertResponse(context.Background(), raw, xir.ProtocolGemini, xir.ProtocolChatCompletions, convert.Options{Model: "wire-model", Policy: convert.Strict})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	body := decodeBody(t, result.Bytes)
	choices := body["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	calls := message["tool_calls"].([]any)
	if len(calls) != 2 {
		t.Fatalf("tool_calls = %v, want 2", calls)
	}
	ids := map[string]bool{}
	for _, call := range calls {
		id, _ := call.(map[string]any)["id"].(string)
		if id == "" {
			t.Fatalf("tool call has no id: %v", call)
		}
		ids[id] = true
	}
	if len(ids) != 2 {
		t.Fatalf("tool call ids are not unique: %v", ids)
	}
	if finish := choices[0].(map[string]any)["finish_reason"]; finish != "tool_calls" {
		t.Fatalf("finish_reason = %v", finish)
	}
}

func TestGeminiToolResultMatchesCallID(t *testing.T) {
	raw := []byte(`{"contents":[{"role":"user","parts":[{"text":"weather?"}]},{"role":"model","parts":[{"functionCall":{"name":"weather","args":{"city":"SH"}}}]},{"role":"user","parts":[{"functionResponse":{"name":"weather","response":{"result":"sunny"}}}]}]}`)
	result, err := convert.ConvertRequest(context.Background(), raw, xir.ProtocolGemini, xir.ProtocolChatCompletions, convert.Options{Model: "wire-model", Policy: convert.Strict})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	body := decodeBody(t, result.Bytes)
	messages := body["messages"].([]any)
	var callID, resultID string
	for _, rawMessage := range messages {
		message := rawMessage.(map[string]any)
		if calls, ok := message["tool_calls"].([]any); ok && len(calls) > 0 {
			callID, _ = calls[0].(map[string]any)["id"].(string)
		}
		if id, ok := message["tool_call_id"].(string); ok {
			resultID = id
		}
	}
	if callID == "" || resultID == "" || callID != resultID {
		t.Fatalf("call id %q != tool result id %q", callID, resultID)
	}
}

func TestGeminiCandidatesBecomeChoices(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"first"}]},"finishReason":"STOP","index":0},{"content":{"role":"model","parts":[{"text":"second"}]},"finishReason":"STOP","index":1}]}`)
	result, err := convert.ConvertResponse(context.Background(), raw, xir.ProtocolGemini, xir.ProtocolChatCompletions, convert.Options{Model: "wire-model", Policy: convert.Strict})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	body := decodeBody(t, result.Bytes)
	choices := body["choices"].([]any)
	if len(choices) != 2 {
		t.Fatalf("choices = %d, want 2", len(choices))
	}
	second := choices[1].(map[string]any)["message"].(map[string]any)["content"]
	if second != "second" {
		t.Fatalf("second choice content = %v", second)
	}
}

func TestSealedResponseDropsInsteadOfFailing(t *testing.T) {
	// A thinking model's response is otherwise valid: the protocol-owned
	// thinking block must be dropped with a ledger entry, not fail the whole
	// conversion (legacy dropped it and answered).
	raw := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"thinking","thinking":"plan","signature":"sig-1"},{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	result, err := convert.ConvertResponse(context.Background(), raw, xir.ProtocolMessages, xir.ProtocolChatCompletions, convert.Options{Model: "wire-model", Policy: convert.Strict})
	if err != nil {
		t.Fatalf("sealed response rejected: %v", err)
	}
	body := decodeBody(t, result.Bytes)
	choices := body["choices"].([]any)
	content := choices[0].(map[string]any)["message"].(map[string]any)["content"]
	if content != "answer" {
		t.Fatalf("content = %v, want the answer text", content)
	}
	loss, ok := lossFields(result.Loss)["response[0].signature"]
	if !ok || loss.Fatal {
		t.Fatalf("signature loss = %+v, want non-fatal", loss)
	}
}

func TestTopKNotSentToChat(t *testing.T) {
	raw := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"topK":5}}`)
	result, err := convert.ConvertRequest(context.Background(), raw, xir.ProtocolGemini, xir.ProtocolChatCompletions, convert.Options{Model: "wire-model", Policy: convert.Strict})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	body := decodeBody(t, result.Bytes)
	if _, ok := body["top_k"]; ok {
		t.Fatalf("chat body carries top_k: %s", result.Bytes)
	}
	if _, ok := lossFields(result.Loss)["params.top_k"]; !ok {
		t.Fatalf("losses = %+v, want params.top_k", result.Loss)
	}
}
