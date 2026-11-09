package audit_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/audit"
	"github.com/kingfs/Trajecta/pkg/xir/codec/chat"
	"github.com/kingfs/Trajecta/pkg/xir/codec/responses"
)

const chatRequestJSON = `{
  "model": "gpt-4o",
  "messages": [
    {"role": "system", "content": "You are a careful assistant."},
    {"role": "user", "content": [
      {"type": "text", "text": "What is the weather in Paris?"},
      {"type": "image_url", "image_url": {"url": "https://example.com/paris.png"}}
    ]},
    {"role": "assistant", "content": "Checking.", "tool_calls": [
      {"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"Paris\"}"}}
    ]},
    {"role": "tool", "tool_call_id": "call_1", "content": "sunny, 22C"}
  ],
  "tools": [
    {"type": "function", "function": {
      "name": "get_weather",
      "description": "Get the weather for a city",
      "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}
    }}
  ]
}`

const chatResponseJSON = `{
  "id": "chatcmpl_1",
  "model": "gpt-4o-2024",
  "choices": [{
    "finish_reason": "tool_calls",
    "message": {
      "role": "assistant",
      "content": "Let me check the weather.",
      "tool_calls": [
        {"id": "call_2", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"Paris\"}"}}
      ]
    }
  }],
  "usage": {
    "prompt_tokens": 12,
    "completion_tokens": 7,
    "total_tokens": 19,
    "completion_tokens_details": {"reasoning_tokens": 3}
  }
}`

func decodeChatRequest(t *testing.T, raw string) *xir.Request {
	t.Helper()
	doc, err := xir.ParseDocument(xir.ProtocolChatCompletions, xir.OperationChat, []byte(raw))
	if err != nil {
		t.Fatalf("parse chat request: %v", err)
	}
	req, err := chat.New().DecodeRequest(context.Background(), doc, xir.DecodeOptions{})
	if err != nil {
		t.Fatalf("decode chat request: %v", err)
	}
	return req
}

func decodeChatResponse(t *testing.T, raw string) *xir.Response {
	t.Helper()
	doc, err := xir.ParseDocument(xir.ProtocolChatCompletions, xir.OperationChat, []byte(raw))
	if err != nil {
		t.Fatalf("parse chat response: %v", err)
	}
	resp, err := chat.New().DecodeResponse(context.Background(), doc, xir.DecodeOptions{})
	if err != nil {
		t.Fatalf("decode chat response: %v", err)
	}
	return resp
}

func decodeResponsesRequest(t *testing.T, raw string) *xir.Request {
	t.Helper()
	doc, err := xir.ParseDocument(xir.ProtocolResponses, xir.OperationResponses, []byte(raw))
	if err != nil {
		t.Fatalf("parse responses request: %v", err)
	}
	req, err := responses.New().DecodeRequest(context.Background(), doc, xir.DecodeOptions{})
	if err != nil {
		t.Fatalf("decode responses request: %v", err)
	}
	return req
}

func decodeResponsesResponse(t *testing.T, raw string) *xir.Response {
	t.Helper()
	doc, err := xir.ParseDocument(xir.ProtocolResponses, xir.OperationResponses, []byte(raw))
	if err != nil {
		t.Fatalf("parse responses response: %v", err)
	}
	resp, err := responses.New().DecodeResponse(context.Background(), doc, xir.DecodeOptions{})
	if err != nil {
		t.Fatalf("decode responses response: %v", err)
	}
	return resp
}

func findNode(nodes []audit.Node, predicate func(audit.Node) bool) (audit.Node, bool) {
	for _, node := range nodes {
		if predicate(node) {
			return node, true
		}
	}
	return audit.Node{}, false
}

func mustFindNode(t *testing.T, nodes []audit.Node, predicate func(audit.Node) bool, what string) audit.Node {
	t.Helper()
	node, ok := findNode(nodes, predicate)
	if !ok {
		t.Fatalf("node not found: %s", what)
	}
	return node
}

