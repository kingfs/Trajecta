package llm

// ========== OpenAI Chat Completions 映射 ==========

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type OpenAIChatMessage struct {
	Role       string           `json:"role"`
	Content    interface{}      `json:"content"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
	Refusal    string           `json:"refusal,omitempty"`
}

type OpenAIToolCall struct {
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type,omitempty"`
	Function OpenAIToolFunctionCall `json:"function"`
}

type OpenAIToolFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

type OpenAIToolFunction struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Parameters  JSONSchema `json:"parameters,omitempty"`
	Strict      *bool      `json:"strict,omitempty"`
}

type OpenAITool struct {
	Type     string             `json:"type"` // "function"
	Function OpenAIToolFunction `json:"function"`
}

type OpenAIChatRequest struct {
	Model    string              `json:"model"`
	Messages []OpenAIChatMessage `json:"messages"`

	Tools      []OpenAITool `json:"tools,omitempty"`
	ToolChoice any          `json:"tool_choice,omitempty"`

	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
	Stop        []string `json:"stop,omitempty"`

	User string `json:"user,omitempty"`
}

type OpenAIChatChoice struct {
	Index        int               `json:"index"`
	Message      OpenAIChatMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type OpenAIChatResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []OpenAIChatChoice `json:"choices"`
	Usage   *OpenAIUsage       `json:"usage,omitempty"`
}

// ---- LLMRequest -> OpenAIChatRequest ----

// ToOpenAI renders the request for Chat Completions. Tool calls become the
// assistant message's tool_calls, and each tool_result block becomes its own
// role "tool" message: Chat Completions has one tool result per message, while
// Messages carries them as blocks in a user message.
func (r *LLMRequest) ToOpenAI() OpenAIChatRequest {
	msgs := make([]OpenAIChatMessage, 0, len(r.System)+len(r.Messages))

	for i := range r.System {
		c := &r.System[i]
		if c.Type == "text" && c.Text != "" {
			msgs = append(msgs, OpenAIChatMessage{Role: "system", Content: c.Text})
		}
	}

	callSequence := 0
	callIDs := make(map[string]string)
	for i := range r.Messages {
		m := &r.Messages[i]
		role := canonicalRole(m.Role)

		var textParts []string
		var toolCalls []OpenAIToolCall
		var toolResults []LLMContent
		for j := range m.Content {
			c := &m.Content[j]
			switch c.Type {
			case "text":
				if c.Text != "" {
					textParts = append(textParts, c.Text)
				}
			case "tool_use":
				callSequence++
				id := firstNonEmpty(c.ToolCallID, c.ID)
				if id == "" {
					id = fmt.Sprintf("call_%d", callSequence)
				}
				args := c.ToolArgs
				if args == nil {
					args = map[string]any{}
				}
				argsText, err := json.Marshal(args)
				if err != nil {
					argsText = []byte("{}")
				}
				if c.ToolName != "" {
					callIDs[c.ToolName] = id
				}
				toolCalls = append(toolCalls, OpenAIToolCall{
					ID:   id,
					Type: "function",
					Function: OpenAIToolFunctionCall{
						Name:      c.ToolName,
						Arguments: string(argsText),
					},
				})
			case "tool_result":
				toolResults = append(toolResults, *c)
			}
		}

		if len(toolResults) > 0 {
			// Anthropic expresses tool results as blocks in a user message.
			// Chat Completions needs one role "tool" message per result.
			for k := range toolResults {
				result := &toolResults[k]
				id := firstNonEmpty(result.ToolCallID, result.ID)
				if id == "" {
					id = callIDs[result.ToolName]
				}
				if id == "" {
					callSequence++
					id = fmt.Sprintf("call_%d", callSequence)
				}
				// Chat's tool message schema is role/content/tool_call_id;
				// the name is not part of it and strict providers reject
				// unknown properties.
				msgs = append(msgs, OpenAIChatMessage{
					Role:       "tool",
					ToolCallID: id,
					Content:    ToolResultText(*result),
				})
			}
			if len(textParts) > 0 {
				msgs = append(msgs, OpenAIChatMessage{Role: contentRoleForToolResult(role), Content: chatContent(textParts)})
			}
			continue
		}

		if len(textParts) == 0 && len(toolCalls) == 0 {
			continue
		}
		msg := OpenAIChatMessage{Role: role, ToolCalls: toolCalls}
		if len(textParts) > 0 {
			msg.Content = chatContent(textParts)
		}
		msgs = append(msgs, msg)
	}

	tools := make([]OpenAITool, 0, len(r.Tools))
	for i := range r.Tools {
		t := &r.Tools[i]
		tools = append(tools, OpenAITool{
			Type: "function",
			Function: OpenAIToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
				Strict:      t.Strict,
			},
		})
	}

	return OpenAIChatRequest{
		Model:       r.Model,
		Messages:    msgs,
		Tools:       tools,
		ToolChoice:  encodeChatToolChoice(r.ToolChoice),
		Temperature: r.Temperature,
		TopP:        r.TopP,
		MaxTokens:   r.MaxTokens,
		Stop:        r.StopSeq,
		User:        r.UserID,
	}
}

