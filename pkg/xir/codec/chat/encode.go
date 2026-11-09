package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
)

// EncodeRequest renders a normalized request as Chat Completions JSON.
//
// Only fields the IR actually carries are emitted: an unset pointer, empty
// string or empty slice is left out, and no default value is manufactured for
// a parameter the caller did not supply (tool-call arguments being the one
// protocol requirement: a call with neither ArgsText nor Args gets "{}").
// Values in Params.Extra beyond the modeled "reasoning" key are not emitted.
func (c *Codec) EncodeRequest(ctx context.Context, req *xir.Request, opts xir.EncodeOptions) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, fmt.Errorf("chat: nil request")
	}
	model := req.Model
	if opts.Model != "" {
		model = opts.Model
	}
	messages := encodeMessages(req)

	out := requestWire{
		Model:               model,
		Messages:            messages,
		ToolChoice:          encodeToolChoice(req.ToolChoice),
		Temperature:         req.Params.Temperature,
		TopP:                req.Params.TopP,
		TopK:                req.Params.TopK,
		MaxTokens:           req.Params.MaxTokens,
		MaxCompletionTokens: req.Params.MaxCompletion,
		Stop:                encodeStop(req.Params.Stop),
		Seed:                req.Params.Seed,
		PresencePenalty:     req.Params.PresencePenalty,
		FrequencyPenalty:    req.Params.FrequencyPenalty,
		Logprobs:            req.Params.Logprobs,
		TopLogprobs:         req.Params.TopLogprobs,
		ParallelToolCalls:   req.Params.ParallelTools,
		ResponseFormat:      req.Params.ResponseFormat,
		ReasoningEffort:     req.Params.ReasoningEffort,
		Reasoning:           req.Params.Extra["reasoning"],
		User:                req.Params.User,
		Metadata:            req.Params.Metadata,
		PromptCacheKey:      req.State.PromptCacheKey,
		Store:               req.State.Store,
		Stream:              req.Stream || opts.Stream,
	}
	if out.Messages == nil {
		out.Messages = []messageWire{}
	}
	if len(req.Tools) > 0 {
		out.Tools = encodeTools(req.Tools)
	}
	return json.Marshal(out)
}

// EncodeResponse renders a normalized response as Chat Completions JSON. A
// Response carrying Error is emitted as an error envelope instead of a
// completion.
func (c *Codec) EncodeResponse(ctx context.Context, resp *xir.Response, opts xir.EncodeOptions) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("chat: nil response")
	}
	if resp.Error != nil {
		return encodeError(resp.Error)
	}
	model := resp.Model
	if opts.Model != "" {
		model = opts.Model
	}

	content, calls, refusal := encodeMessageContent(resp.Parts, true)
	message := responseMessageWire{
		Role:      "assistant",
		Content:   content,
		ToolCalls: calls,
		Refusal:   refusal,
	}
	out := responseWire{
		ID:     resp.ID,
		Object: "chat.completion",
		Model:  model,
		Choices: []choiceWire{{
			Index:        0,
			Message:      message,
			FinishReason: encodeFinishReason(resp.StopReason),
		}},
		Usage: encodeUsage(resp.Usage),
	}
	return json.Marshal(out)
}

type requestWire struct {
	Model               string          `json:"model,omitempty"`
	Messages            []messageWire   `json:"messages"`
	Tools               []toolWire      `json:"tools,omitempty"`
	ToolChoice          any             `json:"tool_choice,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	TopK                *int            `json:"top_k,omitempty"`
	MaxTokens           *int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
	Stop                any             `json:"stop,omitempty"`
	Seed                *int64          `json:"seed,omitempty"`
	PresencePenalty     *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64        `json:"frequency_penalty,omitempty"`
	Logprobs            *bool           `json:"logprobs,omitempty"`
	TopLogprobs         *int            `json:"top_logprobs,omitempty"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`
	ResponseFormat      json.RawMessage `json:"response_format,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	Reasoning           json.RawMessage `json:"reasoning,omitempty"`
	User                string          `json:"user,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
	PromptCacheKey      string          `json:"prompt_cache_key,omitempty"`
	Store               *bool           `json:"store,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
}

