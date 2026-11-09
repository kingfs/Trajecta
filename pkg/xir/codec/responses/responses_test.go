package responses

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
)

const fullRequestJSON = `{
  "model": "gpt-5",
  "instructions": "be brief",
  "input": [
    {"type":"message","role":"user","content":[{"type":"input_text","text":"hello"},{"type":"input_image","image_url":"https://example.com/a.png","detail":"high"}]},
    {"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"id\":1}"},
    {"type":"function_call_output","call_id":"call_1","name":"lookup","output":"{\"ok\":true}"},
    {"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thought"}],"encrypted_content":"gAAAA"}
  ],
  "tools": [
    {"type":"function","name":"lookup","description":"d","parameters":{"type":"object"},"strict":true},
    {"type":"custom","name":"shell","description":"run","format":{"type":"grammar","syntax":"lark","definition":"start: x"}}
  ],
  "tool_choice": {"type":"function","name":"lookup"},
  "max_output_tokens": 256,
  "temperature": 0.2,
  "top_p": 0.9,
  "top_k": 5,
  "stream": true,
  "text": {"format":{"type":"json_schema","name":"x","schema":{"type":"object"},"strict":true},"verbosity":"low"},
  "reasoning": {"effort":"high","summary":"auto"},
  "previous_response_id": "resp_1",
  "conversation": {"id":"conv_1"},
  "prompt_cache_key": "cache-1",
  "metadata": {"trace":"abc"},
  "store": false,
  "include": ["reasoning.encrypted_content"],
  "truncation": "auto",
  "parallel_tool_calls": true,
  "unknown_field": {"x":1}
}`

const fullResponseJSON = `{
  "id": "resp_1",
  "object": "response",
  "created_at": 1741476542,
  "status": "incomplete",
  "model": "gpt-5",
  "incomplete_details": {"reason":"max_output_tokens"},
  "output": [
    {"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[
      {"type":"output_text","text":"hello","annotations":[]},
      {"type":"refusal","refusal":"no"}
    ]},
    {"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"q\":1}","status":"completed"},
    {"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thinking"}],"encrypted_content":"gAAAA"},
    {"type":"web_search_call","id":"ws_1","status":"completed"}
  ],
  "usage": {
    "input_tokens": 10,
    "input_tokens_details": {"cached_tokens": 2},
    "output_tokens": 5,
    "output_tokens_details": {"reasoning_tokens": 3},
    "total_tokens": 15
  },
  "error": null,
  "previous_response_id": "resp_0",
  "metadata": {"k":"v"}
}`

func testDoc(t *testing.T, raw string) *xir.Document {
	t.Helper()
	doc, err := xir.ParseDocument(xir.ProtocolResponses, xir.OperationResponses, []byte(raw))
	if err != nil {
		t.Fatalf("parse document: %v", err)
	}
	return doc
}

func decodeRequestRaw(t *testing.T, raw string) *xir.Request {
	t.Helper()
	req, err := Codec{}.DecodeRequest(context.Background(), testDoc(t, raw), xir.DecodeOptions{})
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	return req
}

func decodeResponseRaw(t *testing.T, raw string) *xir.Response {
	t.Helper()
	resp, err := Codec{}.DecodeResponse(context.Background(), testDoc(t, raw), xir.DecodeOptions{})
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	return resp
}

func encodeRequestObject(t *testing.T, req *xir.Request, opts xir.EncodeOptions) map[string]json.RawMessage {
	t.Helper()
	raw, err := Codec{}.EncodeRequest(context.Background(), req, opts)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatalf("EncodeRequest produced invalid JSON: %s", raw)
	}
	return objectFromRaw(t, raw)
}

func encodeResponseObject(t *testing.T, resp *xir.Response, opts xir.EncodeOptions) map[string]json.RawMessage {
	t.Helper()
	raw, err := Codec{}.EncodeResponse(context.Background(), resp, opts)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatalf("EncodeResponse produced invalid JSON: %s", raw)
	}
	return objectFromRaw(t, raw)
}

func objectFromRaw(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("decode object: %v (%s)", err, raw)
	}
	return obj
}

func rawObject(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("decode object: %v (%s)", err, raw)
	}
	return obj
}

func rawArray(t *testing.T, raw json.RawMessage) []json.RawMessage {
	t.Helper()
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("decode array: %v (%s)", err, raw)
	}
	return items
}

func rawText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode string: %v (%s)", err, raw)
	}
	return s
}

func rawLiteral(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	return strings.TrimSpace(string(raw))
}

func extraString(t *testing.T, params xir.Params, key string) string {
	t.Helper()
	raw, ok := params.Extra[key]
	if !ok {
		t.Fatalf("Params.Extra[%q] missing", key)
	}
	return rawText(t, raw)
}

