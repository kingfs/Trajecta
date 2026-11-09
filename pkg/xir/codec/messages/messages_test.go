package messages_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/codec/messages"
)

var (
	_ xir.Codec = messages.New()
	_ xir.Codec = messages.Codec{}
)

const requestJSON = `{
  "model": "claude-sonnet-4-5",
  "system": "You are concise.",
  "messages": [
    {"role": "user", "content": [
      {"type": "text", "text": "Describe this image."},
      {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "aW1hZ2U="}}
    ]},
    {"role": "assistant", "content": "It is a diagram."}
  ],
  "tools": [
    {
      "name": "lookup",
      "description": "Look something up",
      "input_schema": {"type": "object", "properties": {"q": {"type": "string"}}, "required": ["q"]},
      "strict": true
    }
  ],
  "tool_choice": {"type": "tool", "name": "lookup", "disable_parallel_tool_use": true},
  "max_tokens": 2048,
  "temperature": 0.2,
  "top_p": 0.9,
  "top_k": 40,
  "stop_sequences": ["END"],
  "metadata": {"user_id": "user-42"},
  "stream": true
}`

func TestCodecProtocol(t *testing.T) {
	codec := messages.New()
	if got := codec.Protocol(); got != xir.ProtocolMessages {
		t.Fatalf("Protocol() = %q, want %q", got, xir.ProtocolMessages)
	}
}

func TestRequestRoundTrip(t *testing.T) {
	req := decodeMessagesRequest(t, requestJSON)

	if req.Model != "claude-sonnet-4-5" {
		t.Fatalf("Model = %q", req.Model)
	}
	if !req.Stream {
		t.Fatal("Stream = false, want true")
	}
	if req.Ref == nil {
		t.Fatal("request Ref is nil")
	}
	if len(req.System) != 1 || req.System[0].Kind != xir.PartText || req.System[0].Text != "You are concise." {
		t.Fatalf("System = %+v", req.System)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("len(Messages) = %d", len(req.Messages))
	}
	if req.Messages[0].Role != xir.RoleUser || req.Messages[1].Role != xir.RoleAssistant {
		t.Fatalf("roles = %q, %q", req.Messages[0].Role, req.Messages[1].Role)
	}
	if req.Messages[0].Ref == nil || len(req.Messages[0].Parts) != 2 {
		t.Fatalf("first message = %+v", req.Messages[0])
	}
	image := req.Messages[0].Parts[1]
	if image.Kind != xir.PartImage || image.Media == nil {
		t.Fatalf("image part = %+v", image)
	}
	if image.Media.Kind != xir.MediaImage || image.Media.MediaType != "image/png" || image.Media.Data != "aW1hZ2U=" || image.Media.URL != "" {
		t.Fatalf("image media = %+v", image.Media)
	}
	if image.Ref == nil {
		t.Fatal("image Ref is nil")
	}
	if len(req.Tools) != 1 {
		t.Fatalf("len(Tools) = %d", len(req.Tools))
	}
	tool := req.Tools[0]
	if tool.Name != "lookup" || tool.Description != "Look something up" || tool.Kind != "function" {
		t.Fatalf("tool = %+v", tool)
	}
	if tool.Strict == nil || !*tool.Strict {
		t.Fatalf("tool.Strict = %v", tool.Strict)
	}
	if !json.Valid(tool.Schema) || !bytes.Contains(tool.Schema, []byte(`"q"`)) {
		t.Fatalf("tool.Schema = %s", tool.Schema)
	}
	if req.ToolChoice.Mode != "tool" || req.ToolChoice.Name != "lookup" {
		t.Fatalf("ToolChoice = %+v", req.ToolChoice)
	}
	if req.Params.ParallelTools == nil || *req.Params.ParallelTools {
		t.Fatalf("ParallelTools = %v, want false", req.Params.ParallelTools)
	}
	if req.Params.MaxTokens == nil || *req.Params.MaxTokens != 2048 {
		t.Fatalf("MaxTokens = %v", req.Params.MaxTokens)
	}
	if req.Params.Temperature == nil || *req.Params.Temperature != 0.2 {
		t.Fatalf("Temperature = %v", req.Params.Temperature)
	}
	if req.Params.TopP == nil || *req.Params.TopP != 0.9 {
		t.Fatalf("TopP = %v", req.Params.TopP)
	}
	if req.Params.TopK == nil || *req.Params.TopK != 40 {
		t.Fatalf("TopK = %v", req.Params.TopK)
	}
	if !reflect.DeepEqual(req.Params.Stop, []string{"END"}) {
		t.Fatalf("Stop = %v", req.Params.Stop)
	}
	if req.Params.User != "user-42" || !bytes.Contains(req.Params.Metadata, []byte("user-42")) {
		t.Fatalf("metadata = %s, user = %q", req.Params.Metadata, req.Params.User)
	}
	if req.Extensions != nil {
		t.Fatalf("Extensions = %v, want nil", req.Extensions)
	}

	encoded := encodeMessagesRequest(t, req, xir.EncodeOptions{})
	obj := messagesJSONMap(t, encoded)
	if obj["model"] != "claude-sonnet-4-5" {
		t.Fatalf("wire model = %v", obj["model"])
	}
	if obj["stream"] != true {
		t.Fatalf("wire stream = %v", obj["stream"])
	}
	if system, ok := obj["system"].(string); !ok || system != "You are concise." {
		t.Fatalf("wire system = %#v", obj["system"])
	}
	choice := messagesJSONObject(t, obj["tool_choice"])
	if choice["type"] != "tool" || choice["name"] != "lookup" || choice["disable_parallel_tool_use"] != true {
		t.Fatalf("wire tool_choice = %#v", choice)
	}
	if obj["max_tokens"] != float64(2048) || obj["temperature"] != 0.2 || obj["top_p"] != 0.9 || obj["top_k"] != float64(40) {
		t.Fatalf("wire params = %#v", obj)
	}

	again := decodeMessagesRequest(t, string(encoded))
	if again.Model != req.Model || again.Stream != req.Stream {
		t.Fatalf("round trip model/stream = %q/%v", again.Model, again.Stream)
	}
	if !reflect.DeepEqual(again.Params.Stop, req.Params.Stop) ||
		again.Params.User != req.Params.User ||
		*again.Params.MaxTokens != *req.Params.MaxTokens ||
		again.ToolChoice != req.ToolChoice ||
		*again.Params.ParallelTools != *req.Params.ParallelTools {
		t.Fatalf("round trip request = %+v", again)
	}
	same := encodeMessagesRequest(t, again, xir.EncodeOptions{})
	if !bytes.Equal(encoded, same) {
		t.Fatalf("encode is not stable:\nfirst:  %s\nsecond: %s", encoded, same)
	}
}

