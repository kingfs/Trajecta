package convert_test

// Cross-engine conversion benchmarks: the same body through the legacy engine
// (pkg/protocolconvert via the pkg/llm IR) and the XIR hub (pkg/xir/convert),
// under the exact production call shapes used by internal/proxy. Run with:
//
//	go test -run '^$' -bench 'BenchmarkConversion' -benchmem -count 6 ./pkg/xir/convert
//
// The parity corpus proves the outputs are equivalent; this file measures what
// the conversion costs. Bodies are inline so the cost of one shape is stable
// even when the corpus grows.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/protocolconvert"
	convert "github.com/kingfs/Trajecta/pkg/xir/convert"
)

const benchWireModel = "wire-model"

type conversionBenchCase struct {
	name string
	from protocolconvert.Protocol
	to   protocolconvert.Protocol
	body []byte
}

func benchLongText() string {
	return strings.Repeat("The quick brown fox jumps over the lazy dog. ", 6000) // ~270 KB
}

func benchTools(n int) []any {
	tools := make([]any, 0, n)
	for i := 0; i < n; i++ {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        fmt.Sprintf("tool_%d", i),
				"description": strings.Repeat("look up a record ", 12),
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":   map[string]any{"type": "string"},
						"note": map[string]any{"type": "string"},
					},
					"required": []string{"id"},
				},
			},
		})
	}
	return tools
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func benchConversionRequests() []conversionBenchCase {
	textChat := mustJSON(map[string]any{
		"model": "src-model",
		"messages": []any{
			map[string]any{"role": "system", "content": "be concise"},
			map[string]any{"role": "user", "content": "hello conversion"},
		},
	})
	toolLoopChat := mustJSON(map[string]any{
		"model": "src-model",
		"tools": benchTools(8),
		"messages": []any{
			map[string]any{"role": "user", "content": "weather in SH?"},
			map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
				map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "tool_1", "arguments": `{"id":"1"}`}},
			}},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "sunny"},
		},
	})
	chatMessages := mustJSON(map[string]any{
		"model":      "src-model",
		"max_tokens": 256,
		"messages":   []any{map[string]any{"role": "user", "content": "hello"}},
	})
	chatLarge := mustJSON(map[string]any{
		"model":      "src-model",
		"max_tokens": 512,
		"messages": []any{
			map[string]any{"role": "user", "content": benchLongText()},
		},
	})
	responsesRequest := mustJSON(map[string]any{
		"model": "src-model",
		"input": "hello from responses",
	})
	messagesRequest := mustJSON(map[string]any{
		"model":      "src-model",
		"max_tokens": 256,
		"messages":   []any{map[string]any{"role": "user", "content": "hello from messages"}},
	})
	geminiRequest := mustJSON(map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hello from gemini"}}}},
	})

	return []conversionBenchCase{
		{name: "chat_to_messages_text", from: protocolconvert.ProtocolChatCompletions, to: protocolconvert.ProtocolMessages, body: chatMessages},
		{name: "chat_to_messages_tool_loop", from: protocolconvert.ProtocolChatCompletions, to: protocolconvert.ProtocolMessages, body: toolLoopChat},
		{name: "chat_to_messages_large", from: protocolconvert.ProtocolChatCompletions, to: protocolconvert.ProtocolMessages, body: chatLarge},
		{name: "chat_to_responses_text", from: protocolconvert.ProtocolChatCompletions, to: protocolconvert.ProtocolResponses, body: textChat},
		{name: "chat_to_gemini_tools", from: protocolconvert.ProtocolChatCompletions, to: protocolconvert.ProtocolGemini, body: toolLoopChat},
		{name: "messages_to_chat_text", from: protocolconvert.ProtocolMessages, to: protocolconvert.ProtocolChatCompletions, body: messagesRequest},
		{name: "responses_to_chat_text", from: protocolconvert.ProtocolResponses, to: protocolconvert.ProtocolChatCompletions, body: responsesRequest},
		{name: "gemini_to_chat_text", from: protocolconvert.ProtocolGemini, to: protocolconvert.ProtocolChatCompletions, body: geminiRequest},
	}
}

