// Package llm normalizes requests, responses, stream events and token usage
// across the protocol families Trajecta records: OpenAI-compatible, Anthropic
// Messages, Google GenAI and Vertex. It exposes the per-protocol adapters, the
// trace semantics used by the monitor, and the streaming ResponsePipeline that
// extracts usage and timeline events from a recorded response.
//
// Normalization is for observation only: the adapters never translate a
// request into another protocol family for forwarding.
package llm

import (
	"encoding/json"
	"strings"
	"sync"
)

type JSONSchema = json.RawMessage

// ========== 统一内部抽象：请求 ==========

type LLMRequest struct {
	Model string `json:"model"`

	System   []LLMContent `json:"system,omitempty"`
	Messages []LLMMessage `json:"messages,omitempty"`
	Tools    []LLMTool    `json:"tools,omitempty"`

	// ToolChoice is the canonical tool-selection directive. The wire spelling
	// differs per protocol (a string for Chat Completions, an object for
	// Messages/Responses, functionCallingConfig for Gemini), so the adapters
	// parse it into this shape and re-encode it for the target.
	ToolChoice *ToolChoice `json:"tool_choice,omitempty"`
	StopSeq    []string    `json:"stop_sequences,omitempty"`

	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	TopK        *int     `json:"top_k,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`

	SafetySettings []LLMSafetyConfig `json:"safety_settings,omitempty"`

	// 热路径上只用 UserID，避免 map[string]any
	UserID string `json:"user_id,omitempty"`

	// 非热路径扩展字段（不在 benchmark 中使用）
	Extensions map[string]any `json:"extensions,omitempty"`
}

type LLMMessage struct {
	Role    string       `json:"role"` // user, assistant, system, tool, model
	Content []LLMContent `json:"content"`
}

type LLMContent struct {
	Type string `json:"type"` // text, image, audio, video, tool_use, tool_result

	Text string `json:"text,omitempty"`
	ID   string `json:"id,omitempty"`

	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	ToolArgs   map[string]any `json:"tool_args,omitempty"`
	ToolResult map[string]any `json:"tool_result,omitempty"`
	Refusal    string         `json:"refusal,omitempty"`
}

type LLMTool struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Parameters  JSONSchema `json:"parameters,omitempty"`
	// Strict asks the provider to enforce the schema exactly. Only Chat
	// Completions and Responses can carry it; conversion to Anthropic/Gemini
	// rejects a required strict tool instead of silently relaxing it.
	Strict *bool `json:"strict,omitempty"`
}

type LLMSafetyConfig struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

// ========== 统一内部抽象：响应 ==========

type LLMResponse struct {
	ID        string `json:"id,omitempty"`
	Model     string `json:"model,omitempty"`
	CreatedAt int64  `json:"created_at,omitempty"`

	Candidates []LLMCandidate `json:"candidates"`

	Usage  *LLMUsage         `json:"usage,omitempty"`
	Safety []LLMSafetyRating `json:"safety,omitempty"`

	Extensions map[string]any `json:"extensions,omitempty"`
}

type LLMCandidate struct {
	Index        int          `json:"index,omitempty"`
	Role         string       `json:"role,omitempty"` // assistant / tool / system / model
	Content      []LLMContent `json:"content"`
	FinishReason string       `json:"finish_reason,omitempty"`

	ToolCalls []LLMToolCall `json:"tool_calls,omitempty"`
	Refusal   *LLMRefusal   `json:"refusal,omitempty"`

	Extensions map[string]any `json:"extensions,omitempty"`
}

type LLMToolCall struct {
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"`
	Name     string         `json:"name"`
	Args     map[string]any `json:"args,omitempty"`
	ArgsText string         `json:"args_text,omitempty"`
}

type LLMRefusal struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// ToolChoice is the protocol-neutral tool-selection directive carried by the
// IR. Mode is one of "auto", "none", "required" or "tool"; Name is set only
// for Mode == "tool". A nil *ToolChoice means the request did not send one.
type ToolChoice struct {
	Mode string `json:"mode,omitempty"`
	Name string `json:"name,omitempty"`
}

// ResponseText concatenates every text block of a candidate. The blocks of one
// assistant turn are one message, so the join is empty rather than a separator:
// a separator would invent bytes the upstream never sent.
func (c LLMCandidate) ResponseText() string {
	var builder strings.Builder
	for _, content := range c.Content {
		if content.Type == "text" {
			builder.WriteString(content.Text)
		}
	}
	return builder.String()
}

// ResponseToolCalls returns the candidate's tool calls, merging the two places
// an adapter may have put them: Candidate.ToolCalls (Chat/Responses/Gemini) and
// "tool_use" content blocks (Anthropic). The merge is deduplicated by call id
// and name, so an adapter that populated both does not emit the call twice.
func (c LLMCandidate) ResponseToolCalls() []LLMToolCall {
	out := make([]LLMToolCall, 0, len(c.ToolCalls))
	seen := make(map[string]struct{}, len(c.ToolCalls))
	add := func(call LLMToolCall) {
		key := call.ID + "\x00" + call.Name
		if call.ID == "" && call.Name == "" {
			key = "\x00\x00" + call.ArgsText
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, call)
	}
	for _, call := range c.ToolCalls {
		add(call)
	}
	for _, content := range c.Content {
		if content.Type != "tool_use" {
			continue
		}
		add(LLMToolCall{
			ID:       firstNonEmpty(content.ToolCallID, content.ID),
			Type:     "function",
			Name:     content.ToolName,
			Args:     content.ToolArgs,
			ArgsText: marshalCompactString(content.ToolArgs),
		})
	}
	return out
}

// ToolResultText renders a tool_result block as the plain text every protocol
// can carry. A string Result is the common case; a {"value": ...} wrapper is
// what the Anthropic parser produces for a non-object payload.
func ToolResultText(content LLMContent) string {
	if content.Text != "" {
		return content.Text
	}
	if content.ToolResult != nil {
		if value, ok := content.ToolResult["value"]; ok && len(content.ToolResult) == 1 {
			if text, ok := value.(string); ok {
				return text
			}
		}
		return marshalCompactString(content.ToolResult)
	}
	return ""
}

// ToolResultValue is the structured form of a tool result, used by the
// Responses "function_call_output.output" field and the Gemini
// functionResponse payload: keep an object/array as-is, render scalars as
// strings.
func ToolResultValue(content LLMContent) any {
	if content.ToolResult != nil {
		if value, ok := content.ToolResult["value"]; ok && len(content.ToolResult) == 1 {
			return value
		}
		return content.ToolResult
	}
	return content.Text
}

func (c LLMContent) isToolUse() bool { return c.Type == "tool_use" }

type LLMUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`

	ReasoningTokens int `json:"reasoning_tokens,omitempty"`

	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type LLMSafetyRating struct {
	Category    string `json:"category"`
	Probability string `json:"probability"`
	Blocked     bool   `json:"blocked"`
}

// ========== sync.Pool 优化（零拷贝 slice 复用） ==========

var contentSlicePool = sync.Pool{
	New: func() any { return make([]LLMContent, 0, 8) },
}

func getContentSlice() []LLMContent {
	return contentSlicePool.Get().([]LLMContent)[:0]
}

var candidateSlicePool = sync.Pool{
	New: func() any { return make([]LLMCandidate, 0, 4) },
}

func getCandidateSlice() []LLMCandidate {
	return candidateSlicePool.Get().([]LLMCandidate)[:0]
}