func TestSystemBlocks(t *testing.T) {
	raw := `{
	  "model": "m",
	  "max_tokens": 8,
	  "system": [
	    {"type": "text", "text": "first", "cache_control": {"type": "ephemeral"}},
	    {"type": "text", "text": "second"}
	  ],
	  "messages": []
	}`
	req := decodeMessagesRequest(t, raw)
	if len(req.System) != 2 || req.System[0].Text != "first" || req.System[1].Text != "second" {
		t.Fatalf("System = %+v", req.System)
	}
	obj := messagesJSONMap(t, encodeMessagesRequest(t, req, xir.EncodeOptions{}))
	blocks, ok := obj["system"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("wire system = %#v, want block array", obj["system"])
	}
	if first := messagesJSONObject(t, blocks[0]); first["type"] != "text" || first["text"] != "first" {
		t.Fatalf("first system block = %#v", first)
	}

	// One text block is the string shorthand again.
	single := decodeMessagesRequest(t, `{"model":"m","max_tokens":8,"system":[{"type":"text","text":"only"}],"messages":[]}`)
	if len(single.System) != 1 {
		t.Fatalf("single System = %+v", single.System)
	}
	obj = messagesJSONMap(t, encodeMessagesRequest(t, single, xir.EncodeOptions{}))
	if got, ok := obj["system"].(string); !ok || got != "only" {
		t.Fatalf("single wire system = %#v", obj["system"])
	}

	// Structured IR input without a wire source also renders blocks.
	structured := &xir.Request{
		Model:  "m",
		Params: xir.Params{MaxTokens: messagesIntPtr(8)},
		System: []xir.Part{
			{Kind: xir.PartText, Text: "a"},
			{Kind: xir.PartText, Text: "b"},
		},
	}
	obj = messagesJSONMap(t, encodeMessagesRequest(t, structured, xir.EncodeOptions{}))
	if blocks, ok := obj["system"].([]any); !ok || len(blocks) != 2 {
		t.Fatalf("structured wire system = %#v", obj["system"])
	}
}