func TestBuildChatRequestAndResponse(t *testing.T) {
	req := decodeChatRequest(t, chatRequestJSON)
	resp := decodeChatResponse(t, chatResponseJSON)

	obs := audit.Build(xir.ProtocolChatCompletions, xir.OperationChat, req, resp)

	if obs.Protocol != xir.ProtocolChatCompletions || obs.Operation != xir.OperationChat {
		t.Fatalf("protocol/operation = %q/%q", obs.Protocol, obs.Operation)
	}
	if obs.Model != "gpt-4o-2024" {
		t.Fatalf("model = %q, want response model", obs.Model)
	}
	if obs.Request.Model != "gpt-4o" {
		t.Fatalf("request model = %q", obs.Request.Model)
	}
	if len(obs.Request.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(obs.Request.Messages))
	}

	system := obs.Request.Messages[0]
	if system.Node.Role != xir.RoleSystem || len(system.Parts) != 1 {
		t.Fatalf("system message = %+v", system)
	}
	if system.Parts[0].Text != "You are a careful assistant." {
		t.Fatalf("system text = %q", system.Parts[0].Text)
	}

	user := obs.Request.Messages[1]
	if user.Node.Role != xir.RoleUser || len(user.Parts) != 2 {
		t.Fatalf("user message = %+v", user)
	}
	if user.Parts[0].Kind != string(xir.PartText) {
		t.Fatalf("user part kind = %q", user.Parts[0].Kind)
	}
	image := user.Parts[1]
	if image.Kind != string(xir.PartImage) || image.Media == nil {
		t.Fatalf("image node = %+v", image)
	}
	if image.Media.URL != "https://example.com/paris.png" {
		t.Fatalf("image URL = %q", image.Media.URL)
	}
	if indexOfNode(obs.Nodes, image.ID) < 0 {
		t.Fatalf("image node %s missing from flat nodes", image.ID)
	}

	if len(obs.Request.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(obs.Request.Tools))
	}
	tool := obs.Request.Tools[0]
	if tool.Kind != "tool" || tool.Name != "get_weather" || tool.ProtocolType != "function" {
		t.Fatalf("tool node = %+v", tool)
	}
	if !strings.Contains(string(tool.Raw), `"properties"`) {
		t.Fatalf("tool schema missing from raw: %s", tool.Raw)
	}

	if len(obs.Response.Parts) != 2 {
		t.Fatalf("response parts = %d, want 2", len(obs.Response.Parts))
	}
	text := obs.Response.Parts[0]
	if text.Kind != string(xir.PartText) || text.Text != "Let me check the weather." {
		t.Fatalf("response text = %+v", text)
	}
	if len(obs.Response.ToolCalls) != 1 {
		t.Fatalf("response tool calls = %d, want 1", len(obs.Response.ToolCalls))
	}
	call := obs.Response.ToolCalls[0]
	if call.Kind != string(xir.PartToolCall) || call.Name != "get_weather" || call.ToolCallID != "call_2" {
		t.Fatalf("tool call = %+v", call)
	}
	if call.Text != `{"city":"Paris"}` {
		t.Fatalf("tool call text = %q", call.Text)
	}
	if !json.Valid(call.Raw) || !strings.Contains(string(call.Raw), "Paris") {
		t.Fatalf("tool call raw = %s", call.Raw)
	}

	if obs.Usage == nil || obs.Response.Usage == nil {
		t.Fatal("usage not projected")
	}
	if obs.Usage.InputTokens != 12 || obs.Usage.OutputTokens != 7 || obs.Usage.TotalTokens != 19 {
		t.Fatalf("usage = %+v", obs.Usage)
	}
	if obs.Usage.ReasoningTokens != 3 || obs.Usage.Source != "chat" {
		t.Fatalf("usage detail = %+v", obs.Usage)
	}

	if len(obs.Nodes) == 0 {
		t.Fatal("flat nodes empty")
	}
	for _, node := range obs.Nodes {
		if !strings.HasPrefix(node.ID, "node_") || len(node.ID) != len("node_")+16 {
			t.Fatalf("bad node ID %q", node.ID)
		}
		if node.Path == "" {
			t.Fatalf("node %s has no path", node.ID)
		}
		if node.Section != audit.SectionRequest && node.Section != audit.SectionResponse {
			t.Fatalf("node %s section = %q", node.ID, node.Section)
		}
		if node.SpanEnd <= node.SpanStart {
			t.Fatalf("node %s span = %d..%d, want a decoded span", node.ID, node.SpanStart, node.SpanEnd)
		}
	}
	if _, err := json.Marshal(obs); err != nil {
		t.Fatalf("marshal observation: %v", err)
	}
}

