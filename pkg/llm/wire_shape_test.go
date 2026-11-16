package llm

// Wire-shape regressions for the P5 legacy-engine defects (see
// .scratch/audit/p5-parity.md §4.1 ③⑧ and .scratch/audit/p5-legacy-fix.md):
//
//   - S-INPUT-TEXT: Responses message-item text parts must spell the payload
//     member "text", never a member named after the part type.
//   - S-GEM-PARALLEL: parallel Gemini functionCalls (no ids, often the same
//     function name) must all survive into every target protocol.
//   - S-GEM-ARGS: Gemini functionCall.args must reach Anthropic tool_use.input
//     instead of an empty object.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const geminiParallelCallsResponse = `{"candidates":[{"content":{"role":"model","parts":[{"text":"checking both"},{"functionCall":{"name":"weather","args":{"city":"SH"}}},{"functionCall":{"name":"weather","args":{"city":"BJ"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":9,"totalTokenCount":19}}`

func parseGeminiResponseForShape(t *testing.T, body string) LLMResponse {
	t.Helper()
	resp, err := ParseResponse(ProviderGoogleGenAI, "/v1beta/models:generateContent", []byte(body))
	require.NoError(t, err)
	require.Len(t, resp.Candidates, 1)
	return resp
}

// ---- S-INPUT-TEXT: Responses item text member key ----