func TestToolLoop(t *testing.T) {
	raw := `{
	  "model": "claude-sonnet-4-5",
	  "max_tokens": 1024,
	  "tools": [{"name": "weather", "input_schema": {"type": "object"}}],
	  "messages": [
	    {"role": "user", "content": "Weather in Paris?"},
	    {"role": "assistant", "content": [
	      {"type": "text", "text": "Checking."},
	      {"type": "tool_use", "id": "toolu_1", "name": "weather", "input": {"city": "Paris"}}
	    ]},
	    {"role": "user", "content": [
	      {"type": "text", "text": "Here is the result:"},
	      {"type": "tool_result", "tool_use_id": "toolu_1", "content": "18C and sunny"},
	      {"type": "tool_result", "tool_use_id": "toolu_2", "is_error": true, "content": [{"type": "text", "text": "boom"}]}
	    ]}
	  ]
	}`
	req := decodeMessagesRequest(t, raw)
	assistant := req.Messages[1]
	if len(assistant.Parts) != 2 || assistant.Parts[1].Kind != xir.PartToolCall {
		t.Fatalf("assistant parts = %+v", assistant.Parts)
	}
	call := assistant.Parts[1].ToolCall
	if call == nil || call.ID != "toolu_1" || call.Name != "weather" || call.Type != "function" {
		t.Fatalf("tool call = %+v", call)
	}
	if !bytes.Equal(call.Args, json.RawMessage(`{"city": "Paris"}`)) && !bytes.Equal(call.Args, json.RawMessage(`{"city":"Paris"}`)) {
		t.Fatalf("tool args = %s", call.Args)
	}
	user := req.Messages[2]
	if len(user.Parts) != 3 {
		t.Fatalf("user parts = %+v", user.Parts)
	}
	if user.Parts[0].Kind != xir.PartText || user.Parts[1].Kind != xir.PartToolResult || user.Parts[2].Kind != xir.PartToolResult {
		t.Fatalf("wire order changed on decode: %+v", user.Parts)
	}
	if result := user.Parts[1].ToolResult; result == nil || result.CallID != "toolu_1" || result.Text != "18C and sunny" || result.IsError {
		t.Fatalf("tool result = %+v", result)
	}
	if result := user.Parts[2].ToolResult; result == nil || !result.IsError || len(result.Content) != 1 || result.Content[0].Text != "boom" {
		t.Fatalf("error tool result = %+v", result)
	}

	encoded := encodeMessagesRequest(t, req, xir.EncodeOptions{})
	obj := messagesJSONMap(t, encoded)
	wireMessages := messagesJSONArray(t, obj["messages"])
	if len(wireMessages) != 3 {
		t.Fatalf("wire messages = %d", len(wireMessages))
	}
	wireUser := messagesJSONObject(t, wireMessages[2])
	wireBlocks := messagesJSONArray(t, wireUser["content"])
	if len(wireBlocks) != 3 {
		t.Fatalf("wire user blocks = %#v", wireUser["content"])
	}
	for i := 0; i < 2; i++ {
		if block := messagesJSONObject(t, wireBlocks[i]); block["type"] != "tool_result" {
			t.Fatalf("block %d = %#v, want tool_result first", i, block)
		}
	}
	if text := messagesJSONObject(t, wireBlocks[2]); text["type"] != "text" || text["text"] != "Here is the result:" {
		t.Fatalf("last block = %#v", text)
	}
	if content := messagesJSONObject(t, wireBlocks[0])["content"]; content != "18C and sunny" {
		t.Fatalf("string tool_result content = %#v", content)
	}
	second := messagesJSONObject(t, wireBlocks[1])
	if second["is_error"] != true {
		t.Fatalf("is_error = %#v", second["is_error"])
	}
	if content, ok := second["content"].([]any); !ok || len(content) != 1 {
		t.Fatalf("array tool_result content = %#v", second["content"])
	}

	again := decodeMessagesRequest(t, string(encoded))
	if got := again.Messages[2].Parts; len(got) != 3 || got[0].Kind != xir.PartToolResult || got[2].Kind != xir.PartText {
		t.Fatalf("re-decoded user parts = %+v", got)
	}
	if !bytes.Equal(encoded, encodeMessagesRequest(t, again, xir.EncodeOptions{})) {
		t.Fatal("tool loop encode is not stable")
	}
}