func benchConversionResponses() []conversionBenchCase {
	chatResponse := mustJSON(map[string]any{
		"id": "chatcmpl-1", "object": "chat.completion", "created": 1, "model": "src-model",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": "hello"},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	})
	messagesResponse := mustJSON(map[string]any{
		"id": "msg-1", "type": "message", "role": "assistant", "model": "src-model",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "plan", "signature": "sig-1"},
			map[string]any{"type": "text", "text": "answer"},
		},
		"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	responsesResponse := mustJSON(map[string]any{
		"id": "resp_1", "object": "response", "created_at": 1, "model": "src-model",
		"status": "completed",
		"output": []any{map[string]any{
			"type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": "hello", "annotations": []any{}}},
		}},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
	})
	geminiResponse := mustJSON(map[string]any{
		"candidates": []any{
			map[string]any{
				"content":      map[string]any{"role": "model", "parts": []any{map[string]any{"text": "first"}}},
				"finishReason": "STOP", "index": 0,
			},
			map[string]any{
				"content":      map[string]any{"role": "model", "parts": []any{map[string]any{"text": "second"}}},
				"finishReason": "STOP", "index": 1,
			},
		},
		"usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15},
	})

	return []conversionBenchCase{
		{name: "chat_to_messages_text", from: protocolconvert.ProtocolChatCompletions, to: protocolconvert.ProtocolMessages, body: chatResponse},
		{name: "messages_to_chat_thinking", from: protocolconvert.ProtocolMessages, to: protocolconvert.ProtocolChatCompletions, body: messagesResponse},
		{name: "responses_to_chat_envelope", from: protocolconvert.ProtocolResponses, to: protocolconvert.ProtocolChatCompletions, body: responsesResponse},
		{name: "responses_to_messages_envelope", from: protocolconvert.ProtocolResponses, to: protocolconvert.ProtocolMessages, body: responsesResponse},
		{name: "gemini_to_chat_candidates", from: protocolconvert.ProtocolGemini, to: protocolconvert.ProtocolChatCompletions, body: geminiResponse},
	}
}

func benchmarkRequestConversion(b *testing.B, tc conversionBenchCase) {
	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(tc.body)))
		for i := 0; i < b.N; i++ {
			result, err := protocolconvert.ConvertRequest(tc.body, tc.from, tc.to, benchWireModel, false)
			if err != nil {
				b.Fatal(err)
			}
			if len(result.FatalWarnings()) > 0 {
				b.Fatalf("legacy refused: %+v", result.FatalWarnings())
			}
			benchSink = result.Body
		}
	})
	b.Run("xir", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(tc.body)))
		for i := 0; i < b.N; i++ {
			result, err := convert.ConvertRequest(context.Background(), tc.body, xirProtocol(tc.from), xirProtocol(tc.to), convert.Options{Model: benchWireModel, Policy: convert.Strict})
			if err != nil {
				b.Fatal(err)
			}
			benchSink = result.Bytes
		}
	})
}

func benchmarkResponseConversion(b *testing.B, tc conversionBenchCase) {
	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(tc.body)))
		for i := 0; i < b.N; i++ {
			result, err := protocolconvert.ConvertResponse(tc.body, tc.from, tc.to, benchWireModel, false)
			if err != nil {
				b.Fatal(err)
			}
			benchSink = result.Body
		}
	})
	b.Run("xir", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(tc.body)))
		for i := 0; i < b.N; i++ {
			result, err := convert.ConvertResponse(context.Background(), tc.body, xirProtocol(tc.from), xirProtocol(tc.to), convert.Options{Model: benchWireModel, Policy: convert.Strict})
			if err != nil {
				b.Fatal(err)
			}
			benchSink = result.Bytes
		}
	})
}

// benchSink keeps the converted bytes alive so the compiler cannot elide the
// conversion.
var benchSink []byte

func BenchmarkConversionRequests(b *testing.B) {
	for _, tc := range benchConversionRequests() {
		b.Run(tc.name, func(b *testing.B) {
			benchmarkRequestConversion(b, tc)
		})
	}
}

func BenchmarkConversionResponses(b *testing.B) {
	for _, tc := range benchConversionResponses() {
		b.Run(tc.name, func(b *testing.B) {
			benchmarkResponseConversion(b, tc)
		})
	}
}