func commonFields(t *testing.T, obj map[string]json.RawMessage, key string) map[string]json.RawMessage {
	t.Helper()
	raw, ok := obj[key]
	if !ok {
		t.Fatalf("encoded object is missing %q: %v", key, obj)
	}
	return rawObject(t, raw)
}

func requireKey(t *testing.T, obj map[string]json.RawMessage, key string) json.RawMessage {
	t.Helper()
	raw, ok := obj[key]
	if !ok {
		t.Fatalf("missing key %q in %v", key, obj)
	}
	return raw
}

func TestRequestRoundTrips(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		check func(t *testing.T, req *xir.Request)
	}{
		{
			name: "string input",
			raw:  `{"model":"gpt-5","input":"hello"}`,
			check: func(t *testing.T, req *xir.Request) {
				if req.Model != "gpt-5" {
					t.Fatalf("model = %q", req.Model)
				}
				if len(req.Messages) != 1 || req.Messages[0].Role != xir.RoleUser {
					t.Fatalf("messages = %+v", req.Messages)
				}
				if len(req.Messages[0].Parts) != 1 || req.Messages[0].Parts[0].Text != "hello" {
					t.Fatalf("parts = %+v", req.Messages[0].Parts)
				}
			},
		},
		{
			name: "instructions string",
			raw:  `{"model":"m","instructions":"be nice","input":"hi"}`,
			check: func(t *testing.T, req *xir.Request) {
				if len(req.System) != 1 || req.System[0].Kind != xir.PartText || req.System[0].Text != "be nice" {
					t.Fatalf("system = %+v", req.System)
				}
			},
		},
		{
			name: "instructions array",
			raw:  `{"model":"m","instructions":[{"type":"input_text","text":"a"},{"type":"input_text","text":"b"}],"input":[]}`,
			check: func(t *testing.T, req *xir.Request) {
				if len(req.System) != 2 {
					t.Fatalf("system = %+v", req.System)
				}
				if req.System[0].Text != "a" || req.System[1].Text != "b" {
					t.Fatalf("system = %+v", req.System)
				}
			},
		},
		{
			name: "tool call and array tool result",
			raw: `{"model":"m","input":[
				{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},
				{"type":"function_call_output","call_id":"c1","output":[{"type":"input_text","text":"a"},{"type":"input_text","text":"b"}]}]}`,
			check: func(t *testing.T, req *xir.Request) {
				if len(req.Messages) != 2 {
					t.Fatalf("messages = %+v", req.Messages)
				}
				call := req.Messages[0].Parts[0].ToolCall
				if call == nil || call.ID != "c1" || call.Name != "f" {
					t.Fatalf("call = %+v", call)
				}
				result := req.Messages[1].Parts[0].ToolResult
				if result == nil || result.CallID != "c1" {
					t.Fatalf("result = %+v", result)
				}
				if len(result.Content) != 2 || result.Text != "a\nb" {
					t.Fatalf("result content = %+v text = %q", result.Content, result.Text)
				}
			},
		},
		{
			name: "unknown input item",
			raw:  `{"model":"m","input":[{"type":"item_reference","id":"x"}]}`,
			check: func(t *testing.T, req *xir.Request) {
				if len(req.Messages) != 1 || len(req.Messages[0].Parts) != 1 {
					t.Fatalf("messages = %+v", req.Messages)
				}
				part := req.Messages[0].Parts[0]
				if part.Kind != xir.PartUnknown || part.Sealed == nil {
					t.Fatalf("part = %+v", part)
				}
				if part.Sealed.Protocol != xir.ProtocolResponses || part.Sealed.Kind != "item_reference" {
					t.Fatalf("sealed = %+v", part.Sealed)
				}
			},
		},
		{
			name: "unrepresentable tool choice preserved",
			raw:  `{"model":"m","input":"hi","tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"f"}]}}`,
			check: func(t *testing.T, req *xir.Request) {
				if req.ToolChoice != (xir.ToolChoice{}) {
					t.Fatalf("tool choice = %+v", req.ToolChoice)
				}
				extras := requestExtras(t, req)
				if _, ok := extras["tool_choice"]; !ok {
					t.Fatalf("tool_choice not preserved in extras: %v", extras)
				}
			},
		},
		{
			name:  "full request",
			raw:   fullRequestJSON,
			check: checkFullRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := decodeRequestRaw(t, tc.raw)
			if original.Ref == nil {
				t.Fatal("decoded request has no Ref")
			}
			tc.check(t, original)

			encoded := encodeRequestObject(t, original, xir.EncodeOptions{})
			roundTripped := decodeRequestRaw(t, string(mustMarshal(t, encoded)))
			tc.check(t, roundTripped)
		})
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func requestExtras(t *testing.T, req *xir.Request) map[string]json.RawMessage {
	t.Helper()
	raw, ok := req.Extensions[ExtensionKey]
	if !ok {
		return nil
	}
	return rawObject(t, raw)
}