func TestResponseToolUseRoundTrip(t *testing.T) {
	raw := `{
	  "id": "msg_tool",
	  "type": "message",
	  "role": "assistant",
	  "model": "claude-sonnet-4-5",
	  "content": [
	    {"type": "text", "text": "Calling the tool."},
	    {"type": "tool_use", "id": "toolu_x", "name": "calc", "input": {"n": 2}}
	  ],
	  "stop_reason": "tool_use",
	  "usage": {"input_tokens": 5, "output_tokens": 7}
	}`
	resp := decodeMessagesResponse(t, raw)
	if resp.Ref == nil {
		t.Fatal("response Ref is nil")
	}
	if len(resp.Parts) != 2 {
		t.Fatalf("parts = %+v", resp.Parts)
	}
	if resp.Parts[0].Ref == nil || resp.Parts[1].Ref == nil {
		t.Fatal("response part Ref is nil")
	}
	if resp.Parts[1].Kind != xir.PartToolCall || resp.Parts[1].ToolCall == nil {
		t.Fatalf("tool call part = %+v", resp.Parts[1])
	}
	if call := resp.Parts[1].ToolCall; call.ID != "toolu_x" || call.Name != "calc" || !json.Valid(call.Args) {
		t.Fatalf("tool call = %+v", call)
	}
	if resp.StopReason != xir.FinishToolUse {
		t.Fatalf("StopReason = %q", resp.StopReason)
	}
	encoded := encodeMessagesResponse(t, resp, xir.EncodeOptions{})
	obj := messagesJSONMap(t, encoded)
	if obj["stop_reason"] != "tool_use" || obj["type"] != "message" || obj["role"] != "assistant" {
		t.Fatalf("wire response = %#v", obj)
	}
	blocks := messagesJSONArray(t, obj["content"])
	if block := messagesJSONObject(t, blocks[1]); block["type"] != "tool_use" || block["name"] != "calc" {
		t.Fatalf("wire tool_use = %#v", block)
	}
	if !bytes.Equal(encoded, encodeMessagesResponse(t, decodeMessagesResponse(t, string(encoded)), xir.EncodeOptions{})) {
		t.Fatal("response tool_use encode is not stable")
	}
}

func TestThinkingSealed(t *testing.T) {
	raw := `{
	  "id": "msg_1",
	  "type": "message",
	  "role": "assistant",
	  "model": "claude-opus-4",
	  "content": [
	    {"type": "thinking", "thinking": "Let me think", "signature": "sig-abc"},
	    {"type": "redacted_thinking", "data": "encrypted-blob"},
	    {"type": "text", "text": "Done"}
	  ],
	  "stop_reason": "end_turn",
	  "usage": {"input_tokens": 10, "output_tokens": 5}
	}`
	resp := decodeMessagesResponse(t, raw)
	if len(resp.Parts) != 3 {
		t.Fatalf("parts = %+v", resp.Parts)
	}
	thinking := resp.Parts[0]
	if thinking.Kind != xir.PartReasoning || thinking.Reasoning == nil {
		t.Fatalf("thinking part = %+v", thinking)
	}
	if thinking.Reasoning.Text != "Let me think" || thinking.Reasoning.Signature != "sig-abc" || thinking.Reasoning.Sealed != nil {
		t.Fatalf("thinking reasoning = %+v", thinking.Reasoning)
	}
	redacted := resp.Parts[1]
	if redacted.Kind != xir.PartReasoning || redacted.Reasoning == nil || redacted.Reasoning.Sealed == nil {
		t.Fatalf("redacted part = %+v", redacted)
	}
	sealed := redacted.Reasoning.Sealed
	if sealed.Protocol != xir.ProtocolMessages || sealed.Kind != "redacted_thinking" || !json.Valid(sealed.Raw) {
		t.Fatalf("sealed = %+v", sealed)
	}
	if redacted.Sealed != sealed {
		t.Fatalf("Part.Sealed = %p, Reasoning.Sealed = %p", redacted.Sealed, sealed)
	}
	if redacted.Reasoning.EncryptedContent != "encrypted-blob" {
		t.Fatalf("encrypted content = %q", redacted.Reasoning.EncryptedContent)
	}

	encoded := encodeMessagesResponse(t, resp, xir.EncodeOptions{})
	obj := messagesJSONMap(t, encoded)
	blocks := messagesJSONArray(t, obj["content"])
	if len(blocks) != 3 {
		t.Fatalf("wire content = %#v", obj["content"])
	}
	if block := messagesJSONObject(t, blocks[0]); block["type"] != "thinking" || block["signature"] != "sig-abc" {
		t.Fatalf("wire thinking = %#v", block)
	}
	if block := messagesJSONObject(t, blocks[1]); block["type"] != "redacted_thinking" || block["data"] != "encrypted-blob" {
		t.Fatalf("wire redacted = %#v", block)
	}
	if !bytes.Equal(encoded, encodeMessagesResponse(t, decodeMessagesResponse(t, string(encoded)), xir.EncodeOptions{})) {
		t.Fatal("response encode is not stable")
	}

	// The same blocks also appear in an assistant history turn of a request.
	requestRaw := `{
	  "model": "claude-opus-4",
	  "max_tokens": 64,
	  "messages": [
	    {"role": "user", "content": "continue"},
	    {"role": "assistant", "content": [
	      {"type": "thinking", "thinking": "history thought", "signature": "sig-history"},
	      {"type": "redacted_thinking", "data": "encrypted-history"},
	      {"type": "text", "text": "answer"}
	    ]}
	  ]
	}`
	req := decodeMessagesRequest(t, requestRaw)
	requestEncoded := encodeMessagesRequest(t, req, xir.EncodeOptions{})
	requestObj := messagesJSONMap(t, requestEncoded)
	wireMessages := messagesJSONArray(t, requestObj["messages"])
	wireBlocks := messagesJSONArray(t, messagesJSONObject(t, wireMessages[1])["content"])
	if len(wireBlocks) != 3 {
		t.Fatalf("wire request blocks = %#v", wireBlocks)
	}
	if block := messagesJSONObject(t, wireBlocks[0]); block["signature"] != "sig-history" {
		t.Fatalf("wire request thinking = %#v", block)
	}
	if block := messagesJSONObject(t, wireBlocks[1]); block["type"] != "redacted_thinking" || block["data"] != "encrypted-history" {
		t.Fatalf("wire request redacted = %#v", block)
	}
}

