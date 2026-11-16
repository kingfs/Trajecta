package chatclient

import (
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/responses/runtime"
)

// HTTPError is constructed by the proxy's local Responses adapter and
// classified by httpapi; its message must name the status and carry the
// provider body, marking a truncated one so the operator knows it is partial.
func TestHTTPErrorMessageIncludesStatusAndTruncationMarker(t *testing.T) {
	body := strings.Repeat("x", 64)
	full := HTTPError{StatusCode: 502, Status: "502 Bad Gateway", Body: body}
	if got := full.Error(); !strings.Contains(got, "status 502") || !strings.Contains(got, body) || strings.Contains(got, "truncated") {
		t.Fatalf("Error() = %q, want status and full body without truncation marker", got)
	}
	truncated := HTTPError{StatusCode: 502, Status: "502 Bad Gateway", Body: body, BodyTruncated: true}
	if got := truncated.Error(); !strings.Contains(got, "status 502") || !strings.Contains(got, body) || !strings.Contains(got, "(truncated)") {
		t.Fatalf("Error() = %q, want status, body and truncation marker", got)
	}
}

func TestAggregateChatCompletionStreamAggregatesSSEChunks(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello "},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"world"},"finish_reason":"stop"}]}`,
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`,
		`data: [DONE]`,
	}, "\n")

	resp, err := AggregateChatCompletionStreamWithCallback(strings.NewReader(body), nil)
	if err != nil {
		t.Fatalf("AggregateChatCompletionStreamWithCallback() error = %v", err)
	}

	if resp.ID != "chatcmpl_stream" || resp.Model != "gpt-4o" {
		t.Fatalf("response identity = %q/%q, want stream id/model", resp.ID, resp.Model)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(resp.Choices))
	}
	choice := resp.Choices[0]
	if choice.Message.Role != "assistant" || choice.Message.Content != "Hello world" || choice.FinishReason != "stop" {
		t.Fatalf("choice = %+v, want aggregated assistant text", choice)
	}
	if resp.Usage.TotalTokens != 7 {
		t.Fatalf("usage = %+v, want total_tokens 7", resp.Usage)
	}
}

func TestAggregateChatCompletionStreamCallbackReceivesContentDeltas(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello "},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"world"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}, "\n")

	var deltas []string
	resp, err := AggregateChatCompletionStreamWithCallback(strings.NewReader(body), func(event runtime.ChatStreamEvent) error {
		if event.ContentDelta != "" {
			deltas = append(deltas, event.ContentDelta)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("AggregateChatCompletionStreamWithCallback() error = %v", err)
	}

	if strings.Join(deltas, "|") != "Hello |world" {
		t.Fatalf("deltas = %#v, want separate stream chunks", deltas)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "Hello world" {
		t.Fatalf("aggregated response = %+v, want full content", resp)
	}
}

func TestAggregateChatCompletionStreamAggregatesToolCallDeltas(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_tools","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\""}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_tools","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n")

	resp, err := AggregateChatCompletionStreamWithCallback(strings.NewReader(body), nil)
	if err != nil {
		t.Fatalf("AggregateChatCompletionStreamWithCallback() error = %v", err)
	}

	if len(resp.Choices) != 1 || len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("choices = %+v, want one tool call", resp.Choices)
	}
	call := resp.Choices[0].Message.ToolCalls[0]
	if call.ID != "call_1" || call.Type != "function" || call.Function.Name != "weather" || call.Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("tool call = %+v, want aggregated function call", call)
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls", resp.Choices[0].FinishReason)
	}
}

func TestAggregateChatCompletionStreamCallbackReceivesToolCallDeltas(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_tools","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\""}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl_tools","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n")

	var deltas []runtime.ChatStreamToolCallDelta
	resp, err := AggregateChatCompletionStreamWithCallback(strings.NewReader(body), func(event runtime.ChatStreamEvent) error {
		deltas = append(deltas, event.ToolCallDeltas...)
		return nil
	})
	if err != nil {
		t.Fatalf("AggregateChatCompletionStreamWithCallback() error = %v", err)
	}

	if len(deltas) != 2 {
		t.Fatalf("tool deltas = %#v, want two chunks", deltas)
	}
	if deltas[0].Index != 0 || deltas[0].ID != "call_1" || deltas[0].Type != "function" || deltas[0].FunctionName != "weather" || deltas[0].ArgumentsDelta != `{"city"` {
		t.Fatalf("first tool delta = %#v", deltas[0])
	}
	if deltas[1].Index != 0 || deltas[1].ArgumentsDelta != `:"Paris"}` {
		t.Fatalf("second tool delta = %#v", deltas[1])
	}
	call := resp.Choices[0].Message.ToolCalls[0]
	if call.Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("aggregated tool arguments = %q", call.Function.Arguments)
	}
}

func TestAggregateChatCompletionStreamInvalidJSONReturnsDiagnosticError(t *testing.T) {
	_, err := AggregateChatCompletionStreamWithCallback(strings.NewReader("data: {bad json}\n"), nil)
	if err == nil {
		t.Fatal("AggregateChatCompletionStreamWithCallback() error = nil, want stream decode error")
	}
	if !strings.Contains(err.Error(), "decode chat completion stream chunk JSON") {
		t.Fatalf("error = %q, want stream decode diagnostic", err)
	}
}