func TestBuildToolCallsAndToolResults(t *testing.T) {
	req := decodeChatRequest(t, chatRequestJSON)
	obs := audit.Build(xir.ProtocolChatCompletions, xir.OperationChat, req, nil)

	assistant := obs.Request.Messages[2]
	call, ok := findNode(assistant.Parts, func(node audit.Node) bool {
		return node.Kind == string(xir.PartToolCall)
	})
	if !ok {
		t.Fatalf("assistant tool call missing from %+v", assistant.Parts)
	}
	if call.Name != "get_weather" || call.ToolCallID != "call_1" || call.Role != xir.RoleAssistant {
		t.Fatalf("tool call = %+v", call)
	}
	if call.ParentID != assistant.Node.ID {
		t.Fatalf("tool call parent = %q, want %q", call.ParentID, assistant.Node.ID)
	}

	toolMessage := obs.Request.Messages[3]
	result := toolMessage.Parts[0]
	if result.Kind != string(xir.PartToolResult) || result.ToolCallID != "call_1" {
		t.Fatalf("tool result = %+v", result)
	}
	if result.Text != "sunny, 22C" || result.Role != xir.RoleTool {
		t.Fatalf("tool result text/role = %q/%q", result.Text, result.Role)
	}
	if result.IsError {
		t.Fatal("tool result marked as error")
	}

	resp := decodeChatResponse(t, chatResponseJSON)
	resp.Parts = append(resp.Parts, xir.Part{
		Kind: xir.PartToolResult,
		ToolResult: &xir.ToolResult{
			CallID:  "call_2",
			Name:    "get_weather",
			Text:    `{"ok":true}`,
			JSON:    json.RawMessage(`{"ok":true}`),
			IsError: true,
		},
	})
	obs = audit.Build(xir.ProtocolChatCompletions, xir.OperationChat, req, resp)
	if len(obs.Response.ToolResults) != 1 {
		t.Fatalf("response tool results = %d, want 1", len(obs.Response.ToolResults))
	}
	responseResult := obs.Response.ToolResults[0]
	if responseResult.ToolCallID != "call_2" || !responseResult.IsError {
		t.Fatalf("response tool result = %+v", responseResult)
	}
	if responseResult.Role != xir.RoleTool {
		t.Fatalf("response tool result role = %q", responseResult.Role)
	}
	if !strings.Contains(string(responseResult.Raw), `"ok"`) {
		t.Fatalf("response tool result raw = %s", responseResult.Raw)
	}
}

func TestBuildReasoningAndSealed(t *testing.T) {
	visibleSealed := json.RawMessage(`{"type":"thinking","signature":"sig-1","thinking":"sealed-secret"}`)
	opaqueSealed := json.RawMessage(`{"encrypted_content":"encrypted-secret"}`)

	resp := &xir.Response{
		ID:         "resp_1",
		Model:      "m",
		Status:     "completed",
		StopReason: xir.FinishStop,
		Parts: []xir.Part{
			{
				Kind: xir.PartReasoning,
				Reasoning: &xir.Reasoning{
					Text:   "visible reasoning",
					Sealed: &xir.Sealed{Protocol: xir.ProtocolMessages, Kind: "thinking", Raw: visibleSealed},
				},
			},
			{
				Kind:      xir.PartReasoning,
				Reasoning: &xir.Reasoning{EncryptedContent: "encrypted-secret"},
			},
			{
				Kind:   xir.PartSealed,
				Sealed: &xir.Sealed{Protocol: xir.ProtocolResponses, Kind: "reasoning", Raw: opaqueSealed},
			},
			{Kind: xir.PartRefusal, Text: "I cannot help with that."},
		},
	}

	obs := audit.Build(xir.ProtocolMessages, xir.OperationMessages, nil, resp)
	if obs.Status != "completed" || obs.Response.StopReason != xir.FinishStop {
		t.Fatalf("status/stop = %q/%q", obs.Status, obs.Response.StopReason)
	}
	if len(obs.Response.Parts) != 4 {
		t.Fatalf("response parts = %d, want 4", len(obs.Response.Parts))
	}
	if len(obs.Response.Reasoning) != 3 {
		t.Fatalf("reasoning nodes = %d, want 3", len(obs.Response.Reasoning))
	}
	if len(obs.Response.Refusals) != 1 {
		t.Fatalf("refusal nodes = %d, want 1", len(obs.Response.Refusals))
	}

	visible := obs.Response.Parts[0]
	if visible.Kind != string(xir.PartReasoning) || visible.Text != "visible reasoning" {
		t.Fatalf("visible reasoning = %+v", visible)
	}
	sealedChild := mustFindNode(t, obs.Nodes, func(node audit.Node) bool {
		return node.ParentID == visible.ID && node.Kind == string(xir.PartSealed)
	}, "sealed child of visible reasoning")
	if sealedChild.Text != "" {
		t.Fatalf("sealed child decoded into text: %q", sealedChild.Text)
	}
	if string(sealedChild.Raw) != string(visibleSealed) {
		t.Fatalf("sealed child raw = %s", sealedChild.Raw)
	}
	if sealedChild.ProtocolType != "thinking" {
		t.Fatalf("sealed child protocol type = %q", sealedChild.ProtocolType)
	}

	opaque := obs.Response.Parts[1]
	if opaque.Kind != string(xir.PartSealed) || opaque.Text != "" {
		t.Fatalf("opaque reasoning = %+v", opaque)
	}
	if !strings.Contains(string(opaque.Raw), "encrypted-secret") {
		t.Fatalf("opaque reasoning raw = %s", opaque.Raw)
	}

	sealedOnly := obs.Response.Parts[2]
	if sealedOnly.Kind != string(xir.PartSealed) || sealedOnly.Text != "" {
		t.Fatalf("sealed-only part = %+v", sealedOnly)
	}
	if string(sealedOnly.Raw) != string(opaqueSealed) {
		t.Fatalf("sealed-only raw = %s", sealedOnly.Raw)
	}
	if sealedOnly.ProtocolType != "reasoning" {
		t.Fatalf("sealed-only protocol type = %q", sealedOnly.ProtocolType)
	}

	refusal := obs.Response.Parts[3]
	if refusal.Kind != string(xir.PartRefusal) || refusal.Text != "I cannot help with that." {
		t.Fatalf("refusal = %+v", refusal)
	}

	for _, node := range obs.Nodes {
		if strings.Contains(node.Text, "sealed-secret") || strings.Contains(node.Text, "encrypted-secret") {
			t.Fatalf("sealed content decoded into text on node %s: %q", node.ID, node.Text)
		}
	}
}