func TestUsageCacheFields(t *testing.T) {
	raw := `{
	  "id": "msg_usage",
	  "type": "message",
	  "role": "assistant",
	  "model": "claude-sonnet-4-5",
	  "content": [{"type": "text", "text": "ok"}],
	  "stop_reason": "max_tokens",
	  "usage": {
	    "input_tokens": 100,
	    "output_tokens": 20,
	    "cache_creation_input_tokens": 30,
	    "cache_read_input_tokens": 40,
	    "cache_creation": {"ephemeral_5m_input_tokens": 30, "ephemeral_1h_input_tokens": 0}
	  }
	}`
	resp := decodeMessagesResponse(t, raw)
	if resp.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if resp.Usage.InputTokens != 100 || resp.Usage.OutputTokens != 20 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if resp.Usage.CacheCreationTokens != 30 || resp.Usage.CacheReadTokens != 40 {
		t.Fatalf("cache usage = %+v", resp.Usage)
	}
	if resp.Usage.CacheCreation5mTokens != 30 || resp.Usage.CacheCreation1hTokens != 0 {
		t.Fatalf("cache creation split = %+v", resp.Usage)
	}
	if resp.Usage.TotalTokens != 120 || resp.Usage.Source != "messages" {
		t.Fatalf("derived usage = %+v", resp.Usage)
	}
	if resp.StopReason != xir.FinishLength {
		t.Fatalf("StopReason = %q", resp.StopReason)
	}

	obj := messagesJSONMap(t, encodeMessagesResponse(t, resp, xir.EncodeOptions{}))
	wireUsage := messagesJSONObject(t, obj["usage"])
	if wireUsage["input_tokens"] != float64(100) || wireUsage["output_tokens"] != float64(20) {
		t.Fatalf("wire usage = %#v", wireUsage)
	}
	if wireUsage["cache_creation_input_tokens"] != float64(30) || wireUsage["cache_read_input_tokens"] != float64(40) {
		t.Fatalf("wire cache usage = %#v", wireUsage)
	}
	if cacheCreation := messagesJSONObject(t, wireUsage["cache_creation"]); cacheCreation["ephemeral_5m_input_tokens"] != float64(30) {
		t.Fatalf("wire cache_creation = %#v", cacheCreation)
	}
	again := decodeMessagesResponse(t, string(encodeMessagesResponse(t, resp, xir.EncodeOptions{})))
	if !reflect.DeepEqual(resp.Usage, again.Usage) {
		t.Fatalf("usage round trip = %+v vs %+v", resp.Usage, again.Usage)
	}
}