func TestDecodeRequestFull(t *testing.T) {
	req := decodeRequestRaw(t, fullRequestJSON)
	checkFullRequest(t, req)

	// Refs point at wire nodes so hooks can edit by span.
	if req.Ref == nil || req.Messages[1].Ref == nil || req.Messages[1].Parts[0].ToolCall.Ref == nil {
		t.Fatalf("missing refs: req=%v message=%v call=%v",
			req.Ref, req.Messages[1].Ref, req.Messages[1].Parts[0].ToolCall.Ref)
	}
}

func checkFullRequest(t *testing.T, req *xir.Request) {
	t.Helper()
	if req.Model != "gpt-5" {
		t.Fatalf("model = %q", req.Model)
	}
	if len(req.System) != 1 || req.System[0].Kind != xir.PartText || req.System[0].Text != "be brief" {
		t.Fatalf("system = %+v", req.System)
	}
	if len(req.Messages) != 4 {
		t.Fatalf("messages = %d: %+v", len(req.Messages), req.Messages)
	}

	user := req.Messages[0]
	if user.Role != xir.RoleUser || len(user.Parts) != 2 {
		t.Fatalf("user message = %+v", user)
	}
	if user.Parts[0].Kind != xir.PartText || user.Parts[0].Text != "hello" {
		t.Fatalf("user text = %+v", user.Parts[0])
	}
	if user.Parts[1].Kind != xir.PartImage || user.Parts[1].Media == nil {
		t.Fatalf("user image = %+v", user.Parts[1])
	}
	if user.Parts[1].Media.URL != "https://example.com/a.png" || user.Parts[1].Media.Detail != "high" {
		t.Fatalf("user image media = %+v", user.Parts[1].Media)
	}

	callMessage := req.Messages[1]
	if callMessage.Role != xir.RoleAssistant || len(callMessage.Parts) != 1 {
		t.Fatalf("function call message = %+v", callMessage)
	}
	call := callMessage.Parts[0].ToolCall
	if call == nil || call.ID != "call_1" || call.Name != "lookup" || call.ArgsText != `{"id":1}` {
		t.Fatalf("tool call = %+v", call)
	}
	if !json.Valid(call.Args) {
		t.Fatalf("tool call Args is not valid JSON: %q", call.Args)
	}
	if call.Ref == nil {
		t.Fatal("tool call has no Ref")
	}

	resultMessage := req.Messages[2]
	if resultMessage.Role != xir.RoleTool || len(resultMessage.Parts) != 1 {
		t.Fatalf("tool result message = %+v", resultMessage)
	}
	result := resultMessage.Parts[0].ToolResult
	if result == nil || result.CallID != "call_1" || result.Name != "lookup" || result.Text != `{"ok":true}` {
		t.Fatalf("tool result = %+v", result)
	}
	if !json.Valid(result.JSON) {
		t.Fatalf("tool result JSON is not valid JSON: %q", result.JSON)
	}

	reasoningMessage := req.Messages[3]
	if reasoningMessage.Role != xir.RoleAssistant || len(reasoningMessage.Parts) != 1 {
		t.Fatalf("reasoning message = %+v", reasoningMessage)
	}
	reasoning := reasoningMessage.Parts[0].Reasoning
	if reasoning == nil {
		t.Fatalf("reasoning part = %+v", reasoningMessage.Parts[0])
	}
	if !reflect.DeepEqual(reasoning.Summary, []string{"thought"}) {
		t.Fatalf("reasoning summary = %v", reasoning.Summary)
	}
	if reasoning.EncryptedContent != "gAAAA" {
		t.Fatalf("encrypted content = %q", reasoning.EncryptedContent)
	}
	if reasoning.Sealed == nil || reasoning.Sealed.Protocol != xir.ProtocolResponses || reasoning.Sealed.Kind != "reasoning" {
		t.Fatalf("reasoning sealed = %+v", reasoning.Sealed)
	}

	if len(req.Tools) != 2 {
		t.Fatalf("tools = %+v", req.Tools)
	}
	if req.Tools[0].Kind != "function" || req.Tools[0].Name != "lookup" ||
		req.Tools[0].Description != "d" || req.Tools[0].Strict == nil || !*req.Tools[0].Strict {
		t.Fatalf("function tool = %+v", req.Tools[0])
	}
	if string(req.Tools[0].Schema) != `{"type":"object"}` {
		t.Fatalf("function tool schema = %s", req.Tools[0].Schema)
	}
	if req.Tools[1].Kind != "custom" || req.Tools[1].Name != "shell" {
		t.Fatalf("custom tool = %+v", req.Tools[1])
	}
	if req.ToolChoice != (xir.ToolChoice{Mode: "tool", Name: "lookup"}) {
		t.Fatalf("tool choice = %+v", req.ToolChoice)
	}

	params := req.Params
	if params.MaxTokens == nil || *params.MaxTokens != 256 {
		t.Fatalf("max tokens = %v", params.MaxTokens)
	}
	if params.Temperature == nil || *params.Temperature != 0.2 {
		t.Fatalf("temperature = %v", params.Temperature)
	}
	if params.TopP == nil || *params.TopP != 0.9 {
		t.Fatalf("top_p = %v", params.TopP)
	}
	if params.TopK == nil || *params.TopK != 5 {
		t.Fatalf("top_k = %v", params.TopK)
	}
	if params.ParallelTools == nil || !*params.ParallelTools {
		t.Fatalf("parallel_tool_calls = %v", params.ParallelTools)
	}
	if params.ReasoningEffort != "high" {
		t.Fatalf("reasoning effort = %q", params.ReasoningEffort)
	}
	if !json.Valid(params.ResponseFormat) || !strings.Contains(string(params.ResponseFormat), "json_schema") {
		t.Fatalf("response format = %s", params.ResponseFormat)
	}
	if got := extraString(t, params, ParamReasoningSummary); got != "auto" {
		t.Fatalf("reasoning summary extra = %q", got)
	}
	if got := extraString(t, params, ParamTextVerbosity); got != "low" {
		t.Fatalf("text verbosity extra = %q", got)
	}
	if !json.Valid(params.Metadata) {
		t.Fatalf("metadata = %s", params.Metadata)
	}

	if req.State.PreviousResponseID != "resp_1" || req.State.ConversationID != "conv_1" ||
		req.State.PromptCacheKey != "cache-1" {
		t.Fatalf("state = %+v", req.State)
	}
	if req.State.Store == nil || *req.State.Store {
		t.Fatalf("store = %v", req.State.Store)
	}
	if !req.Stream {
		t.Fatal("stream = false")
	}

	extras := requestExtras(t, req)
	if _, ok := extras["model"]; ok {
		t.Fatalf("model leaked into extras: %v", extras)
	}
	for _, key := range []string{"include", "truncation", "unknown_field"} {
		if _, ok := extras[key]; !ok {
			t.Fatalf("extra %q missing: %v", key, extras)
		}
	}
}

