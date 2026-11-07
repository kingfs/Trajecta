package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type OpenAIResponsesContentPart struct {
	Type       string                       `json:"type"`
	Text       string                       `json:"text,omitempty"`
	InputText  string                       `json:"input_text,omitempty"`
	OutputText string                       `json:"output_text,omitempty"`
	Refusal    string                       `json:"refusal,omitempty"`
	Summary    []OpenAIResponsesContentPart `json:"summary,omitempty"`
	ImageURL   string                       `json:"image_url,omitempty"`
	FileID     string                       `json:"file_id,omitempty"`
	Data       interface{}                  `json:"data,omitempty"`
}

type OpenAIResponsesInputItem struct {
	ID        string                       `json:"id,omitempty"`
	Type      string                       `json:"type"`
	Role      string                       `json:"role,omitempty"`
	Status    string                       `json:"status,omitempty"`
	Content   []OpenAIResponsesContentPart `json:"content,omitempty"`
	Arguments string                       `json:"arguments,omitempty"`
	CallID    string                       `json:"call_id,omitempty"`
	Name      string                       `json:"name,omitempty"`
	Output    interface{}                  `json:"output,omitempty"`
}

type OpenAIResponsesOutputItem = OpenAIResponsesInputItem

// OpenAIResponsesTool is the Responses API's flat function-tool shape. Chat
// Completions nests the function object under "function"; Responses does not.
type OpenAIResponsesTool struct {
	Type        string     `json:"type"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Parameters  JSONSchema `json:"parameters,omitempty"`
	Strict      *bool      `json:"strict,omitempty"`
}

// UnmarshalJSON accepts both the Responses flat tool and the legacy/aggregator
// nested {"function":{...}} spelling, so older cassettes keep their tool list.
func (t *OpenAIResponsesTool) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type        string     `json:"type"`
		Name        string     `json:"name"`
		Description string     `json:"description"`
		Parameters  JSONSchema `json:"parameters"`
		Strict      *bool      `json:"strict"`
		Function    *struct {
			Name        string     `json:"name"`
			Description string     `json:"description"`
			Parameters  JSONSchema `json:"parameters"`
			Strict      *bool      `json:"strict"`
		} `json:"function"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	t.Type = raw.Type
	t.Name = raw.Name
	t.Description = raw.Description
	t.Parameters = raw.Parameters
	t.Strict = raw.Strict
	if raw.Function != nil {
		if t.Name == "" {
			t.Name = raw.Function.Name
		}
		if t.Description == "" {
			t.Description = raw.Function.Description
		}
		if len(t.Parameters) == 0 {
			t.Parameters = raw.Function.Parameters
		}
		if t.Strict == nil {
			t.Strict = raw.Function.Strict
		}
	}
	return nil
}

type OpenAIResponsesRequest struct {
	Model              string                `json:"model"`
	Input              interface{}           `json:"input"`
	Instructions       string                `json:"instructions,omitempty"`
	PreviousResponseID string                `json:"previous_response_id,omitempty"`
	Tools              []OpenAIResponsesTool `json:"tools,omitempty"`
	ToolChoice         any                   `json:"tool_choice,omitempty"`
	Temperature        *float64              `json:"temperature,omitempty"`
	TopP               *float64              `json:"top_p,omitempty"`
	MaxOutputTokens    *int                  `json:"max_output_tokens,omitempty"`
	User               string                `json:"user,omitempty"`
}

type OpenAIResponsesUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	TotalTokens              int `json:"total_tokens"`
	ReasoningTokens          int `json:"reasoning_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type OpenAIResponsesIncompleteDetails struct {
	Reason string `json:"reason,omitempty"`
}

type OpenAIResponsesResponse struct {
	ID                string                            `json:"id"`
	Object            string                            `json:"object,omitempty"`
	Created           int64                             `json:"created_at,omitempty"`
	Model             string                            `json:"model,omitempty"`
	Status            string                            `json:"status,omitempty"`
	IncompleteDetails *OpenAIResponsesIncompleteDetails `json:"incomplete_details,omitempty"`
	Output            []OpenAIResponsesOutputItem       `json:"output"`
	OutputText        string                            `json:"output_text,omitempty"`
	Usage             *OpenAIResponsesUsage             `json:"usage,omitempty"`
	Error             interface{}                       `json:"error,omitempty"`
}

type openAIResponsesAdapter struct {
	semantics TraceSemantics
}

func (a openAIResponsesAdapter) Semantics() TraceSemantics { return a.semantics }
func (a openAIResponsesAdapter) ParseRequest(body []byte) (LLMRequest, error) {
	var req OpenAIResponsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return LLMRequest{}, err
	}
	return FromOpenAIResponsesRequest(req), nil
}
func (a openAIResponsesAdapter) ParseResponse(body []byte) (LLMResponse, error) {
	if resp, ok := parseProviderErrorResponse(body); ok {
		return resp, nil
	}
	var resp OpenAIResponsesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return LLMResponse{}, err
	}
	return OpenAIResponsesToLLM(resp), nil
}
func (a openAIResponsesAdapter) MarshalRequest(req LLMRequest) ([]byte, error) {
	return json.Marshal(req.ToOpenAIResponses())
}
func (a openAIResponsesAdapter) MarshalResponse(resp LLMResponse) ([]byte, error) {
	return json.Marshal(resp.ToOpenAIResponsesResponse())
}

func FromOpenAIResponsesRequest(req OpenAIResponsesRequest) LLMRequest {
	llmReq := LLMRequest{
		Model:       req.Model,
		ToolChoice:  parseToolChoiceValue(req.ToolChoice),
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxOutputTokens,
		UserID:      req.User,
	}
	if req.Instructions != "" {
		llmReq.System = append(llmReq.System, LLMContent{
			Type: "text",
			Text: req.Instructions,
		})
	}

	llmReq.Tools = make([]LLMTool, 0, len(req.Tools))
	for i := range req.Tools {
		t := &req.Tools[i]
		llmReq.Tools = append(llmReq.Tools, LLMTool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
			Strict:      t.Strict,
		})
	}

	appendResponsesInput(&llmReq, req.Input)

	return llmReq
}

func OpenAIResponsesToLLM(resp OpenAIResponsesResponse) LLMResponse {
	candidate := LLMCandidate{
		Index: 0,
		Role:  "assistant",
	}

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			candidate.Role = firstNonEmpty(item.Role, "assistant")
			for _, part := range item.Content {
				if part.Refusal != "" {
					candidate.Refusal = &LLMRefusal{
						Reason:  "refusal",
						Message: part.Refusal,
					}
					continue
				}
				if text := responseContentText(part); text != "" {
					candidate.Content = append(candidate.Content, LLMContent{
						Type: "text",
						Text: text,
					})
				}
			}
		case "reasoning":
			for _, part := range item.Content {
				if text := responseContentText(part); text != "" {
					candidate.Content = append(candidate.Content, LLMContent{
						Type: "thinking",
						Text: text,
					})
				}
			}
		case "function_call":
			candidate.ToolCalls = append(candidate.ToolCalls, LLMToolCall{
				ID:       firstNonEmpty(item.CallID, item.ID),
				Type:     "function",
				Name:     item.Name,
				Args:     parseJSONObject(item.Arguments),
				ArgsText: item.Arguments,
			})
		case "web_search_call", "file_search_call", "computer_call", "mcp_call", "custom_tool_call":
			argsText := firstNonEmpty(item.Arguments, marshalCompactString(item))
			candidate.ToolCalls = append(candidate.ToolCalls, LLMToolCall{
				ID:       firstNonEmpty(item.CallID, item.ID),
				Type:     firstNonEmpty(item.Type, "function"),
				Name:     firstNonEmpty(item.Name, item.Type),
				Args:     parseJSONObject(argsText),
				ArgsText: argsText,
			})
		case "function_call_output":
			candidate.Content = append(candidate.Content, LLMContent{
				Type:       "tool_result",
				ID:         item.ID,
				ToolCallID: item.CallID,
				ToolName:   item.Name,
				Text:       responseOutputText(item.Output),
				ToolResult: normalizeToolResult(item.Output),
			})
		}
	}

	result := LLMResponse{
		ID:         resp.ID,
		Model:      resp.Model,
		CreatedAt:  resp.Created,
		Candidates: []LLMCandidate{candidate},
	}
	if resp.Usage != nil {
		result.Usage = &LLMUsage{
			InputTokens:              resp.Usage.InputTokens,
			OutputTokens:             resp.Usage.OutputTokens,
			TotalTokens:              resp.Usage.TotalTokens,
			ReasoningTokens:          resp.Usage.ReasoningTokens,
			CacheCreationInputTokens: resp.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     resp.Usage.CacheReadInputTokens,
		}
	}
	return result
}

// responseOutputText renders a Responses function_call_output payload as text
// without mangling a plain string.
func responseOutputText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return marshalCompactString(typed)
	}
}

// ToOpenAIResponses renders the request as Responses input items. The shape is
// item-based rather than message-based: a tool call is a function_call item and
// its result is a function_call_output item, while text lives in a message item
// whose part type is output_text for assistant history and input_text for the
// user/system turns.
func (r *LLMRequest) ToOpenAIResponses() OpenAIResponsesRequest {
	input := make([]OpenAIResponsesInputItem, 0, len(r.Messages))

	callSequence := 0
	callIDs := make(map[string]string)
	for i := range r.Messages {
		message := &r.Messages[i]
		role := canonicalRole(message.Role)

		var textParts []OpenAIResponsesContentPart
		var calls []OpenAIResponsesInputItem
		var outputs []OpenAIResponsesInputItem

		for j := range message.Content {
			content := &message.Content[j]
			switch content.Type {
			case "text":
				if content.Text == "" {
					continue
				}
				part := OpenAIResponsesContentPart{Type: "input_text", InputText: content.Text}
				if role == "assistant" {
					part = OpenAIResponsesContentPart{Type: "output_text", OutputText: content.Text}
				}
				textParts = append(textParts, part)
			case "tool_use":
				callSequence++
				callID := firstNonEmpty(content.ToolCallID, content.ID)
				if callID == "" {
					callID = fmt.Sprintf("call_%d", callSequence)
				}
				if content.ToolName != "" {
					callIDs[content.ToolName] = callID
				}
				arguments := marshalCompactString(content.ToolArgs)
				if arguments == "" {
					arguments = "{}"
				}
				calls = append(calls, OpenAIResponsesInputItem{
					Type:      "function_call",
					CallID:    callID,
					Name:      content.ToolName,
					Arguments: arguments,
				})
			case "tool_result":
				callID := firstNonEmpty(content.ToolCallID, content.ID)
				if callID == "" {
					callID = callIDs[content.ToolName]
				}
				outputs = append(outputs, OpenAIResponsesInputItem{
					Type:   "function_call_output",
					CallID: callID,
					Name:   content.ToolName,
					Output: responsesToolOutput(*content),
				})
			}
		}

		if role == "assistant" {
			if len(textParts) > 0 {
				input = append(input, OpenAIResponsesInputItem{
					Type:    "message",
					Role:    "assistant",
					Content: textParts,
				})
			}
			input = append(input, calls...)
			continue
		}

		// A user turn can carry tool results (Anthropic) before its prose.
		input = append(input, outputs...)
		if len(textParts) > 0 {
			input = append(input, OpenAIResponsesInputItem{
				Type:    "message",
				Role:    firstNonEmpty(role, "user"),
				Content: textParts,
			})
		}
	}

	tools := make([]OpenAIResponsesTool, 0, len(r.Tools))
	for _, tool := range r.Tools {
		tools = append(tools, OpenAIResponsesTool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
			Strict:      tool.Strict,
		})
	}

	return OpenAIResponsesRequest{
		Model:           r.Model,
		Input:           input,
		Instructions:    renderSystemInstructions(r.System),
		Tools:           tools,
		ToolChoice:      encodeResponsesToolChoice(r.ToolChoice),
		Temperature:     r.Temperature,
		TopP:            r.TopP,
		MaxOutputTokens: r.MaxTokens,
		User:            r.UserID,
	}
}

// responsesToolOutput keeps an empty tool result present: the Responses API
// requires function_call_output.output, and omitempty would drop a plain "".
// The value is the text form (a structured result becomes its JSON text),
// because output is a string on the wire.
func responsesToolOutput(content LLMContent) any {
	text := ToolResultText(content)
	if text == "" {
		return json.RawMessage(`""`)
	}
	return text
}

func (r *LLMResponse) ToOpenAIResponsesResponse() OpenAIResponsesResponse {
	created := r.CreatedAt
	if created == 0 {
		created = time.Now().Unix()
	}
	resp := OpenAIResponsesResponse{
		ID:      firstNonEmpty(r.ID, "resp_converted"),
		Object:  "response",
		Created: created,
		Model:   r.Model,
		Status:  "completed",
		Output:  make([]OpenAIResponsesOutputItem, 0, 2),
	}
	if len(r.Candidates) > 0 {
		candidate := r.Candidates[0]
		parts := make([]OpenAIResponsesContentPart, 0, 2)
		if text := candidate.ResponseText(); text != "" {
			parts = append(parts, OpenAIResponsesContentPart{Type: "output_text", OutputText: text})
			resp.OutputText = text
		}
		if candidate.Refusal != nil && candidate.Refusal.Message != "" {
			parts = append(parts, OpenAIResponsesContentPart{Type: "refusal", Refusal: candidate.Refusal.Message})
		}
		if len(parts) > 0 {
			resp.Output = append(resp.Output, OpenAIResponsesOutputItem{
				ID:      "msg_converted",
				Type:    "message",
				Role:    "assistant",
				Status:  "completed",
				Content: parts,
			})
		}
		for i, call := range candidate.ResponseToolCalls() {
			callID := firstNonEmpty(call.ID, fmt.Sprintf("call_%d", i+1))
			resp.Output = append(resp.Output, OpenAIResponsesOutputItem{
				ID:        "fc_converted_" + callID,
				Type:      "function_call",
				Status:    "completed",
				CallID:    callID,
				Name:      call.Name,
				Arguments: firstNonEmpty(call.ArgsText, marshalCompactString(call.Args), "{}"),
			})
		}
	}
	finish := ""
	if len(r.Candidates) > 0 {
		finish = r.Candidates[0].FinishReason
	}
	switch finish {
	case "length":
		resp.Status = "incomplete"
		resp.IncompleteDetails = &OpenAIResponsesIncompleteDetails{Reason: "max_output_tokens"}
	case "content_filter":
		resp.Status = "incomplete"
		resp.IncompleteDetails = &OpenAIResponsesIncompleteDetails{Reason: "content_filter"}
	}
	if r.Usage != nil {
		resp.Usage = &OpenAIResponsesUsage{
			InputTokens:              r.Usage.InputTokens,
			OutputTokens:             r.Usage.OutputTokens,
			TotalTokens:              r.Usage.TotalTokens,
			ReasoningTokens:          r.Usage.ReasoningTokens,
			CacheCreationInputTokens: r.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     r.Usage.CacheReadInputTokens,
		}
	}
	return resp
}

func appendResponsesInput(req *LLMRequest, input interface{}) {
	switch value := input.(type) {
	case nil:
		return
	case string:
		req.Messages = append(req.Messages, LLMMessage{
			Role: "user",
			Content: []LLMContent{
				{Type: "text", Text: value},
			},
		})
	case []interface{}:
		for _, item := range value {
			appendResponsesInput(req, item)
		}
	default:
		itemBytes, err := json.Marshal(value)
		if err != nil {
			return
		}
		var item OpenAIResponsesInputItem
		if err := json.Unmarshal(itemBytes, &item); err != nil {
			return
		}
		if msg, ok := responsesItemToMessage(item); ok {
			if msg.Role == "system" || msg.Role == "developer" {
				for _, content := range msg.Content {
					if content.Type == "text" && content.Text != "" {
						req.System = append(req.System, content)
					}
				}
				return
			}
			req.Messages = append(req.Messages, msg)
		}
	}
}

func responsesItemToMessage(item OpenAIResponsesInputItem) (LLMMessage, bool) {
	switch item.Type {
	case "message":
		msg := LLMMessage{Role: firstNonEmpty(item.Role, "user")}
		for _, part := range item.Content {
			if text := responseContentText(part); text != "" {
				msg.Content = append(msg.Content, LLMContent{
					Type: "text",
					Text: text,
				})
			}
			if part.Refusal != "" {
				msg.Content = append(msg.Content, LLMContent{
					Type:    "text",
					Text:    part.Refusal,
					Refusal: part.Refusal,
				})
			}
		}
		return msg, len(msg.Content) > 0
	case "function_call":
		return LLMMessage{
			Role: "assistant",
			Content: []LLMContent{{
				ID:         item.ID,
				Type:       "tool_use",
				ToolCallID: item.CallID,
				ToolName:   item.Name,
				ToolArgs:   parseJSONObject(item.Arguments),
			}},
		}, true
	case "function_call_output":
		return LLMMessage{
			Role: "tool",
			Content: []LLMContent{{
				ID:         item.ID,
				Type:       "tool_result",
				ToolCallID: item.CallID,
				ToolName:   item.Name,
				Text:       responseOutputText(item.Output),
				ToolResult: normalizeToolResult(item.Output),
			}},
		}, true
	default:
		return LLMMessage{}, false
	}
}

func responseContentText(part OpenAIResponsesContentPart) string {
	for _, text := range []string{part.OutputText, part.Text, part.InputText, part.Refusal} {
		if text != "" {
			return text
		}
	}
	if len(part.Summary) > 0 {
		segments := make([]string, 0, len(part.Summary))
		for _, child := range part.Summary {
			if text := responseContentText(child); text != "" {
				segments = append(segments, text)
			}
		}
		return strings.Join(segments, "\n")
	}
	if part.Data != nil {
		return marshalCompactString(part.Data)
	}
	return ""
}

func renderSystemInstructions(system []LLMContent) string {
	if len(system) == 0 {
		return ""
	}
	parts := make([]string, 0, len(system))
	for _, content := range system {
		if content.Type == "text" && content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func parseJSONObject(input string) map[string]any {
	if input == "" {
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(input), &result); err != nil {
		return nil
	}
	return result
}

func normalizeToolResult(v interface{}) map[string]any {
	if v == nil {
		return nil
	}
	if result, ok := v.(map[string]any); ok {
		return result
	}
	return map[string]any{"value": v}
}

func marshalCompactString(v interface{}) string {
	if v == nil {
		return ""
	}
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}