func TestErrorEnvelope(t *testing.T) {
	raw := `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens is required","param":"max_tokens"}}`
	resp := decodeMessagesResponse(t, raw)
	if resp.Error == nil {
		t.Fatal("Error is nil")
	}
	if resp.Error.Type != "invalid_request_error" || resp.Error.Message != "max_tokens is required" || resp.Error.Param != "max_tokens" {
		t.Fatalf("Error = %+v", resp.Error)
	}
	if len(resp.Error.Raw) == 0 || !json.Valid(resp.Error.Raw) {
		t.Fatalf("Error.Raw = %s", resp.Error.Raw)
	}
	if len(resp.Parts) != 0 {
		t.Fatalf("error response parts = %+v", resp.Parts)
	}

	encoded := encodeMessagesResponse(t, resp, xir.EncodeOptions{})
	obj := messagesJSONMap(t, encoded)
	if obj["type"] != "error" {
		t.Fatalf("wire type = %#v", obj["type"])
	}
	body := messagesJSONObject(t, obj["error"])
	if body["type"] != "invalid_request_error" || body["message"] != "max_tokens is required" || body["param"] != "max_tokens" {
		t.Fatalf("wire error = %#v", body)
	}
	again := decodeMessagesResponse(t, string(encoded))
	if again.Error == nil || again.Error.Type != resp.Error.Type || again.Error.Message != resp.Error.Message {
		t.Fatalf("error round trip = %+v", again.Error)
	}

	constructed := &xir.Response{Error: &xir.Error{Type: "api_error", Message: "upstream failed"}}
	encoded = encodeMessagesResponse(t, constructed, xir.EncodeOptions{})
	if body := messagesJSONObject(t, messagesJSONMap(t, encoded)["error"]); body["type"] != "api_error" || body["message"] != "upstream failed" {
		t.Fatalf("constructed wire error = %#v", body)
	}
}

func TestStopDetailsAndRefusal(t *testing.T) {
	raw := `{
	  "id": "msg_refusal",
	  "type": "message",
	  "role": "assistant",
	  "model": "claude-sonnet-4-5",
	  "content": [],
	  "stop_reason": "refusal",
	  "stop_details": {"type": "refusal", "category": "cyber", "explanation": "declined by policy"},
	  "usage": {"input_tokens": 1, "output_tokens": 0}
	}`
	resp := decodeMessagesResponse(t, raw)
	if resp.StopReason != xir.FinishRefusal {
		t.Fatalf("StopReason = %q", resp.StopReason)
	}
	if resp.StopDetail != "declined by policy" {
		t.Fatalf("StopDetail = %q", resp.StopDetail)
	}
	if resp.Extensions == nil || !bytes.Contains(resp.Extensions[messages.ExtensionKey], []byte("stop_details")) {
		t.Fatalf("Extensions = %s", resp.Extensions)
	}
	encoded := encodeMessagesResponse(t, resp, xir.EncodeOptions{})
	obj := messagesJSONMap(t, encoded)
	if obj["stop_reason"] != "refusal" {
		t.Fatalf("wire stop_reason = %#v", obj["stop_reason"])
	}
	details := messagesJSONObject(t, obj["stop_details"])
	if details["category"] != "cyber" || details["explanation"] != "declined by policy" {
		t.Fatalf("wire stop_details = %#v", details)
	}

	// A matched stop sequence is persisted through StopDetail.
	stopSequence := `{
	  "id": "msg_stop",
	  "type": "message",
	  "role": "assistant",
	  "model": "m",
	  "content": [{"type": "text", "text": "partial"}],
	  "stop_reason": "stop_sequence",
	  "stop_sequence": "END"
	}`
	stopResp := decodeMessagesResponse(t, stopSequence)
	if stopResp.StopReason != xir.FinishStop || stopResp.StopDetail != "END" {
		t.Fatalf("stop response = %+v", stopResp)
	}
	obj = messagesJSONMap(t, encodeMessagesResponse(t, stopResp, xir.EncodeOptions{}))
	if obj["stop_reason"] != "stop_sequence" || obj["stop_sequence"] != "END" {
		t.Fatalf("wire stop sequence = %#v", obj)
	}
}