func TestBuildUsageErrorAndNilSides(t *testing.T) {
	empty := audit.Build(xir.ProtocolResponses, xir.OperationResponses, nil, nil)
	if empty == nil {
		t.Fatal("nil build returned nil")
	}
	if empty.Protocol != xir.ProtocolResponses || empty.Operation != xir.OperationResponses {
		t.Fatalf("nil build protocol/operation = %q/%q", empty.Protocol, empty.Operation)
	}
	if empty.Usage != nil || empty.Error != nil {
		t.Fatalf("nil build usage/error = %+v/%+v", empty.Usage, empty.Error)
	}
	if empty.Nodes == nil || len(empty.Nodes) != 0 {
		t.Fatalf("nil build nodes = %#v", empty.Nodes)
	}
	encoded, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty observation: %v", err)
	}
	if !strings.Contains(string(encoded), `"nodes":[]`) {
		t.Fatalf("empty observation nodes not []: %s", encoded)
	}

	requestOnly := &xir.Request{Model: "request-model", Messages: []xir.Message{{
		Role:  xir.RoleUser,
		Parts: []xir.Part{{Kind: xir.PartText, Text: "hi"}},
	}}}
	obs := audit.Build(xir.ProtocolChatCompletions, xir.OperationChat, requestOnly, nil)
	if obs.Model != "request-model" || len(obs.Nodes) == 0 {
		t.Fatalf("request-only observation = %+v", obs)
	}
	if obs.Response.ID != "" || obs.Response.Status != "" || len(obs.Response.Parts) != 0 {
		t.Fatalf("request-only response = %+v", obs.Response)
	}

	responseOnly := &xir.Response{
		ID:    "resp_1",
		Model: "response-model",
		Usage: &usage.Usage{InputTokens: 3, OutputTokens: 2, Source: "chat"},
		Error: &xir.Error{
			Status:  429,
			Type:    "rate_limit",
			Code:    "too_many_requests",
			Message: "slow down",
			Param:   "model",
			Raw:     json.RawMessage(`{"error":{"message":"slow down"}}`),
		},
	}
	obs = audit.Build(xir.ProtocolChatCompletions, xir.OperationChat, nil, responseOnly)
	if obs.Model != "response-model" || obs.Status != "error" {
		t.Fatalf("response-only status/model = %q/%q", obs.Status, obs.Model)
	}
	if obs.Usage == nil || obs.Usage.InputTokens != 3 || obs.Response.Usage == nil {
		t.Fatalf("response-only usage = %+v", obs.Usage)
	}
	if obs.Error == nil || obs.Error.Status != 429 || obs.Error.Code != "too_many_requests" {
		t.Fatalf("response-only error = %+v", obs.Error)
	}
	if !strings.Contains(string(obs.Error.Raw), "slow down") {
		t.Fatalf("response-only error raw = %s", obs.Error.Raw)
	}

	responseOnly.Usage.InputTokens = 99
	if obs.Usage.InputTokens != 3 {
		t.Fatalf("usage aliased the response: %d", obs.Usage.InputTokens)
	}
}