// chatContent keeps a single text part as the string form OpenAI clients expect
// and falls back to the array form only for a genuinely multi-part message.
func chatContent(parts []string) any {
	if len(parts) == 1 {
		return parts[0]
	}
	out := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		out = append(out, map[string]any{"type": "text", "text": part})
	}
	return out
}

// contentRoleForToolResult picks the role of the prose that shared a user
// message with tool_result blocks. Chat has no such message; the text belongs
// to the user turn the results are reported in.
func contentRoleForToolResult(role string) string {
	if role == "assistant" {
		return "assistant"
	}
	return "user"
}

// ---- OpenAIChatRequest -> LLMRequest ----

func FromOpenAIRequest(req OpenAIChatRequest) LLMRequest {
	llmReq := LLMRequest{
		Model:       req.Model,
		ToolChoice:  parseToolChoiceValue(req.ToolChoice),
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
		StopSeq:     req.Stop,
		UserID:      req.User,
	}

	llmReq.System = make([]LLMContent, 0, 1)
	llmReq.Messages = make([]LLMMessage, 0, len(req.Messages))

	toolNames := make(map[string]string)
	for i := range req.Messages {
		m := &req.Messages[i]
		for _, toolCall := range m.ToolCalls {
			if toolCall.ID != "" && toolCall.Function.Name != "" {
				toolNames[toolCall.ID] = toolCall.Function.Name
			}
		}

		// system message
		if m.Role == "system" || m.Role == "developer" {
			appendOpenAIContentToSystem(&llmReq, m.Content)
			continue
		}

		// A Chat tool result is a message of its own. The IR carries it as a
		// tool_result block whose text is the output, which is what every
		// target encoder reads.
		if m.Role == "tool" || m.Role == "function" {
			text := openAIContentText(m.Content)
			name := firstNonEmpty(m.Name, toolNames[m.ToolCallID])
			llmReq.Messages = append(llmReq.Messages, LLMMessage{
				Role: "tool",
				Content: []LLMContent{{
					Type:       "tool_result",
					ID:         m.ToolCallID,
					ToolCallID: m.ToolCallID,
					ToolName:   name,
					Text:       text,
				}},
			})
			continue
		}

		msg := LLMMessage{
			Role:    m.Role,
			Content: parseOpenAIMessageContent(m.Content),
		}
		if m.Refusal != "" {
			// A refusal is an assistant answer of its own. Keep it as text
			// carrying the refusal marker so cross-protocol encoders do not
			// drop it; Chat can re-emit it as the message's refusal field.
			msg.Content = append(msg.Content, LLMContent{Type: "text", Text: m.Refusal, Refusal: m.Refusal})
		}
		for _, toolCall := range m.ToolCalls {
			msg.Content = append(msg.Content, LLMContent{
				ID:         toolCall.ID,
				Type:       "tool_use",
				ToolCallID: toolCall.ID,
				ToolName:   toolCall.Function.Name,
				ToolArgs:   parseJSONObject(toolCall.Function.Arguments),
			})
		}
		llmReq.Messages = append(llmReq.Messages, msg)
	}

	llmReq.Tools = make([]LLMTool, 0, len(req.Tools))
	for i := range req.Tools {
		t := &req.Tools[i]
		llmReq.Tools = append(llmReq.Tools, LLMTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
			Strict:      t.Function.Strict,
		})
	}

	return llmReq
}

// openAIContentText flattens a tool message's content to the text the IR
// carries. Some providers send the tool output as a single string, others as a
// content-part array.
func openAIContentText(raw interface{}) string {
	switch value := raw.(type) {
	case nil:
		return ""
	case string:
		return value
	case []interface{}:
		var builder strings.Builder
		for _, item := range value {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := part["text"].(string); ok {
				builder.WriteString(text)
			}
		}
		return builder.String()
	default:
		return marshalCompactString(value)
	}
}

// ---- OpenAIChatResponse -> LLMResponse ----