func TestUnknownFieldPreservation(t *testing.T) {
	req := decodeMessagesRequest(t, `{"model":"m","max_tokens":8,"messages":[],"service_tier":"auto","context_management":{"edits":[]}}`)
	if req.Extensions == nil || len(req.Extensions[messages.ExtensionKey]) == 0 {
		t.Fatalf("Extensions = %v", req.Extensions)
	}
	obj := messagesJSONMap(t, encodeMessagesRequest(t, req, xir.EncodeOptions{}))
	if obj["service_tier"] != "auto" {
		t.Fatalf("wire service_tier = %#v", obj["service_tier"])
	}
	if _, ok := obj["context_management"].(map[string]any); !ok {
		t.Fatalf("wire context_management = %#v", obj["context_management"])
	}

	// An unmapped stop_reason survives via the same extension object.
	resp := decodeMessagesResponse(t, `{"id":"msg_p","type":"message","role":"assistant","model":"m","content":[],"stop_reason":"pause_turn"}`)
	if resp.StopReason != xir.FinishUnknown {
		t.Fatalf("StopReason = %q", resp.StopReason)
	}
	if obj := messagesJSONMap(t, encodeMessagesResponse(t, resp, xir.EncodeOptions{})); obj["stop_reason"] != "pause_turn" {
		t.Fatalf("wire pause_turn = %#v", obj["stop_reason"])
	}
}

func TestMediaBlocks(t *testing.T) {
	raw := `{
	  "model": "m",
	  "max_tokens": 8,
	  "messages": [{"role": "user", "content": [
	    {"type": "document", "source": {"type": "url", "url": "https://example.com/a.pdf"}, "title": "a.pdf"},
	    {"type": "document", "source": {"type": "text", "media_type": "text/plain", "data": "plain body"}},
	    {"type": "image", "source": {"type": "url", "url": "https://example.com/i.png"}}
	  ]}]
	}`
	req := decodeMessagesRequest(t, raw)
	parts := req.Messages[0].Parts
	if len(parts) != 3 {
		t.Fatalf("parts = %+v", parts)
	}
	if parts[0].Kind != xir.PartFile || parts[0].Media == nil || parts[0].Media.URL != "https://example.com/a.pdf" || parts[0].Media.Filename != "a.pdf" {
		t.Fatalf("url document = %+v", parts[0])
	}
	if parts[1].Media == nil || parts[1].Media.Data != "plain body" || parts[1].Media.MediaType != "text/plain" {
		t.Fatalf("text document = %+v", parts[1])
	}
	if parts[2].Kind != xir.PartImage || parts[2].Media == nil || parts[2].Media.URL != "https://example.com/i.png" {
		t.Fatalf("url image = %+v", parts[2])
	}

	obj := messagesJSONMap(t, encodeMessagesRequest(t, req, xir.EncodeOptions{}))
	wireBlocks := messagesJSONArray(t, messagesJSONArray(t, obj["messages"])[0].(map[string]any)["content"])
	if source := messagesJSONObject(t, messagesJSONObject(t, wireBlocks[0])["source"]); source["type"] != "url" || source["url"] != "https://example.com/a.pdf" {
		t.Fatalf("wire url document = %#v", source)
	}
	if source := messagesJSONObject(t, messagesJSONObject(t, wireBlocks[1])["source"]); source["type"] != "text" || source["data"] != "plain body" {
		t.Fatalf("wire text document = %#v", source)
	}
	if source := messagesJSONObject(t, messagesJSONObject(t, wireBlocks[2])["source"]); source["type"] != "url" {
		t.Fatalf("wire url image = %#v", source)
	}
}