func TestEncodeRequestShape(t *testing.T) {
	strict := true
	temperature := 0.5
	req := &xir.Request{
		Model: "gpt-5",
		Messages: []xir.Message{
			{Role: xir.RoleUser, Parts: []xir.Part{{Kind: xir.PartText, Text: "hi"}}},
			{Role: xir.RoleAssistant, Parts: []xir.Part{
				{Kind: xir.PartToolCall, ToolCall: &xir.ToolCall{ID: "call_1", Name: "f", ArgsText: `{"a":1}`}},
				{Kind: xir.PartToolResult, ToolResult: &xir.ToolResult{CallID: "call_1", Text: "ok"}},
				{Kind: xir.PartReasoning, Reasoning: &xir.Reasoning{
					Summary:          []string{"s"},
					EncryptedContent: "enc",
				}},
			}},
		},
		Tools: []xir.Tool{{
			Kind:        "function",
			Name:        "f",
			Description: "d",
			Schema:      json.RawMessage(`{"type":"object"}`),
			Strict:      &strict,
		}},
		ToolChoice: xir.ToolChoice{Mode: "tool", Name: "f"},
		Params:     xir.Params{Temperature: &temperature},
		Stream:     true,
	}

	obj := encodeRequestObject(t, req, xir.EncodeOptions{})

	tools := rawArray(t, requireKey(t, obj, "tools"))
	if len(tools) != 1 {
		t.Fatalf("tools = %s", tools)
	}
	tool := rawObject(t, tools[0])
	if _, ok := tool["function"]; ok {
		t.Fatalf("tool must stay flat, got nested function: %v", tool)
	}
	if rawText(t, requireKey(t, tool, "type")) != "function" {
		t.Fatalf("tool type = %v", tool)
	}
	if rawText(t, requireKey(t, tool, "name")) != "f" {
		t.Fatalf("tool name = %v", tool)
	}

	choice := rawObject(t, requireKey(t, obj, "tool_choice"))
	if rawText(t, requireKey(t, choice, "type")) != "function" || rawText(t, requireKey(t, choice, "name")) != "f" {
		t.Fatalf("tool choice = %v", choice)
	}
	if rawLiteral(t, requireKey(t, obj, "stream")) != "true" {
		t.Fatalf("stream = %v", obj["stream"])
	}

	input := rawArray(t, requireKey(t, obj, "input"))
	var types []string
	for _, raw := range input {
		item := rawObject(t, raw)
		types = append(types, rawText(t, requireKey(t, item, "type")))
	}
	want := []string{"message", "function_call", "function_call_output", "reasoning"}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("input item types = %v, want %v", types, want)
	}
	reasoning := rawObject(t, input[3])
	if rawText(t, requireKey(t, reasoning, "encrypted_content")) != "enc" {
		t.Fatalf("reasoning = %v", reasoning)
	}
	if summary := rawArray(t, requireKey(t, reasoning, "summary")); len(summary) != 1 {
		t.Fatalf("reasoning summary = %s", summary)
	}

	// Absent parameters are not invented, and EncodeOptions.Stream can mark
	// an otherwise non-streaming request.
	plain := &xir.Request{Model: "m", Messages: req.Messages[:1]}
	plainObj := encodeRequestObject(t, plain, xir.EncodeOptions{Stream: true})
	for _, key := range []string{"temperature", "top_p", "tools", "tool_choice"} {
		if _, ok := plainObj[key]; ok {
			t.Fatalf("unexpected %q in encoded request: %v", key, plainObj)
		}
	}
	if rawLiteral(t, requireKey(t, plainObj, "stream")) != "true" {
		t.Fatalf("stream = %v", plainObj["stream"])
	}

	// EncodeOptions.Model overrides the request model.
	override := encodeRequestObject(t, plain, xir.EncodeOptions{Model: "upstream-model"})
	if rawText(t, requireKey(t, override, "model")) != "upstream-model" {
		t.Fatalf("model = %v", override["model"])
	}
}

