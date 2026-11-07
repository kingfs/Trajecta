package protocolconvert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/pkg/llm"
)

// ConvertStreamResponse converts an upstream SSE stream body from one
// protocol to the client's protocol. It aggregates the stream through
// pkg/llm's stream parser and re-encodes it as SSE in the target protocol
// with a single text delta (plus any tool calls in the final frame). This
// is deliberately not a frame-by-frame translator: incremental upstream
// ordering is not preserved, but the client receives a well-formed stream
// in its own protocol, which is what a converted call needs.
func ConvertStreamResponse(body []byte, from Protocol, to Protocol, model string, vertex bool) (Result, error) {
	from, to = canonical(from), canonical(to)
	parsed, err := llm.ParseStreamResponse(ProviderFor(from, vertex), from.Endpoint(), body)
	if err != nil {
		return Result{}, fmt.Errorf("protocolconvert: parse %s stream: %w", from, err)
	}
	if model != "" {
		parsed.Model = model
	}
	out, err := encodeStream(parsed, to)
	if err != nil {
		return Result{}, fmt.Errorf("protocolconvert: encode %s stream: %w", to, err)
	}
	return Result{Body: out, Warnings: responseWarnings(parsed, from, to), From: from, To: to}, nil
}

func encodeStream(resp llm.LLMResponse, to Protocol) ([]byte, error) {
	var buf bytes.Buffer
	text, toolCalls := streamTextAndTools(resp)
	created := resp.CreatedAt
	if created == 0 {
		created = time.Now().Unix()
	}
	id := resp.ID
	if id == "" {
		id = "chatcmpl-converted"
	}
	switch to {
	case ProtocolChatCompletions:
		return encodeChatStream(&buf, id, resp.Model, created, text, toolCalls, resp.Usage)
	case ProtocolResponses:
		return encodeResponsesStream(&buf, id, resp.Model, created, text, toolCalls, resp.Usage)
	case ProtocolMessages:
		return encodeMessagesStream(&buf, id, resp.Model, text, toolCalls, resp.Usage)
	case ProtocolGemini:
		return encodeGeminiStream(&buf, resp.Model, text, toolCalls, resp.Usage)
	}
	return nil, fmt.Errorf("protocolconvert: unsupported stream protocol %q", to)
}

func streamTextAndTools(resp llm.LLMResponse) (string, []llm.LLMToolCall) {
	if len(resp.Candidates) == 0 {
		return "", nil
	}
	candidate := resp.Candidates[0]
	var sb strings.Builder
	for _, content := range candidate.Content {
		if content.Type == "text" {
			sb.WriteString(content.Text)
		}
	}
	return sb.String(), candidate.ToolCalls
}

func sseFrame(buf *bytes.Buffer, event string, payload any) {
	data, _ := json.Marshal(payload)
	if event != "" {
		fmt.Fprintf(buf, "event: %s\n", event)
	}
	fmt.Fprintf(buf, "data: %s\n\n", data)
}

func encodeChatStream(buf *bytes.Buffer, id, model string, created int64, text string, toolCalls []llm.LLMToolCall, usage *llm.LLMUsage) ([]byte, error) {
	base := map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model}
	if text != "" {
		sseFrame(buf, "", withChoice(base, map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": text}, "finish_reason": nil}))
	}
	for i, call := range toolCalls {
		args := call.ArgsText
		if args == "" && call.Args != nil {
			raw, _ := json.Marshal(call.Args)
			args = string(raw)
		}
		sseFrame(buf, "", withChoice(base, map[string]any{
			"index":         0,
			"delta":         map[string]any{"tool_calls": []any{map[string]any{"index": i, "id": call.ID, "type": "function", "function": map[string]any{"name": call.Name, "arguments": args}}}},
			"finish_reason": nil,
		}))
	}
	finish := "stop"
	if len(toolCalls) > 0 {
		finish = "tool_calls"
	}
	final := withChoice(base, map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish})
	if usage != nil {
		final["usage"] = map[string]any{"prompt_tokens": usage.InputTokens, "completion_tokens": usage.OutputTokens, "total_tokens": usage.TotalTokens}
	}
	sseFrame(buf, "", final)
	buf.WriteString("data: [DONE]\n\n")
	return buf.Bytes(), nil
}

