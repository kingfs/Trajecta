package llm

// ========== Anthropic Messages 映射 ==========

import (
	"bytes"
	"encoding/json"
)

type AnthropicContentBlock struct {
	Type      string      `json:"type"` // "text", "tool_use", ...
	Text      string      `json:"text,omitempty"`
	Thinking  string      `json:"thinking,omitempty"`
	ID        string      `json:"id,omitempty"`
	Name      string      `json:"name,omitempty"`
	Input     interface{} `json:"input,omitempty"`
	ToolUseID string      `json:"tool_use_id,omitempty"`
	Content   interface{} `json:"content,omitempty"`
	IsError   bool        `json:"is_error,omitempty"`
}

type AnthropicMessage struct {
	Role    string                  `json:"role"`
	Content AnthropicMessageContent `json:"content"`
}

// AnthropicMessageContent accepts the two wire forms of a message body: a plain
// string (the shorthand Anthropic allows for a single text turn) and an array
// of content blocks. It always encodes as the block array.
type AnthropicMessageContent []AnthropicContentBlock

func (c *AnthropicMessageContent) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*c = nil
		return nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*c = AnthropicMessageContent{{Type: "text", Text: text}}
		return nil
	}
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(data, &blocks); err != nil {
		return err
	}
	*c = blocks
	return nil
}

type AnthropicTool struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	InputSchema JSONSchema `json:"input_schema,omitempty"`
}