func TestDecodeRequestVariants(t *testing.T) {
	t.Run("empty object is tolerated", func(t *testing.T) {
		req := decodeRequestRaw(t, `{}`)
		if req.Model != "" || len(req.Messages) != 0 || len(req.Tools) != 0 {
			t.Fatalf("request = %+v", req)
		}
	})

	t.Run("wrong optional field types are tolerated", func(t *testing.T) {
		req := decodeRequestRaw(t, `{"model":7,"input":42,"tools":{},"tool_choice":true,"stream":"yes","temperature":"hot"}`)
		if req.Model != "" || len(req.Messages) != 0 || len(req.Tools) != 0 {
			t.Fatalf("request = %+v", req)
		}
		if req.Params.Temperature != nil {
			t.Fatalf("temperature = %v", req.Params.Temperature)
		}
	})

	t.Run("conversation string", func(t *testing.T) {
		req := decodeRequestRaw(t, `{"model":"m","conversation":"conv_x"}`)
		if req.State.ConversationID != "conv_x" {
			t.Fatalf("state = %+v", req.State)
		}
	})

	t.Run("nested legacy function tool", func(t *testing.T) {
		req := decodeRequestRaw(t, `{"model":"m","tools":[{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object"}}}]}`)
		if len(req.Tools) != 1 || req.Tools[0].Name != "f" || req.Tools[0].Description != "d" {
			t.Fatalf("tools = %+v", req.Tools)
		}
		if !json.Valid(req.Tools[0].Schema) {
			t.Fatalf("schema = %s", req.Tools[0].Schema)
		}
	})
}

func TestDecodeResponseFull(t *testing.T) {
	resp := decodeResponseRaw(t, fullResponseJSON)
	checkFullResponse(t, resp)

	if resp.Ref == nil {
		t.Fatal("response has no Ref")
	}
	for i := range resp.Parts {
		if resp.Parts[i].Ref == nil {
			t.Fatalf("part %d has no Ref", i)
		}
	}
}