func TestBuildDeterministicIDsAndEvidence(t *testing.T) {
	first := audit.Build(
		xir.ProtocolChatCompletions, xir.OperationChat,
		decodeChatRequest(t, chatRequestJSON), decodeChatResponse(t, chatResponseJSON),
	)
	second := audit.Build(
		xir.ProtocolChatCompletions, xir.OperationChat,
		decodeChatRequest(t, chatRequestJSON), decodeChatResponse(t, chatResponseJSON),
	)
	if !reflect.DeepEqual(first.Nodes, second.Nodes) {
		t.Fatal("same input produced different nodes")
	}

	seen := make(map[string]struct{}, len(first.Nodes))
	for _, node := range first.Nodes {
		if _, exists := seen[node.ID]; exists {
			t.Fatalf("duplicate node ID %q", node.ID)
		}
		seen[node.ID] = struct{}{}
	}

	manual := &xir.Request{Model: "m", Messages: []xir.Message{{
		Role:  xir.RoleUser,
		Parts: []xir.Part{{Kind: xir.PartText, Text: "hi"}},
	}}}
	obs := audit.Build(xir.ProtocolChatCompletions, xir.OperationChat, manual, nil)
	message := obs.Request.Messages[0].Node
	part := obs.Request.Messages[0].Parts[0]

	if want := expectedID("request", "$.messages[0]", "message", 0); message.ID != want {
		t.Fatalf("message ID = %q, want %q", message.ID, want)
	}
	if want := expectedID("request", "$.messages[0].parts[0]", "text", 0); part.ID != want {
		t.Fatalf("part ID = %q, want %q", part.ID, want)
	}
	if part.ParentID != message.ID {
		t.Fatalf("part parent = %q, want %q", part.ParentID, message.ID)
	}

	wantEvidence := "trace#trace-1#node#" + part.ID + "#path#" + part.Path
	if got := audit.EvidencePath("trace-1", part); got != wantEvidence {
		t.Fatalf("evidence path = %q, want %q", got, wantEvidence)
	}

	other := &xir.Request{Messages: []xir.Message{
		{Role: xir.RoleUser, Parts: []xir.Part{{Kind: xir.PartText, Text: "first"}}},
		{Role: xir.RoleUser, Parts: []xir.Part{{Kind: xir.PartText, Text: "second"}}},
	}}
	otherObs := audit.Build(xir.ProtocolChatCompletions, xir.OperationChat, other, nil)
	otherPart := otherObs.Request.Messages[1].Parts[0]
	if otherPart.Path == part.Path {
		t.Fatalf("different message positions share path %q", part.Path)
	}
	if otherPart.ID == part.ID {
		t.Fatal("different path produced the same node ID")
	}
}

func TestBuildResponsesInstructionsStatusAndUsage(t *testing.T) {
	req := decodeResponsesRequest(t, `{
	  "model": "gpt-5",
	  "instructions": "Be terse.",
	  "input": [
	    {"role": "user", "content": [{"type": "input_text", "text": "Hello"}]}
	  ],
	  "stream": true
	}`)
	resp := decodeResponsesResponse(t, `{
	  "id": "resp_1",
	  "object": "response",
	  "status": "completed",
	  "model": "gpt-5",
	  "output": [
	    {"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "Hi"}]},
	    {"type": "reasoning", "summary": [{"type": "summary_text", "text": "thinking"}], "encrypted_content": "opaque"},
	    {"type": "function_call", "call_id": "call_9", "name": "lookup", "arguments": "{\"q\":\"x\"}"}
	  ],
	  "usage": {"input_tokens": 5, "output_tokens": 4, "total_tokens": 9}
	}`)

	obs := audit.Build(xir.ProtocolResponses, xir.OperationResponses, req, resp)
	if obs.Status != "completed" {
		t.Fatalf("status = %q", obs.Status)
	}
	if !obs.Request.Stream {
		t.Fatal("stream flag not projected")
	}
	if len(obs.Request.Instructions) != 1 || obs.Request.Instructions[0].Text != "Be terse." {
		t.Fatalf("instructions = %+v", obs.Request.Instructions)
	}
	if len(obs.Request.Messages) != 1 || obs.Request.Messages[0].Node.Role != xir.RoleUser {
		t.Fatalf("messages = %+v", obs.Request.Messages)
	}
	if len(obs.Response.ToolCalls) != 1 || obs.Response.ToolCalls[0].Name != "lookup" {
		t.Fatalf("response tool calls = %+v", obs.Response.ToolCalls)
	}
	if len(obs.Response.Reasoning) != 1 {
		t.Fatalf("response reasoning = %+v", obs.Response.Reasoning)
	}
	if obs.Usage == nil || obs.Usage.InputTokens != 5 || obs.Usage.OutputTokens != 4 || obs.Usage.TotalTokens != 9 {
		t.Fatalf("usage = %+v", obs.Usage)
	}
	if obs.Response.StopReason != xir.FinishToolUse {
		t.Fatalf("stop reason = %q", obs.Response.StopReason)
	}
}