type messageWire struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	Name       string         `json:"name,omitempty"`
	ToolCalls  []toolCallWire `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Refusal    string         `json:"refusal,omitempty"`
}

type responseWire struct {
	ID      string       `json:"id,omitempty"`
	Object  string       `json:"object,omitempty"`
	Model   string       `json:"model,omitempty"`
	Choices []choiceWire `json:"choices"`
	Usage   *usageWire   `json:"usage,omitempty"`
}

type choiceWire struct {
	Index        int                 `json:"index"`
	Message      responseMessageWire `json:"message"`
	FinishReason *string             `json:"finish_reason"`
}

type responseMessageWire struct {
	Role      string         `json:"role"`
	Content   any            `json:"content"`
	ToolCalls []toolCallWire `json:"tool_calls,omitempty"`
	Refusal   string         `json:"refusal,omitempty"`
}

type usageWire struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	PromptTokensDetails     *promptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *completionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type promptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type completionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type toolCallWire struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function functionCallWire `json:"function"`
}

type functionCallWire struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolWire struct {
	Type     string            `json:"type"`
	Function *functionDeclWire `json:"function,omitempty"`
}

type functionDeclWire struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type toolChoiceWire struct {
	Type     string                  `json:"type"`
	Function *toolChoiceFunctionWire `json:"function,omitempty"`
}

type toolChoiceFunctionWire struct {
	Name string `json:"name"`
}

type partWire struct {
	Type       string          `json:"type"`
	Text       *string         `json:"text,omitempty"`
	ImageURL   *imageURLWire   `json:"image_url,omitempty"`
	InputAudio *inputAudioWire `json:"input_audio,omitempty"`
	File       *fileWire       `json:"file,omitempty"`
}

type imageURLWire struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type inputAudioWire struct {
	Data   string `json:"data"`
	Format string `json:"format,omitempty"`
}

type fileWire struct {
	FileData string `json:"file_data"`
	Filename string `json:"filename,omitempty"`
}

type errorBodyWire struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Param   string `json:"param,omitempty"`
	Code    string `json:"code,omitempty"`
}

type errorEnvelopeWire struct {
	Error errorBodyWire `json:"error"`
}

type rawErrorEnvelopeWire struct {
	Error json.RawMessage `json:"error"`
}

func encodeMessages(req *xir.Request) []messageWire {
	out := make([]messageWire, 0, len(req.Messages)+1)
	if len(req.System) > 0 {
		content, _, refusal := encodeMessageContent(req.System, false)
		if content == nil {
			content = ""
		}
		out = append(out, messageWire{Role: "system", Content: content, Refusal: refusal})
	}
	for i := range req.Messages {
		out = append(out, encodeMessage(&req.Messages[i]))
	}
	return out
}

func encodeMessage(m *xir.Message) messageWire {
	out := messageWire{
		Role: wireRole(m.Role),
		Name: m.Name,
	}
	if m.Role == xir.RoleTool || (m.Role == xir.RoleUnknown && firstToolResult(m.Parts) != nil) {
		tr := firstToolResult(m.Parts)
		if tr != nil {
			out.ToolCallID = tr.CallID
			if out.Name == "" {
				out.Name = tr.Name
			}
			out.Content = encodeToolResultContent(tr)
			return out
		}
	}
	content, calls, refusal := encodeMessageContent(m.Parts, m.Role == xir.RoleAssistant)
	out.Content = content
	out.ToolCalls = calls
	out.Refusal = refusal
	return out
}

func firstToolResult(parts []xir.Part) *xir.ToolResult {
	for i := range parts {
		if parts[i].ToolResult != nil {
			return parts[i].ToolResult
		}
	}
	return nil
}

// encodeMessageContent walks parts in wire order so a mixed text/media array
// keeps its ordering. A single text part renders as a bare string; assistant
// content with several text parts keeps them as text parts, and user/system
// content becomes an array only when it carries more than one part or a
// non-text part.
func encodeMessageContent(parts []xir.Part, assistant bool) (any, []toolCallWire, string) {
	var contentParts []xir.Part
	var calls []toolCallWire
	var refusal string
	textCount := 0
	for i := range parts {
		p := parts[i]
		switch p.Kind {
		case xir.PartToolCall:
			if p.ToolCall != nil {
				calls = append(calls, encodeToolCall(p.ToolCall))
			}
		case xir.PartRefusal:
			if p.Text != "" {
				refusal = p.Text
			}
		case xir.PartText:
			textCount++
			contentParts = append(contentParts, p)
		case xir.PartImage, xir.PartAudio, xir.PartFile:
			contentParts = append(contentParts, p)
		}
	}

	if assistant {
		switch textCount {
		case 0:
			return nil, calls, refusal
		case 1:
			return joinTextParts(contentParts), calls, refusal
		default:
			// Recent chat schemas accept text parts for assistant content;
			// keeping them preserves part boundaries on a round trip.
			encoded := make([]partWire, 0, textCount)
			for _, p := range contentParts {
				if p.Kind == xir.PartText {
					encoded = append(encoded, textPart(p.Text))
				}
			}
			return encoded, calls, refusal
		}
	}

	switch {
	case len(contentParts) == 0:
		return "", calls, refusal
	case len(contentParts) == 1 && contentParts[0].Kind == xir.PartText:
		return contentParts[0].Text, calls, refusal
	}

	encoded := make([]partWire, 0, len(contentParts))
	for _, p := range contentParts {
		if p.Kind == xir.PartText {
			encoded = append(encoded, textPart(p.Text))
			continue
		}
		if media, ok := encodeMediaPart(p); ok {
			encoded = append(encoded, media)
		}
	}
	if len(encoded) == 0 {
		return "", calls, refusal
	}
	if len(encoded) == 1 && encoded[0].Type == "text" && encoded[0].Text != nil {
		return *encoded[0].Text, calls, refusal
	}
	return encoded, calls, refusal
}

// encodeContentPartsArray renders parts as a chat content-part array in wire
// order. It returns nil when nothing representable remains.
func encodeContentPartsArray(parts []xir.Part) []partWire {
	out := make([]partWire, 0, len(parts))
	for _, p := range parts {
		if p.Kind == xir.PartText {
			out = append(out, textPart(p.Text))
			continue
		}
		if media, ok := encodeMediaPart(p); ok {
			out = append(out, media)
		}
	}
	return out
}

func joinTextParts(parts []xir.Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Kind == xir.PartText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func textPart(text string) partWire {
	value := text
	return partWire{Type: "text", Text: &value}
}

func encodeMediaPart(p xir.Part) (partWire, bool) {
	if p.Media == nil {
		return partWire{}, false
	}
	switch p.Kind {
	case xir.PartImage:
		url := p.Media.URL
		if url == "" {
			// The caller may hand us an already-formed data URI.
			url = p.Media.Data
		}
		if url == "" {
			return partWire{}, false
		}
		return partWire{Type: "image_url", ImageURL: &imageURLWire{URL: url, Detail: p.Media.Detail}}, true
	case xir.PartAudio:
		if p.Media.Data == "" {
			return partWire{}, false
		}
		return partWire{Type: "input_audio", InputAudio: &inputAudioWire{Data: p.Media.Data, Format: p.Media.MediaType}}, true
	case xir.PartFile:
		if p.Media.Data == "" {
			// file_id is not modeled, so a reference-only file part has no
			// faithful Chat representation.
			return partWire{}, false
		}
		return partWire{Type: "file", File: &fileWire{FileData: p.Media.Data, Filename: p.Media.Filename}}, true
	default:
		return partWire{}, false
	}
}

func encodeToolResultContent(tr *xir.ToolResult) any {
	if len(tr.Content) > 0 {
		// Keep the array shape so a decoded content-parts result round-trips
		// instead of collapsing into a bare string.
		if parts := encodeContentPartsArray(tr.Content); len(parts) > 0 {
			return parts
		}
	}
	if tr.Text != "" {
		return tr.Text
	}
	if len(tr.JSON) > 0 {
		return string(tr.JSON)
	}
	return ""
}

func encodeToolCall(tc *xir.ToolCall) toolCallWire {
	args := tc.ArgsText
	if args == "" && len(tc.Args) > 0 {
		args = string(tc.Args)
	}
	if args == "" {
		args = "{}"
	}
	typ := tc.Type
	if typ == "" {
		typ = "function"
	}
	return toolCallWire{
		ID:       tc.ID,
		Type:     typ,
		Function: functionCallWire{Name: tc.Name, Arguments: args},
	}
}

func encodeTools(tools []xir.Tool) []toolWire {
	out := make([]toolWire, 0, len(tools))
	for _, tool := range tools {
		kind := tool.Kind
		if kind == "" {
			kind = "function"
		}
		if kind != "function" {
			// Built-in tools (web_search, ...) are declared by type alone; the
			// modeled name/schema fields have no place in that shape.
			out = append(out, toolWire{Type: kind})
			continue
		}
		out = append(out, toolWire{
			Type: "function",
			Function: &functionDeclWire{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Schema,
				Strict:      tool.Strict,
			},
		})
	}
	return out
}

func encodeToolChoice(tc xir.ToolChoice) any {
	switch tc.Mode {
	case "":
		return nil
	case "tool":
		if tc.Name == "" {
			return nil
		}
		return toolChoiceWire{Type: "function", Function: &toolChoiceFunctionWire{Name: tc.Name}}
	default:
		return tc.Mode
	}
}

func encodeStop(stop []string) any {
	switch len(stop) {
	case 0:
		return nil
	case 1:
		return stop[0]
	default:
		return stop
	}
}

func encodeFinishReason(reason xir.FinishReason) *string {
	var value string
	switch reason {
	case xir.FinishStop:
		value = "stop"
	case xir.FinishLength:
		value = "length"
	case xir.FinishToolUse:
		value = "tool_calls"
	case xir.FinishContentFilter:
		value = "content_filter"
	case xir.FinishRefusal:
		// Chat reports a refusal as a normal stop with a refusal message.
		value = "stop"
	default:
		return nil
	}
	return &value
}

func encodeUsage(u *usage.Usage) *usageWire {
	if u == nil || u.Empty() {
		return nil
	}
	normalized := u.Normalize()
	out := &usageWire{
		PromptTokens:     normalized.InputTokens,
		CompletionTokens: normalized.OutputTokens,
		TotalTokens:      normalized.TotalTokens,
	}
	if normalized.CacheReadTokens > 0 {
		out.PromptTokensDetails = &promptTokensDetails{CachedTokens: normalized.CacheReadTokens}
	}
	if normalized.ReasoningTokens > 0 {
		out.CompletionTokensDetails = &completionTokensDetails{ReasoningTokens: normalized.ReasoningTokens}
	}
	return out
}

func encodeError(e *xir.Error) ([]byte, error) {
	if e == nil {
		return nil, fmt.Errorf("chat: nil error")
	}
	if e.Message == "" && len(e.Raw) > 0 && json.Valid(e.Raw) {
		return json.Marshal(rawErrorEnvelopeWire{Error: e.Raw})
	}
	return json.Marshal(errorEnvelopeWire{Error: errorBodyWire{
		Message: e.Message,
		Type:    e.Type,
		Param:   e.Param,
		Code:    e.Code,
	}})
}

func wireRole(r xir.Role) string {
	switch r {
	case xir.RoleSystem, xir.RoleDeveloper, xir.RoleUser, xir.RoleAssistant, xir.RoleTool:
		return string(r)
	case xir.RoleModel:
		return "assistant"
	default:
		return "user"
	}
}