func TestToOpenAIResponsesTextMemberKey(t *testing.T) {
	req := LLMRequest{
		Model: "gpt-5",
		Messages: []LLMMessage{
			{Role: "user", Content: []LLMContent{{Type: "text", Text: "Hello"}}},
			{Role: "assistant", Content: []LLMContent{{Type: "text", Text: "Hi there"}}},
		},
	}
	data, err := json.Marshal(req.ToOpenAIResponses())
	require.NoError(t, err)

	var wire struct {
		Input []struct {
			Type    string           `json:"type"`
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"input"`
	}
	require.NoError(t, json.Unmarshal(data, &wire))
	require.Len(t, wire.Input, 2)

	userPart := wire.Input[0].Content[0]
	assert.Equal(t, "message", wire.Input[0].Type)
	assert.Equal(t, "input_text", userPart["type"])
	assert.Equal(t, "Hello", userPart["text"])
	assert.NotContains(t, userPart, "input_text", "text payload must use the text member, not a type-named member")

	assistantPart := wire.Input[1].Content[0]
	assert.Equal(t, "output_text", assistantPart["type"])
	assert.Equal(t, "Hi there", assistantPart["text"])
	assert.NotContains(t, assistantPart, "output_text", "text payload must use the text member, not a type-named member")
}

func TestToOpenAIResponsesResponseTextMemberKey(t *testing.T) {
	resp := LLMResponse{
		ID:    "resp_1",
		Model: "m",
		Candidates: []LLMCandidate{{
			Role:         "assistant",
			Content:      []LLMContent{{Type: "text", Text: "Hi there"}},
			FinishReason: "stop",
		}},
	}
	data, err := json.Marshal(resp.ToOpenAIResponsesResponse())
	require.NoError(t, err)

	var wire struct {
		Output []struct {
			Type    string           `json:"type"`
			Content []map[string]any `json:"content"`
		} `json:"output"`
		OutputText string `json:"output_text"`
	}
	require.NoError(t, json.Unmarshal(data, &wire))
	require.Len(t, wire.Output, 1)
	require.Len(t, wire.Output[0].Content, 1)

	part := wire.Output[0].Content[0]
	assert.Equal(t, "output_text", part["type"])
	assert.Equal(t, "Hi there", part["text"])
	assert.NotContains(t, part, "output_text", "text payload must use the text member, not a type-named member")
	// The top-level output_text convenience member of the envelope is a
	// different field and stays.
	assert.Equal(t, "Hi there", wire.OutputText)
}

func TestParseOpenAIResponsesLegacyMemberKeyTolerance(t *testing.T) {
	// Cassettes recorded before the fix carry the wrong member key; decoding
	// must keep reading them (encode-side fix only).
	reqBody := []byte(`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","input_text":"Hello"}]}]}`)
	req, err := ParseRequest(ProviderOpenAICompatible, "/v1/responses", reqBody)
	require.NoError(t, err)
	require.Len(t, req.Messages, 1)
	require.Len(t, req.Messages[0].Content, 1)
	assert.Equal(t, "Hello", req.Messages[0].Content[0].Text)

	respBody := []byte(`{"id":"resp_1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","output_text":"Hi"}]}]}`)
	resp, err := ParseResponse(ProviderOpenAICompatible, "/v1/responses", respBody)
	require.NoError(t, err)
	require.Len(t, resp.Candidates, 1)
	assert.Equal(t, "Hi", resp.Candidates[0].ResponseText())
}

// ---- S-GEM-PARALLEL: every parallel call survives, with synthesized ids ----

func TestGeminiParallelFunctionCallsToChat(t *testing.T) {
	resp := parseGeminiResponseForShape(t, geminiParallelCallsResponse)
	calls := resp.Candidates[0].ResponseToolCalls()
	require.Len(t, calls, 2, "parallel same-name Gemini calls must not be merged")

	chat := resp.ToOpenAIResponse()
	require.Len(t, chat.Choices, 1)
	toolCalls := chat.Choices[0].Message.ToolCalls
	require.Len(t, toolCalls, 2)
	// Id synthesis follows the existing single-call rule (call_<n>).
	assert.Equal(t, "call_1", toolCalls[0].ID)
	assert.Equal(t, "call_2", toolCalls[1].ID)
	assert.Equal(t, "weather", toolCalls[0].Function.Name)
	assert.Equal(t, "weather", toolCalls[1].Function.Name)
	assert.JSONEq(t, `{"city":"SH"}`, toolCalls[0].Function.Arguments)
	assert.JSONEq(t, `{"city":"BJ"}`, toolCalls[1].Function.Arguments)
	assert.Equal(t, "checking both", chat.Choices[0].Message.Content)
}

func TestGeminiParallelFunctionCallsToMessages(t *testing.T) {
	resp := parseGeminiResponseForShape(t, geminiParallelCallsResponse)
	msg := resp.ToAnthropicResponse()

	var toolUse []AnthropicContentBlock
	for _, block := range msg.Content {
		if block.Type == "tool_use" {
			toolUse = append(toolUse, block)
		}
	}
	require.Len(t, toolUse, 2)
	// Single-call id synthesis is unchanged; parallel calls stay distinct so
	// each tool_result can reference its own call.
	assert.Equal(t, "toolu_converted", toolUse[0].ID)
	assert.Equal(t, "toolu_converted_2", toolUse[1].ID)
	assert.Equal(t, map[string]any{"city": "SH"}, toolUse[0].Input)
	assert.Equal(t, map[string]any{"city": "BJ"}, toolUse[1].Input)
}

func TestGeminiParallelFunctionCallsToResponses(t *testing.T) {
	resp := parseGeminiResponseForShape(t, geminiParallelCallsResponse)
	out := resp.ToOpenAIResponsesResponse()

	var calls []OpenAIResponsesOutputItem
	for _, item := range out.Output {
		if item.Type == "function_call" {
			calls = append(calls, item)
		}
	}
	require.Len(t, calls, 2)
	assert.Equal(t, "call_1", calls[0].CallID)
	assert.Equal(t, "call_2", calls[1].CallID)
	assert.JSONEq(t, `{"city":"SH"}`, calls[0].Arguments)
	assert.JSONEq(t, `{"city":"BJ"}`, calls[1].Arguments)
}

func TestGeminiParallelFunctionCallsToGemini(t *testing.T) {
	resp := parseGeminiResponseForShape(t, geminiParallelCallsResponse)
	out := resp.toGenerateContentResponse()
	require.Len(t, out.Candidates, 1)

	var calls []GeminiPart
	for _, part := range out.Candidates[0].Content.Parts {
		if part.FunctionCall != nil {
			calls = append(calls, part)
		}
	}
	require.Len(t, calls, 2)
	assert.JSONEq(t, `{"city":"SH"}`, string(calls[0].FunctionCall.Args))
	assert.JSONEq(t, `{"city":"BJ"}`, string(calls[1].FunctionCall.Args))
}

// ---- S-GEM-ARGS: functionCall.args fidelity ----

func TestGeminiFunctionCallArgsCarried(t *testing.T) {
	resp := parseGeminiResponseForShape(t, `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"weather","args":{"city":"SH","days":3}}}]},"finishReason":"STOP"}]}`)
	calls := resp.Candidates[0].ResponseToolCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, map[string]any{"city": "SH", "days": float64(3)}, calls[0].Args)
	assert.JSONEq(t, `{"city":"SH","days":3}`, calls[0].ArgsText)

	msg := resp.ToAnthropicResponse()
	var toolUse *AnthropicContentBlock
	for i := range msg.Content {
		if msg.Content[i].Type == "tool_use" {
			toolUse = &msg.Content[i]
		}
	}
	require.NotNil(t, toolUse)
	assert.Equal(t, map[string]any{"city": "SH", "days": float64(3)}, toolUse.Input, "tool_use.input must carry functionCall.args, not {}")

	chat := resp.ToOpenAIResponse()
	require.Len(t, chat.Choices[0].Message.ToolCalls, 1)
	assert.JSONEq(t, `{"city":"SH","days":3}`, chat.Choices[0].Message.ToolCalls[0].Function.Arguments)
}

func TestGeminiFunctionCallEmptyAndAbsentArgs(t *testing.T) {
	resp := parseGeminiResponseForShape(t, `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"ping","args":{}}},{"functionCall":{"name":"pong"}}]},"finishReason":"STOP"}]}`)
	calls := resp.Candidates[0].ResponseToolCalls()
	require.Len(t, calls, 2)

	msg := resp.ToAnthropicResponse()
	var toolUse []AnthropicContentBlock
	for _, block := range msg.Content {
		if block.Type == "tool_use" {
			toolUse = append(toolUse, block)
		}
	}
	require.Len(t, toolUse, 2)
	// An empty or absent argument object stays an empty object on the wire;
	// the call itself is never dropped.
	assert.Equal(t, map[string]any{}, toolUse[0].Input)
	assert.Equal(t, map[string]any{}, toolUse[1].Input)

	chat := resp.ToOpenAIResponse()
	toolCalls := chat.Choices[0].Message.ToolCalls
	require.Len(t, toolCalls, 2)
	assert.Equal(t, "{}", toolCalls[0].Function.Arguments)
	// Absent args keep the encoder's pre-existing synthesis (out of scope of
	// the three registered defects); only assert it stays valid JSON.
	assert.True(t, json.Valid([]byte(toolCalls[1].Function.Arguments)))
}

// ---- ResponseToolCalls merge semantics ----

func TestResponseToolCallsMergesDuplicateSpellings(t *testing.T) {
	// An adapter that populated both ToolCalls and tool_use content blocks
	// for the same call must not emit it twice, even when the arguments are
	// spelled differently (raw upstream text vs parsed map).
	candidate := LLMCandidate{
		ToolCalls: []LLMToolCall{{ID: "call_1", Type: "function", Name: "weather", ArgsText: `{"city": "SH"}`}},
		Content: []LLMContent{{
			Type:       "tool_use",
			ToolCallID: "call_1",
			ToolName:   "weather",
			ToolArgs:   map[string]any{"city": "SH"},
		}},
	}
	calls := candidate.ResponseToolCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "call_1", calls[0].ID)
}

func TestResponseToolCallsKeepsIdenticalParallelCalls(t *testing.T) {
	// Two id-less calls with identical name AND arguments are still two
	// separate calls; the merge must not collapse them.
	candidate := LLMCandidate{
		ToolCalls: []LLMToolCall{
			{Name: "weather", ArgsText: `{"city":"SH"}`},
			{Name: "weather", ArgsText: `{"city":"SH"}`},
		},
	}
	require.Len(t, candidate.ResponseToolCalls(), 2)
}