func TestBuildMediaDoesNotLeakInlineBytes(t *testing.T) {
	const marker = "SECRET_MEDIA_BYTES_MUST_NOT_LEAK"

	req := &xir.Request{
		Model: "m",
		Messages: []xir.Message{{
			Role: xir.RoleUser,
			Parts: []xir.Part{
				{Kind: xir.PartImage, Media: &xir.Media{Kind: xir.MediaImage, URL: "data:image/png;base64," + marker, Filename: "cat.png"}},
				{Kind: xir.PartAudio, Media: &xir.Media{Kind: xir.MediaAudio, Data: marker, MediaType: "wav", Filename: "clip.wav"}},
				{Kind: xir.PartFile, Media: &xir.Media{Kind: xir.MediaFile, Data: marker, MediaType: "application/pdf", Filename: "doc.pdf"}},
				{Kind: xir.PartVideo, Media: &xir.Media{Kind: xir.MediaVideo, URL: "https://example.com/clip.mp4", Filename: "clip.mp4"}},
			},
		}},
	}

	obs := audit.Build(xir.ProtocolChatCompletions, xir.OperationChat, req, nil)
	parts := obs.Request.Messages[0].Parts
	if len(parts) != 4 {
		t.Fatalf("parts = %d, want 4", len(parts))
	}

	image := parts[0]
	if image.Media == nil || image.Media.URL != "" || image.Media.MediaType != "image/png" || image.Media.Filename != "cat.png" {
		t.Fatalf("image media = %+v", image.Media)
	}
	if image.Text != "" || len(image.Raw) != 0 {
		t.Fatalf("image leaked content: text=%q raw=%s", image.Text, image.Raw)
	}

	audio := parts[1]
	if audio.Media == nil || audio.Media.URL != "" || audio.Media.MediaType != "wav" || audio.Media.Filename != "clip.wav" {
		t.Fatalf("audio media = %+v", audio.Media)
	}
	if audio.Text != "" || len(audio.Raw) != 0 {
		t.Fatalf("audio leaked content: text=%q raw=%s", audio.Text, audio.Raw)
	}

	file := parts[2]
	if file.Media == nil || file.Media.MediaType != "application/pdf" || file.Media.Filename != "doc.pdf" {
		t.Fatalf("file media = %+v", file.Media)
	}

	video := parts[3]
	if video.Media == nil || video.Media.URL != "https://example.com/clip.mp4" || video.Media.Filename != "clip.mp4" {
		t.Fatalf("video media = %+v", video.Media)
	}

	encoded, err := json.Marshal(obs)
	if err != nil {
		t.Fatalf("marshal observation: %v", err)
	}
	if strings.Contains(string(encoded), marker) {
		t.Fatalf("inline media bytes leaked into observation: %s", encoded)
	}
	for _, node := range obs.Nodes {
		if strings.Contains(node.Text, marker) || strings.Contains(string(node.Raw), marker) {
			t.Fatalf("inline media bytes leaked into node %s", node.ID)
		}
	}
}

func expectedID(section, path, protocolType string, index int) string {
	sum := sha1.Sum([]byte(section + "|" + path + "|" + protocolType + "|" + strconv.Itoa(index)))
	return "node_" + hex.EncodeToString(sum[:])[:16]
}

func indexOfNode(nodes []audit.Node, id string) int {
	for i, node := range nodes {
		if node.ID == id {
			return i
		}
	}
	return -1
}