func withChoice(base map[string]any, choice map[string]any) map[string]any {
	out := make(map[string]any, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out["choices"] = []any{choice}
	return out
}

func encodeResponsesStream(buf *bytes.Buffer, id, model string, created int64, text string, toolCalls []llm.LLMToolCall, usage *llm.LLMUsage) ([]byte, error) {
	sseFrame(buf, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": id, "object": "response", "created_at": created, "model": model, "status": "in_progress", "output": []any{}}})
	if text != "" {
		sseFrame(buf, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "msg_0", "output_index": 0, "content_index": 0, "delta": text})
		sseFrame(buf, "response.output_text.done", map[string]any{"type": "response.output_text.done", "item_id": "msg_0", "output_index": 0, "content_index": 0, "text": text})
	}
	var output []any
	if text != "" {
		output = append(output, map[string]any{"type": "message", "id": "msg_0", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}})
	}
	for _, call := range toolCalls {
		args := call.ArgsText
		if args == "" && call.Args != nil {
			raw, _ := json.Marshal(call.Args)
			args = string(raw)
		}
		output = append(output, map[string]any{"type": "function_call", "id": call.ID, "call_id": call.ID, "name": call.Name, "arguments": args, "status": "completed"})
	}
	completed := map[string]any{"id": id, "object": "response", "created_at": created, "model": model, "status": "completed", "output": output}
	if usage != nil {
		completed["usage"] = map[string]any{"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens, "total_tokens": usage.TotalTokens}
	}
	sseFrame(buf, "response.completed", map[string]any{"type": "response.completed", "response": completed})
	return buf.Bytes(), nil
}

func encodeMessagesStream(buf *bytes.Buffer, id, model, text string, toolCalls []llm.LLMToolCall, usage *llm.LLMUsage) ([]byte, error) {
	inputTokens, outputTokens := 0, 0
	if usage != nil {
		inputTokens, outputTokens = usage.InputTokens, usage.OutputTokens
	}
	sseFrame(buf, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": id, "type": "message", "role": "assistant", "model": model, "usage": map[string]any{"input_tokens": inputTokens, "output_tokens": 0}}})
	sseFrame(buf, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
	if text != "" {
		sseFrame(buf, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
	}
	sseFrame(buf, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	for i, call := range toolCalls {
		index := i + 1
		var input any
		if call.Args != nil {
			input = call.Args
		} else {
			input = map[string]any{}
			_ = json.Unmarshal([]byte(call.ArgsText), &input)
		}
		sseFrame(buf, "content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": input}})
		sseFrame(buf, "content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
	}
	stopReason := "end_turn"
	if len(toolCalls) > 0 {
		stopReason = "tool_use"
	}
	sseFrame(buf, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason}, "usage": map[string]any{"output_tokens": outputTokens}})
	sseFrame(buf, "message_stop", map[string]any{"type": "message_stop"})
	return buf.Bytes(), nil
}

func encodeGeminiStream(buf *bytes.Buffer, model, text string, toolCalls []llm.LLMToolCall, usage *llm.LLMUsage) ([]byte, error) {
	var parts []any
	if text != "" {
		parts = append(parts, map[string]any{"text": text})
	}
	for _, call := range toolCalls {
		var args any
		if call.Args != nil {
			args = call.Args
		} else {
			args = map[string]any{}
			_ = json.Unmarshal([]byte(call.ArgsText), &args)
		}
		parts = append(parts, map[string]any{"functionCall": map[string]any{"name": call.Name, "args": args}})
	}
	payload := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "finishReason": "STOP"}}}
	if usage != nil {
		payload["usageMetadata"] = map[string]any{"promptTokenCount": usage.InputTokens, "candidatesTokenCount": usage.OutputTokens, "totalTokenCount": usage.TotalTokens}
	}
	_ = model
	sseFrame(buf, "", payload)
	return buf.Bytes(), nil
}