type AnthropicRequest struct {
	Model string `json:"model"`

	System   any                `json:"system,omitempty"` // string 或 blocks
	Messages []AnthropicMessage `json:"messages"`

	Tools      []AnthropicTool `json:"tools,omitempty"`
	ToolChoice any             `json:"tool_choice,omitempty"`

	MaxTokens   *int     `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	TopK        *int     `json:"top_k,omitempty"`

	StopSequences []string `json:"stop_sequences,omitempty"`

	Metadata map[string]string `json:"metadata,omitempty"`
}

type AnthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type AnthropicResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Model      string                  `json:"model"`
	Content    []AnthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      *AnthropicUsage         `json:"usage,omitempty"`
}

// ---- LLMRequest -> AnthropicRequest ----

// ToAnthropic renders the request for Messages. A tool_result block belongs to
// a "user" message and must precede the prose of that message, so tool results
// are collected separately and emitted first. Chat's separate role "tool"
// messages become exactly that shape.
func (r *LLMRequest) ToAnthropic() AnthropicRequest {
	msgs := make([]AnthropicMessage, 0, len(r.Messages))

	for i := range r.Messages {
		m := &r.Messages[i]
		var toolResults []AnthropicContentBlock
		var others []AnthropicContentBlock

		for j := range m.Content {
			c := &m.Content[j]
			switch c.Type {
			case "text":
				if c.Text != "" {
					others = append(others, AnthropicContentBlock{Type: "text", Text: c.Text})
				}
			case "tool_use":
				id := firstNonEmpty(c.ToolCallID, c.ID)
				others = append(others, AnthropicContentBlock{
					Type:  "tool_use",
					ID:    id,
					Name:  c.ToolName,
					Input: nonNilMap(c.ToolArgs),
				})
			case "tool_result":
				toolResults = append(toolResults, AnthropicContentBlock{
					Type:      "tool_result",
					ToolUseID: firstNonEmpty(c.ToolCallID, c.ID),
					// Messages accepts a string or an array of content blocks,
					// not an arbitrary JSON object; a structured result is
					// rendered as its JSON text.
					Content: ToolResultText(*c),
					IsError: c.Refusal == "error",
				})
			}
		}

		blocks := append(toolResults, others...)
		if len(blocks) == 0 {
			continue
		}
		role := anthropicRole(m.Role)
		if len(toolResults) > 0 {
			// Messages only accepts tool_result blocks in a user turn.
			role = "user"
		}
		msgs = append(msgs, AnthropicMessage{
			Role:    role,
			Content: AnthropicMessageContent(blocks),
		})
	}

	var system any
	systemBlocks := make([]AnthropicContentBlock, 0, len(r.System))
	for i := range r.System {
		c := &r.System[i]
		if c.Type == "text" && c.Text != "" {
			systemBlocks = append(systemBlocks, AnthropicContentBlock{Type: "text", Text: c.Text})
		}
	}
	switch len(systemBlocks) {
	case 0:
	case 1:
		system = systemBlocks[0].Text
	default:
		system = systemBlocks
	}

	tools := make([]AnthropicTool, 0, len(r.Tools))
	for i := range r.Tools {
		t := &r.Tools[i]
		tools = append(tools, AnthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
	}

	var metadata map[string]string
	if r.UserID != "" {
		metadata = map[string]string{"user_id": r.UserID}
	}

	return AnthropicRequest{
		Model:         r.Model,
		System:        system,
		Messages:      msgs,
		Tools:         tools,
		ToolChoice:    encodeAnthropicToolChoice(r.ToolChoice),
		MaxTokens:     r.MaxTokens,
		Temperature:   r.Temperature,
		TopP:          r.TopP,
		TopK:          r.TopK,
		StopSequences: r.StopSeq,
		Metadata:      metadata,
	}
}

// anthropicRole maps the IR roles onto the two roles Messages accepts.
func anthropicRole(role string) string {
	if canonicalRole(role) == "assistant" {
		return "assistant"
	}
	return "user"
}

func nonNilMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

// ---- AnthropicRequest -> LLMRequest ----

func FromAnthropicRequest(req AnthropicRequest) LLMRequest {
	llmReq := LLMRequest{
		Model:          req.Model,
		ToolChoice:     parseToolChoiceValue(req.ToolChoice),
		Temperature:    req.Temperature,
		TopP:           req.TopP,
		TopK:           req.TopK,
		MaxTokens:      req.MaxTokens,
		StopSeq:        req.StopSequences,
		SafetySettings: nil, // Anthropic doesn't expose safety in request
	}

	// ---- Metadata ----
	if req.Metadata != nil {
		if uid, ok := req.Metadata["user_id"]; ok {
			llmReq.UserID = uid
		}
	}

	// ---- System ----
	for _, block := range anthropicSystemBlocks(req.System) {
		if block.Type == "text" && block.Text != "" {
			llmReq.System = append(llmReq.System, LLMContent{Type: "text", Text: block.Text})
		}
	}

	// ---- Messages ----
	llmReq.Messages = make([]LLMMessage, 0, len(req.Messages))
	for i := range req.Messages {
		m := &req.Messages[i]

		contents := make([]LLMContent, 0, len(m.Content))
		for j := range m.Content {
			appendAnthropicContentBlock(&contents, m.Content[j])
		}

		llmReq.Messages = append(llmReq.Messages, LLMMessage{
			Role:    m.Role,
			Content: contents,
		})
	}

	// ---- Tools ----
	llmReq.Tools = make([]LLMTool, 0, len(req.Tools))
	for i := range req.Tools {
		t := &req.Tools[i]
		llmReq.Tools = append(llmReq.Tools, LLMTool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
	}

	return llmReq
}

// anthropicSystemBlocks accepts both wire forms of the system field: a plain
// string and an array of content blocks (the form Claude clients use when they
// attach cache_control). Decoding through "any" yields []any, so a type switch
// on []AnthropicContentBlock alone silently dropped every block-array prompt.
func anthropicSystemBlocks(value any) []AnthropicContentBlock {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		if typed == "" {
			return nil
		}
		return []AnthropicContentBlock{{Type: "text", Text: typed}}
	case []AnthropicContentBlock:
		return typed
	case []map[string]any:
		out := make([]AnthropicContentBlock, 0, len(typed))
		for _, item := range typed {
			if block, ok := anthropicBlockFromMap(item); ok {
				out = append(out, block)
			}
		}
		return out
	case []any:
		out := make([]AnthropicContentBlock, 0, len(typed))
		for _, item := range typed {
			switch block := item.(type) {
			case AnthropicContentBlock:
				out = append(out, block)
			case map[string]any:
				if parsed, ok := anthropicBlockFromMap(block); ok {
					out = append(out, parsed)
				}
			}
		}
		return out
	case map[string]any:
		if block, ok := anthropicBlockFromMap(typed); ok {
			return []AnthropicContentBlock{block}
		}
	}
	return nil
}

func anthropicBlockFromMap(value map[string]any) (AnthropicContentBlock, bool) {
	kind, _ := value["type"].(string)
	switch kind {
	case "text":
		text, _ := value["text"].(string)
		return AnthropicContentBlock{Type: "text", Text: text}, true
	case "tool_use":
		name, _ := value["name"].(string)
		id, _ := value["id"].(string)
		input, _ := value["input"].(map[string]any)
		return AnthropicContentBlock{Type: "tool_use", ID: id, Name: name, Input: input}, true
	case "tool_result":
		id, _ := value["tool_use_id"].(string)
		isError, _ := value["is_error"].(bool)
		return AnthropicContentBlock{Type: "tool_result", ToolUseID: id, Content: value["content"], IsError: isError}, true
	case "thinking":
		text, _ := value["thinking"].(string)
		if text == "" {
			text, _ = value["text"].(string)
		}
		return AnthropicContentBlock{Type: "thinking", Thinking: text}, true
	}
	return AnthropicContentBlock{}, false
}

// ---- AnthropicResponse -> LLMResponse ----

func AnthropicToLLM(resp AnthropicResponse) LLMResponse {
	content := getContentSlice()
	for i := range resp.Content {
		b := &resp.Content[i]
		appendAnthropicContentBlock(&content, *b)
	}

	var usage *LLMUsage
	if resp.Usage != nil {
		u := LLMUsage{
			InputTokens:              resp.Usage.InputTokens,
			OutputTokens:             resp.Usage.OutputTokens,
			TotalTokens:              resp.Usage.InputTokens + resp.Usage.OutputTokens,
			CacheCreationInputTokens: resp.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     resp.Usage.CacheReadInputTokens,
		}
		usage = &u
	}

	cand := LLMCandidate{
		Index:        0,
		Role:         resp.Role,
		Content:      content,
		FinishReason: resp.StopReason,
	}
	if cand.Role == "" {
		cand.Role = "assistant"
	}

	cands := getCandidateSlice()
	cands = append(cands, cand)

	return LLMResponse{
		ID:         resp.ID,
		Model:      resp.Model,
		Candidates: cands,
		Usage:      usage,
	}
}

// ---- LLMResponse -> AnthropicResponse ----

func (r *LLMResponse) ToAnthropicResponse() AnthropicResponse {
	// Claude only supports a single candidate.
	var candidate LLMCandidate
	if len(r.Candidates) > 0 {
		candidate = r.Candidates[0]
	}

	blocks := make([]AnthropicContentBlock, 0, len(candidate.Content)+len(candidate.ToolCalls))
	if text := candidate.ResponseText(); text != "" {
		blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: text})
	} else if candidate.Refusal != nil && candidate.Refusal.Message != "" {
		// Messages has a refusal stop reason; keep the refusal text as the
		// answer instead of returning an empty content array.
		blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: candidate.Refusal.Message})
	}
	for _, call := range candidate.ResponseToolCalls() {
		blocks = append(blocks, AnthropicContentBlock{
			Type:  "tool_use",
			ID:    firstNonEmpty(call.ID, "toolu_converted"),
			Name:  call.Name,
			Input: nonNilMap(call.Args),
		})
	}

	var usage *AnthropicUsage
	if r.Usage != nil {
		usage = &AnthropicUsage{
			InputTokens:              r.Usage.InputTokens,
			OutputTokens:             r.Usage.OutputTokens,
			CacheCreationInputTokens: r.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     r.Usage.CacheReadInputTokens,
		}
	}

	return AnthropicResponse{
		ID:         firstNonEmpty(r.ID, "msg_converted"),
		Type:       "message",
		Role:       "assistant",
		Model:      r.Model,
		Content:    blocks,
		StopReason: candidate.FinishReason,
		Usage:      usage,
	}
}

func appendAnthropicContentBlock(target *[]LLMContent, block AnthropicContentBlock) {
	switch block.Type {
	case "text":
		*target = append(*target, LLMContent{
			Type: "text",
			Text: block.Text,
		})
	case "thinking":
		*target = append(*target, LLMContent{
			Type: "thinking",
			Text: firstNonEmpty(block.Thinking, block.Text),
		})
	case "tool_use":
		*target = append(*target, LLMContent{
			ID:         block.ID,
			Type:       "tool_use",
			ToolCallID: block.ID,
			ToolName:   block.Name,
			ToolArgs:   normalizeToolResult(block.Input),
		})
	case "tool_result":
		text, normalized := anthropicToolResult(block.Content)
		*target = append(*target, LLMContent{
			Type:       "tool_result",
			ToolCallID: block.ToolUseID,
			Text:       text,
			ToolResult: normalized,
			Refusal:    boolMarker(block.IsError, "error"),
		})
	}
}

// anthropicToolResult renders a tool_result content field, which may be a bare
// string, an array of text blocks, or an arbitrary JSON value.
func anthropicToolResult(value any) (string, map[string]any) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		// Keep the raw value available to the observation/monitor paths
		// (ToolResult) while also exposing the text the encoders read.
		return typed, normalizeToolResult(typed)
	case []any:
		var text []string
		for _, item := range typed {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if kind, _ := block["type"].(string); kind == "text" {
				if segment, ok := block["text"].(string); ok {
					text = append(text, segment)
				}
			}
		}
		if len(text) == len(typed) {
			joined := ""
			for _, segment := range text {
				joined += segment
			}
			return joined, normalizeToolResult(joined)
		}
		return "", normalizeToolResult(typed)
	default:
		return "", normalizeToolResult(typed)
	}
}

func boolMarker(v bool, text string) string {
	if v {
		return text
	}
	return ""
}