func TestEncodeOptions(t *testing.T) {
	req := decodeMessagesRequest(t, `{"model":"original","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	encoded := encodeMessagesRequest(t, req, xir.EncodeOptions{Model: "override", Stream: true})
	obj := messagesJSONMap(t, encoded)
	if obj["model"] != "override" || obj["stream"] != true {
		t.Fatalf("wire options = %#v", obj)
	}

	resp := decodeMessagesResponse(t, `{"id":"msg_o","type":"message","role":"assistant","model":"original","content":[]}`)
	obj = messagesJSONMap(t, encodeMessagesResponse(t, resp, xir.EncodeOptions{Model: "override"}))
	if obj["model"] != "override" {
		t.Fatalf("response model = %#v", obj["model"])
	}
}

func TestMalformedInput(t *testing.T) {
	requestCases := []string{
		`[]`,
		`{"messages":{}}`,
		`{"messages":[{"role":"user","content":42}]}`,
		`{"messages":[{"role":"user","content":[42]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":42}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"tool_use","id":5}]}]}`,
		`{"system":42}`,
		`{"tools":{}}`,
		`{"metadata":[]}`,
		`{"thinking":"enabled"}`,
		`{"stop_sequences":["ok",7]}`,
	}
	for _, raw := range requestCases {
		if _, err := messages.New().DecodeRequest(context.Background(), mustMessagesDocument(t, raw), xir.DecodeOptions{}); err == nil {
			t.Fatalf("DecodeRequest(%s) succeeded, want error", raw)
		}
	}

	responseCases := []string{
		`[]`,
		`{"id":"msg_x"}`,
		`{"content":42}`,
		`{"content":[{"type":"text","text":1}]}`,
		`{"type":"error"}`,
		`{"content":[],"usage":[]}`,
		`{"content":[],"usage":{"input_tokens":"many"}}`,
	}
	for _, raw := range responseCases {
		if _, err := messages.New().DecodeResponse(context.Background(), mustMessagesDocument(t, raw), xir.DecodeOptions{}); err == nil {
			t.Fatalf("DecodeResponse(%s) succeeded, want error", raw)
		}
	}

	if _, err := messages.New().DecodeRequest(context.Background(), nil, xir.DecodeOptions{}); err == nil {
		t.Fatal("DecodeRequest(nil) succeeded")
	}
	if _, err := messages.New().DecodeResponse(context.Background(), nil, xir.DecodeOptions{}); err == nil {
		t.Fatal("DecodeResponse(nil) succeeded")
	}
	if _, err := xir.ParseDocument(xir.ProtocolMessages, xir.OperationMessages, []byte(`{"model":`)); err == nil {
		t.Fatal("ParseDocument accepted invalid JSON")
	}

	badExtension := &xir.Request{
		Model:      "m",
		Extensions: map[string]json.RawMessage{messages.ExtensionKey: json.RawMessage(`{`)},
	}
	if _, err := messages.New().EncodeRequest(context.Background(), badExtension, xir.EncodeOptions{}); err == nil {
		t.Fatal("EncodeRequest accepted a non-object extension")
	}
	if _, err := messages.New().EncodeRequest(context.Background(), nil, xir.EncodeOptions{}); err == nil {
		t.Fatal("EncodeRequest(nil) succeeded")
	}
	if _, err := messages.New().EncodeResponse(context.Background(), nil, xir.EncodeOptions{}); err == nil {
		t.Fatal("EncodeResponse(nil) succeeded")
	}
}

func decodeMessagesRequest(t *testing.T, raw string) *xir.Request {
	t.Helper()
	req, err := messages.New().DecodeRequest(context.Background(), mustMessagesDocument(t, raw), xir.DecodeOptions{NeedParts: true, NeedTools: true, NeedUsage: true})
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	return req
}

func encodeMessagesRequest(t *testing.T, req *xir.Request, opts xir.EncodeOptions) []byte {
	t.Helper()
	raw, err := messages.New().EncodeRequest(context.Background(), req, opts)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatalf("EncodeRequest produced invalid JSON: %s", raw)
	}
	return raw
}

func decodeMessagesResponse(t *testing.T, raw string) *xir.Response {
	t.Helper()
	resp, err := messages.New().DecodeResponse(context.Background(), mustMessagesDocument(t, raw), xir.DecodeOptions{NeedParts: true, NeedUsage: true})
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	return resp
}

func encodeMessagesResponse(t *testing.T, resp *xir.Response, opts xir.EncodeOptions) []byte {
	t.Helper()
	raw, err := messages.New().EncodeResponse(context.Background(), resp, opts)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatalf("EncodeResponse produced invalid JSON: %s", raw)
	}
	return raw
}

func mustMessagesDocument(t *testing.T, raw string) *xir.Document {
	t.Helper()
	doc, err := xir.ParseDocument(xir.ProtocolMessages, xir.OperationMessages, []byte(raw))
	if err != nil {
		t.Fatalf("ParseDocument(%s): %v", raw, err)
	}
	return doc
}

func messagesJSONMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return out
}

func messagesJSONObject(t *testing.T, v any) map[string]any {
	t.Helper()
	out, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("value = %#v, want object", v)
	}
	return out
}

func messagesJSONArray(t *testing.T, v any) []any {
	t.Helper()
	out, ok := v.([]any)
	if !ok {
		t.Fatalf("value = %#v, want array", v)
	}
	return out
}

func messagesIntPtr(v int) *int { return &v }
