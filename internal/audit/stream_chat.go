package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
	xirstream "github.com/kingfs/Trajecta/pkg/xir/stream"
)

// decodeChatStream assembles a Chat Completions SSE body into an XIR response.
// It is intentionally limited to the Chat shape: the audit projection needs a
// completed response, and cross-protocol stream state machines are future work.
//
// The assembler is not reused here because Chat may interleave tool-call
// indexes. The decoder handles the SSE framing; this file merges deltas by
// index and builds the parts in index order.
func decodeChatStream(ctx context.Context, body []byte) (*xir.Response, error) {
	dec := xirstream.NewDecoder(bytes.NewReader(body))
	defer dec.Close()
	var (
		response xir.Response
		text     strings.Builder
		reason   strings.Builder
		tools    = make(map[int]*xir.ToolCall)
		order    []int
		usageAcc usage.Usage
		finish   string
	)
	for {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		frame, err := dec.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		data := strings.TrimSpace(string(frame.Data))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil, fmt.Errorf("audit: decode chat stream chunk: %w", err)
		}
		if response.ID == "" && chunk.ID != "" {
			response.ID = chunk.ID
		}
		if response.Model == "" && chunk.Model != "" {
			response.Model = chunk.Model
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				text.WriteString(choice.Delta.Content)
			}
			if choice.Delta.ReasoningContent != "" {
				reason.WriteString(choice.Delta.ReasoningContent)
			}
			if choice.Delta.Reasoning != "" {
				reason.WriteString(choice.Delta.Reasoning)
			}
			for _, call := range choice.Delta.ToolCalls {
				index := call.Index
				if index < 0 {
					index = len(order)
				}
				current, ok := tools[index]
				if !ok {
					current = &xir.ToolCall{Type: "function"}
					tools[index] = current
					order = append(order, index)
				}
				if call.ID != "" {
					current.ID = call.ID
				}
				if call.Function.Name != "" {
					current.Name = call.Function.Name
				}
				current.ArgsText += call.Function.Arguments
			}
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
		}
		if chunk.Usage != nil {
			usageAcc = usage.Merge(usageAcc, chunk.Usage.usage())
		}
	}
	if reason.Len() > 0 {
		response.Parts = append(response.Parts, xir.Part{
			Kind:      xir.PartReasoning,
			Reasoning: &xir.Reasoning{Text: reason.String()},
		})
	}
	if text.Len() > 0 {
		response.Parts = append(response.Parts, xir.Part{Kind: xir.PartText, Text: text.String()})
	}
	sort.Ints(order)
	for _, index := range order {
		call := tools[index]
		if call == nil {
			continue
		}
		if json.Valid([]byte(call.ArgsText)) {
			call.Args = json.RawMessage(call.ArgsText)
		}
		response.Parts = append(response.Parts, xir.Part{Kind: xir.PartToolCall, ToolCall: call})
	}
	if !usageAcc.Empty() {
		response.Usage = &usageAcc
	}
	response.StopReason = chatFinishReason(finish, len(order) > 0)
	response.Status = "completed"
	return &response, nil
}

type chatStreamChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

type chatUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u chatUsage) usage() usage.Usage {
	if u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return usage.Usage{
		InputTokens:     u.PromptTokens,
		OutputTokens:    u.CompletionTokens,
		TotalTokens:     u.TotalTokens,
		ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens,
		CacheReadTokens: u.PromptTokensDetails.CachedTokens,
		Source:          "chat",
	}
}

func chatFinishReason(raw string, hasTools bool) xir.FinishReason {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "stop":
		if hasTools {
			return xir.FinishToolUse
		}
		return xir.FinishStop
	case "length":
		return xir.FinishLength
	case "tool_calls", "function_call":
		return xir.FinishToolUse
	case "content_filter":
		return xir.FinishContentFilter
	case "refusal":
		return xir.FinishRefusal
	case "error":
		return xir.FinishError
	default:
		if hasTools {
			return xir.FinishToolUse
		}
		return xir.FinishUnknown
	}
}