func OpenAIToLLM(resp OpenAIChatResponse) LLMResponse {
	cands := getCandidateSlice()

	for i := range resp.Choices {
		ch := &resp.Choices[i]

		content := getContentSlice()
		content = append(content, parseOpenAIMessageContent(ch.Message.Content)...)
		toolCalls := make([]LLMToolCall, 0, len(ch.Message.ToolCalls))
		for _, toolCall := range ch.Message.ToolCalls {
			toolCalls = append(toolCalls, LLMToolCall{
				ID:       toolCall.ID,
				Type:     firstNonEmpty(toolCall.Type, "function"),
				Name:     toolCall.Function.Name,
				Args:     parseJSONObject(toolCall.Function.Arguments),
				ArgsText: toolCall.Function.Arguments,
			})
		}

		candidate := LLMCandidate{
			Index:        ch.Index,
			Role:         ch.Message.Role,
			Content:      content,
			FinishReason: ch.FinishReason,
			ToolCalls:    toolCalls,
		}
		if ch.Message.Refusal != "" {
			candidate.Refusal = &LLMRefusal{Reason: "refusal", Message: ch.Message.Refusal}
		}
		cands = append(cands, candidate)
	}

	var usage *LLMUsage
	if resp.Usage != nil {
		u := LLMUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		}
		usage = &u
	}

	return LLMResponse{
		ID:         resp.ID,
		Model:      resp.Model,
		CreatedAt:  resp.Created,
		Candidates: cands,
		Usage:      usage,
	}
}

// ---- LLMResponse -> OpenAIChatResponse ----

func (r *LLMResponse) ToOpenAIResponse() OpenAIChatResponse {
	choices := make([]OpenAIChatChoice, 0, len(r.Candidates))

	for i := range r.Candidates {
		c := &r.Candidates[i]
		text := c.ResponseText()
		toolCalls := c.ResponseToolCalls()

		choice := OpenAIChatChoice{
			Index: c.Index,
			Message: OpenAIChatMessage{
				Role: "assistant",
			},
			FinishReason: c.FinishReason,
		}
		if text != "" {
			choice.Message.Content = text
		}
		if c.Refusal != nil && c.Refusal.Message != "" {
			choice.Message.Refusal = c.Refusal.Message
		}
		if len(toolCalls) > 0 {
			choice.Message.ToolCalls = make([]OpenAIToolCall, 0, len(toolCalls))
			for _, call := range toolCalls {
				choice.Message.ToolCalls = append(choice.Message.ToolCalls, OpenAIToolCall{
					ID:   firstNonEmpty(call.ID, fmt.Sprintf("call_%d", len(choice.Message.ToolCalls)+1)),
					Type: firstNonEmpty(call.Type, "function"),
					Function: OpenAIToolFunctionCall{
						Name:      call.Name,
						Arguments: firstNonEmpty(call.ArgsText, marshalCompactString(call.Args), "{}"),
					},
				})
			}
		}
		if choice.FinishReason == "" {
			if len(toolCalls) > 0 {
				choice.FinishReason = "tool_calls"
			} else {
				choice.FinishReason = "stop"
			}
		}
		choices = append(choices, choice)
	}

	var usage *OpenAIUsage
	if r.Usage != nil {
		usage = &OpenAIUsage{
			PromptTokens:     r.Usage.InputTokens,
			CompletionTokens: r.Usage.OutputTokens,
			TotalTokens:      r.Usage.TotalTokens,
		}
	}

	created := r.CreatedAt
	if created == 0 {
		created = time.Now().Unix()
	}
	return OpenAIChatResponse{
		ID:      firstNonEmpty(r.ID, "chatcmpl-converted"),
		Object:  "chat.completion",
		Created: created,
		Model:   r.Model,
		Choices: choices,
		Usage:   usage,
	}
}

func appendOpenAIContentToSystem(req *LLMRequest, raw interface{}) {
	for _, content := range parseOpenAIMessageContent(raw) {
		if content.Type == "text" {
			req.System = append(req.System, content)
		}
	}
}

func parseOpenAIMessageContent(raw interface{}) []LLMContent {
	switch value := raw.(type) {
	case nil:
		return nil
	case string:
		return []LLMContent{{Type: "text", Text: value}}
	case []interface{}:
		result := make([]LLMContent, 0, len(value))
		for _, item := range value {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := itemMap["type"].(string)
			switch partType {
			case "text", "input_text", "output_text":
				text := firstNonEmpty(stringValue(itemMap["text"]), stringValue(itemMap["input_text"]), stringValue(itemMap["output_text"]))
				if text != "" {
					result = append(result, LLMContent{Type: "text", Text: text})
				}
			default:
				result = append(result, LLMContent{
					Type: partType,
					Text: marshalCompactString(itemMap),
				})
			}
		}
		return result
	default:
		return []LLMContent{{Type: "text", Text: marshalCompactString(value)}}
	}
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}