func checkFullResponse(t *testing.T, resp *xir.Response) {
	t.Helper()
	if resp.ID != "resp_1" || resp.Model != "gpt-5" || resp.Status != "incomplete" {
		t.Fatalf("response envelope = %+v", resp)
	}
	if len(resp.Parts) != 5 {
		t.Fatalf("parts = %d: %+v", len(resp.Parts), resp.Parts)
	}
	if resp.Parts[0].Kind != xir.PartText || resp.Parts[0].Text != "hello" {
		t.Fatalf("text part = %+v", resp.Parts[0])
	}
	if resp.Parts[1].Kind != xir.PartRefusal || resp.Parts[1].Text != "no" {
		t.Fatalf("refusal part = %+v", resp.Parts[1])
	}
	call := resp.Parts[2].ToolCall
	if call == nil || call.ID != "call_1" || call.Name != "lookup" || call.ArgsText != `{"q":1}` {
		t.Fatalf("tool call = %+v", call)
	}
	reasoning := resp.Parts[3].Reasoning
	if reasoning == nil || !reflect.DeepEqual(reasoning.Summary, []string{"thinking"}) ||
		reasoning.EncryptedContent != "gAAAA" {
		t.Fatalf("reasoning = %+v", reasoning)
	}
	if reasoning.Sealed == nil || reasoning.Sealed.Protocol != xir.ProtocolResponses || reasoning.Sealed.Kind != "reasoning" {
		t.Fatalf("reasoning sealed = %+v", reasoning.Sealed)
	}
	unknown := resp.Parts[4]
	if unknown.Kind != xir.PartUnknown || unknown.Sealed == nil ||
		unknown.Sealed.Protocol != xir.ProtocolResponses || unknown.Sealed.Kind != "web_search_call" {
		t.Fatalf("unknown part = %+v", unknown)
	}
	if !json.Valid(unknown.Sealed.Raw) {
		t.Fatalf("unknown sealed raw = %s", unknown.Sealed.Raw)
	}

	if resp.Usage == nil {
		t.Fatal("usage is nil")
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 || resp.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if resp.Usage.CacheReadTokens != 2 || resp.Usage.ReasoningTokens != 3 {
		t.Fatalf("usage details = %+v", resp.Usage)
	}
	if resp.Usage.Source != "responses" {
		t.Fatalf("usage source = %q", resp.Usage.Source)
	}

	if resp.StopReason != xir.FinishLength || resp.StopDetail != "max_output_tokens" {
		t.Fatalf("stop = %s/%q", resp.StopReason, resp.StopDetail)
	}
	if resp.Error != nil {
		t.Fatalf("error = %+v", resp.Error)
	}

	extras := responseExtras(t, resp)
	for _, key := range []string{"created_at", "previous_response_id", "metadata"} {
		if _, ok := extras[key]; !ok {
			t.Fatalf("easy extra %q missing: %v", key, extras)
		}
	}
	if _, ok := extras["output"]; ok {
		t.Fatalf("output leaked into extras: %v", extras)
	}
}

func responseExtras(t *testing.T, resp *xir.Response) map[string]json.RawMessage {
	t.Helper()
	raw, ok := resp.Extensions[ExtensionKey]
	if !ok {
		return nil
	}
	return rawObject(t, raw)
}

func TestResponseRoundTrips(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		check func(t *testing.T, resp *xir.Response)
	}{
		{name: "full response", raw: fullResponseJSON, check: checkFullResponse},
		{
			name: "completed function call",
			raw:  `{"id":"r","model":"m","status":"completed","output":[{"type":"function_call","call_id":"c","name":"f","arguments":"{}"}]}`,
			check: func(t *testing.T, resp *xir.Response) {
				if resp.StopReason != xir.FinishToolUse {
					t.Fatalf("stop = %s", resp.StopReason)
				}
				if len(resp.Parts) != 1 || resp.Parts[0].Kind != xir.PartToolCall {
					t.Fatalf("parts = %+v", resp.Parts)
				}
			},
		},
		{
			name: "content filter",
			raw:  `{"id":"r","model":"m","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}`,
			check: func(t *testing.T, resp *xir.Response) {
				if resp.StopReason != xir.FinishContentFilter || resp.StopDetail != "content_filter" {
					t.Fatalf("stop = %s/%q", resp.StopReason, resp.StopDetail)
				}
			},
		},
		{
			name: "failed with error",
			raw:  `{"id":"r","model":"m","status":"failed","error":{"message":"boom","type":"server_error"},"output":[]}`,
			check: func(t *testing.T, resp *xir.Response) {
				if resp.Error == nil || resp.Error.Message != "boom" || resp.Error.Type != "server_error" {
					t.Fatalf("error = %+v", resp.Error)
				}
				if resp.StopReason != xir.FinishError || resp.StopDetail != "boom" {
					t.Fatalf("stop = %s/%q", resp.StopReason, resp.StopDetail)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := decodeResponseRaw(t, tc.raw)
			tc.check(t, original)

			encoded := encodeResponseObject(t, original, xir.EncodeOptions{})
			roundTripped := decodeResponseRaw(t, string(mustMarshal(t, encoded)))
			tc.check(t, roundTripped)
		})
	}
}

func TestDecodeResponseStatus(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantReason xir.FinishReason
		wantDetail string
	}{
		{name: "completed", raw: `{"status":"completed","output":[]}`, wantReason: xir.FinishStop},
		{name: "completed with tool call", raw: `{"status":"completed","output":[{"type":"function_call","call_id":"c","name":"f","arguments":"{}"}]}`, wantReason: xir.FinishToolUse},
		{name: "max output tokens", raw: `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, wantReason: xir.FinishLength, wantDetail: "max_output_tokens"},
		{name: "content filter", raw: `{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}`, wantReason: xir.FinishContentFilter, wantDetail: "content_filter"},
		{name: "other incomplete reason", raw: `{"status":"incomplete","incomplete_details":{"reason":"tool_calls"}}`, wantReason: xir.FinishUnknown, wantDetail: "tool_calls"},
		{name: "failed", raw: `{"status":"failed"}`, wantReason: xir.FinishError},
		{name: "cancelled", raw: `{"status":"cancelled"}`, wantReason: xir.FinishUnknown},
		{name: "queued", raw: `{"status":"queued"}`, wantReason: xir.FinishUnknown},
		{name: "missing status", raw: `{"id":"r"}`, wantReason: xir.FinishUnknown},
		{name: "error envelope without status", raw: `{"error":{"message":"bad"}}`, wantReason: xir.FinishError, wantDetail: "bad"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := decodeResponseRaw(t, tc.raw)
			if resp.StopReason != tc.wantReason {
				t.Fatalf("stop reason = %q, want %q", resp.StopReason, tc.wantReason)
			}
			if resp.StopDetail != tc.wantDetail {
				t.Fatalf("stop detail = %q, want %q", resp.StopDetail, tc.wantDetail)
			}
		})
	}
}

func TestDecodeResponseErrorEnvelope(t *testing.T) {
	resp := decodeResponseRaw(t, `{"error":{"message":"invalid input","type":"invalid_request_error","param":"input","code":"bad_value"}}`)
	if resp.Error == nil {
		t.Fatal("error is nil")
	}
	if resp.Error.Message != "invalid input" || resp.Error.Type != "invalid_request_error" ||
		resp.Error.Param != "input" || resp.Error.Code != "bad_value" {
		t.Fatalf("error = %+v", resp.Error)
	}
	if !json.Valid(resp.Error.Raw) {
		t.Fatalf("raw error = %s", resp.Error.Raw)
	}
	if resp.StopReason != xir.FinishError {
		t.Fatalf("stop = %s", resp.StopReason)
	}
}

func TestEncodeResponseShape(t *testing.T) {
	resp := &xir.Response{
		ID:    "resp_x",
		Model: "gpt-5",
		Parts: []xir.Part{
			{Kind: xir.PartText, Text: "hi"},
			{Kind: xir.PartRefusal, Text: "no"},
			{Kind: xir.PartToolCall, ToolCall: &xir.ToolCall{ID: "call_1", Name: "f", ArgsText: `{"x":1}`}},
		},
		Usage: &usage.Usage{
			InputTokens:     3,
			OutputTokens:    2,
			CacheReadTokens: 1,
			ReasoningTokens: 1,
		},
	}

	obj := encodeResponseObject(t, resp, xir.EncodeOptions{})
	if rawText(t, requireKey(t, obj, "object")) != "response" {
		t.Fatalf("object = %v", obj["object"])
	}
	if rawText(t, requireKey(t, obj, "status")) != "completed" {
		t.Fatalf("status = %v", obj["status"])
	}

	output := rawArray(t, requireKey(t, obj, "output"))
	if len(output) != 2 {
		t.Fatalf("output = %s", output)
	}
	message := rawObject(t, output[0])
	if rawText(t, requireKey(t, message, "type")) != "message" || rawText(t, requireKey(t, message, "role")) != "assistant" {
		t.Fatalf("message = %v", message)
	}
	content := rawArray(t, requireKey(t, message, "content"))
	if len(content) != 2 {
		t.Fatalf("message content = %s", content)
	}
	if rawText(t, requireKey(t, rawObject(t, content[0]), "type")) != "output_text" {
		t.Fatalf("text content = %s", content[0])
	}
	if rawText(t, requireKey(t, rawObject(t, content[1]), "type")) != "refusal" {
		t.Fatalf("refusal content = %s", content[1])
	}
	call := rawObject(t, output[1])
	if rawText(t, requireKey(t, call, "type")) != "function_call" ||
		rawText(t, requireKey(t, call, "call_id")) != "call_1" {
		t.Fatalf("function call = %v", call)
	}

	encodedUsage := commonFields(t, obj, "usage")
	if rawLiteral(t, requireKey(t, encodedUsage, "input_tokens")) != "3" {
		t.Fatalf("usage = %v", encodedUsage)
	}
	details := commonFields(t, encodedUsage, "input_tokens_details")
	if rawLiteral(t, requireKey(t, details, "cached_tokens")) != "1" {
		t.Fatalf("usage input details = %v", details)
	}
	outputDetails := commonFields(t, encodedUsage, "output_tokens_details")
	if rawLiteral(t, requireKey(t, outputDetails, "reasoning_tokens")) != "1" {
		t.Fatalf("usage output details = %v", outputDetails)
	}

	roundTripped := decodeResponseRaw(t, string(mustMarshal(t, obj)))
	if roundTripped.StopReason != xir.FinishToolUse {
		t.Fatalf("round-tripped stop = %s", roundTripped.StopReason)
	}
	if len(roundTripped.Parts) != 3 {
		t.Fatalf("round-tripped parts = %+v", roundTripped.Parts)
	}
}

func TestSealedReasoningRoundTripsVerbatim(t *testing.T) {
	const item = `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"t"}],"encrypted_content":"gAAAA"}`
	resp := decodeResponseRaw(t, `{"id":"r","model":"m","status":"completed","output":[`+item+`]}`)
	obj := encodeResponseObject(t, resp, xir.EncodeOptions{})
	output := rawArray(t, requireKey(t, obj, "output"))
	if len(output) != 1 {
		t.Fatalf("output = %s", output)
	}
	if string(output[0]) != item {
		t.Fatalf("sealed reasoning item changed:\n got %s\nwant %s", output[0], item)
	}
}

func TestEncodeRejectsForeignSealedBlocks(t *testing.T) {
	foreign := &xir.Sealed{Protocol: xir.ProtocolMessages, Kind: "thinking", Raw: json.RawMessage(`{"type":"thinking"}`)}

	_, err := Codec{}.EncodeResponse(context.Background(), &xir.Response{
		Parts: []xir.Part{{Kind: xir.PartSealed, Sealed: foreign}},
	}, xir.EncodeOptions{})
	if err == nil {
		t.Fatal("EncodeResponse accepted a foreign sealed block")
	}

	_, err = Codec{}.EncodeRequest(context.Background(), &xir.Request{
		Messages: []xir.Message{{
			Role:  xir.RoleAssistant,
			Parts: []xir.Part{{Kind: xir.PartReasoning, Reasoning: &xir.Reasoning{Sealed: foreign}}},
		}},
	}, xir.EncodeOptions{})
	if err == nil {
		t.Fatal("EncodeRequest accepted a foreign sealed reasoning block")
	}
}

func TestDecodeRejectsMalformedEnvelopes(t *testing.T) {
	t.Run("malformed JSON", func(t *testing.T) {
		if _, err := xir.ParseDocument(xir.ProtocolResponses, xir.OperationResponses, []byte(`{"model":`)); err == nil {
			t.Fatal("ParseDocument accepted malformed JSON")
		}
	})

	t.Run("nil document", func(t *testing.T) {
		if _, err := (Codec{}).DecodeRequest(context.Background(), nil, xir.DecodeOptions{}); err == nil {
			t.Fatal("DecodeRequest accepted a nil document")
		}
		if _, err := (Codec{}).DecodeResponse(context.Background(), nil, xir.DecodeOptions{}); err == nil {
			t.Fatal("DecodeResponse accepted a nil document")
		}
	})

	t.Run("non-object root", func(t *testing.T) {
		doc := testDoc(t, `[]`)
		if _, err := (Codec{}).DecodeRequest(context.Background(), doc, xir.DecodeOptions{}); err == nil {
			t.Fatal("DecodeRequest accepted an array root")
		}
		if _, err := (Codec{}).DecodeResponse(context.Background(), doc, xir.DecodeOptions{}); err == nil {
			t.Fatal("DecodeResponse accepted an array root")
		}
	})

	t.Run("nil values", func(t *testing.T) {
		if _, err := (Codec{}).EncodeRequest(context.Background(), nil, xir.EncodeOptions{}); err == nil {
			t.Fatal("EncodeRequest accepted nil")
		}
		if _, err := (Codec{}).EncodeResponse(context.Background(), nil, xir.EncodeOptions{}); err == nil {
			t.Fatal("EncodeResponse accepted nil")
		}
	})
}

func TestDecodeResponseToleratesWrongOptionalTypes(t *testing.T) {
	resp := decodeResponseRaw(t, `{"id":7,"model":false,"status":3,"output":{},"usage":"none","incomplete_details":[]}`)
	if resp.ID != "" || resp.Model != "" || resp.Status != "" {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.Parts) != 0 || resp.Usage != nil {
		t.Fatalf("parts/usage = %+v/%+v", resp.Parts, resp.Usage)
	}
	if resp.StopReason != xir.FinishUnknown {
		t.Fatalf("stop = %s", resp.StopReason)
	}
}
