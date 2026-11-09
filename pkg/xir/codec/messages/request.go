package messages

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// requestKnownKeys are the request fields this codec models. Everything else
// is kept in Extensions[ExtensionKey] so a Messages re-encode can restore it.
var requestKnownKeys = map[string]struct{}{
	"model":          {},
	"system":         {},
	"messages":       {},
	"tools":          {},
	"tool_choice":    {},
	"max_tokens":     {},
	"temperature":    {},
	"top_p":          {},
	"top_k":          {},
	"stop_sequences": {},
	"metadata":       {},
	"stream":         {},
	"thinking":       {},
}

// DecodeRequest projects a Messages request document onto the semantic IR.
//
// The model, system, messages, tools, tool_choice and generation parameters
// are decoded. Unknown top-level fields are preserved in Extensions under
// ExtensionKey; content block types the IR does not model are preserved as
// Sealed values. Structural mismatches (a non-object root, a non-array
// messages/tools value, a content value that is neither string nor array) are
// errors.
func (Codec) DecodeRequest(ctx context.Context, doc *xir.Document, opts xir.DecodeOptions) (*xir.Request, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	_ = opts
	if doc == nil || doc.Root == nil {
		return nil, fmt.Errorf("messages: nil request document")
	}
	root := doc.Root
	if root.Kind != wire.Object {
		return nil, fmt.Errorf("messages: request root is %s, want object", kindName(root.Kind))
	}

	req := &xir.Request{Ref: root}
	var err error
	if req.Model, _, err = stringMember(root, "model", "request"); err != nil {
		return nil, err
	}
	if req.System, err = decodeSystem(root); err != nil {
		return nil, err
	}
	if req.Messages, err = decodeMessages(root); err != nil {
		return nil, err
	}
	if req.Tools, err = decodeTools(root); err != nil {
		return nil, err
	}
	choice, parallel, err := decodeToolChoice(root)
	if err != nil {
		return nil, err
	}
	req.ToolChoice = choice
	req.Params.ParallelTools = parallel
	if err := decodeParams(root, &req.Params); err != nil {
		return nil, err
	}
	if req.Stream, _, err = boolMember(root, "stream", "request"); err != nil {
		return nil, err
	}
	req.Extensions = map[string]json.RawMessage{}
	if extras := extraMembers(root, requestKnownKeys); extras != nil {
		req.Extensions[ExtensionKey] = marshalExtras(extras)
	}
	if len(req.Extensions) == 0 {
		req.Extensions = nil
	}
	return req, nil
}

func decodeSystem(root *wire.Node) ([]xir.Part, error) {
	system, ok := root.Get("system")
	if !ok || system.Kind == wire.Null {
		return nil, nil
	}
	switch system.Kind {
	case wire.String:
		return []xir.Part{{Kind: xir.PartText, Text: system.Str, Ref: system}}, nil
	case wire.Array:
		parts := make([]xir.Part, 0, len(system.Items))
		for i, item := range system.Items {
			if item == nil || item.Kind != wire.Object {
				return nil, fmt.Errorf("messages: system[%d] must be a JSON object", i)
			}
			path := fmt.Sprintf("system[%d]", i)
			blockType, present, err := stringMember(item, "type", path)
			if err != nil {
				return nil, err
			}
			if !present {
				blockType = "text"
			}
			switch blockType {
			case "text", "":
				text, _, err := stringMember(item, "text", path)
				if err != nil {
					return nil, err
				}
				parts = append(parts, xir.Part{Kind: xir.PartText, Text: text, Ref: item})
			default:
				parts = append(parts, xir.Part{
					Kind: xir.PartUnknown,
					Sealed: &xir.Sealed{
						Protocol: xir.ProtocolMessages,
						Kind:     blockType,
						Raw:      cloneRaw(item.Raw()),
					},
					Ref: item,
				})
			}
		}
		return parts, nil
	default:
		return nil, fmt.Errorf("messages: system is %s, want a string or an array of content blocks", kindName(system.Kind))
	}
}

func decodeMessages(root *wire.Node) ([]xir.Message, error) {
	messages, ok := root.Get("messages")
	if !ok || messages.Kind == wire.Null {
		return nil, nil
	}
	if messages.Kind != wire.Array {
		return nil, fmt.Errorf("messages: messages is %s, want array", kindName(messages.Kind))
	}
	out := make([]xir.Message, 0, len(messages.Items))
	for i, item := range messages.Items {
		if item == nil || item.Kind != wire.Object {
			return nil, fmt.Errorf("messages: messages[%d] must be a JSON object", i)
		}
		path := fmt.Sprintf("messages[%d]", i)
		role, _, err := stringMember(item, "role", path)
		if err != nil {
			return nil, err
		}
		parts, err := decodeContentMember(item, "content", path)
		if err != nil {
			return nil, err
		}
		name, _, err := stringMember(item, "name", path)
		if err != nil {
			return nil, err
		}
		out = append(out, xir.Message{
			Role:  decodeRole(role),
			Name:  name,
			Parts: parts,
			Ref:   item,
		})
	}
	return out, nil
}

func decodeTools(root *wire.Node) ([]xir.Tool, error) {
	tools, ok := root.Get("tools")
	if !ok || tools.Kind == wire.Null {
		return nil, nil
	}
	if tools.Kind != wire.Array {
		return nil, fmt.Errorf("messages: tools is %s, want array", kindName(tools.Kind))
	}
	out := make([]xir.Tool, 0, len(tools.Items))
	for i, item := range tools.Items {
		if item == nil || item.Kind != wire.Object {
			return nil, fmt.Errorf("messages: tools[%d] must be a JSON object", i)
		}
		path := fmt.Sprintf("tools[%d]", i)
		name, _, err := stringMember(item, "name", path)
		if err != nil {
			return nil, err
		}
		description, _, err := stringMember(item, "description", path)
		if err != nil {
			return nil, err
		}
		declaredType, hasType, err := stringMember(item, "type", path)
		if err != nil {
			return nil, err
		}
		kind := "function"
		if hasType {
			kind = declaredType
		}
		tool := xir.Tool{
			Name:        name,
			Description: description,
			Schema:      nil,
			Kind:        kind,
			Ref:         item,
		}
		if schema, ok := rawMember(item, "input_schema"); ok {
			tool.Schema = schema
		}
		if strict, present, err := boolMember(item, "strict", path); err != nil {
			return nil, err
		} else if present {
			tool.Strict = &strict
		}
		out = append(out, tool)
	}
	return out, nil
}

// decodeToolChoice returns the normalized choice plus the parallel-tool flag.
// Messages expresses parallelism as disable_parallel_tool_use inside
// tool_choice, so it maps to Params.ParallelTools with the sense inverted.
func decodeToolChoice(root *wire.Node) (xir.ToolChoice, *bool, error) {
	choice, ok := root.Get("tool_choice")
	if !ok || choice.Kind == wire.Null {
		return xir.ToolChoice{}, nil, nil
	}
	switch choice.Kind {
	case wire.String:
		return normalizeToolChoice(choice.Str, ""), nil, nil
	case wire.Object:
		mode, _, err := stringMember(choice, "type", "tool_choice")
		if err != nil {
			return xir.ToolChoice{}, nil, err
		}
		name, _, err := stringMember(choice, "name", "tool_choice")
		if err != nil {
			return xir.ToolChoice{}, nil, err
		}
		var parallel *bool
		if disable, present, err := boolMember(choice, "disable_parallel_tool_use", "tool_choice"); err != nil {
			return xir.ToolChoice{}, nil, err
		} else if present {
			enabled := !disable
			parallel = &enabled
		}
		return normalizeToolChoice(mode, name), parallel, nil
	default:
		return xir.ToolChoice{}, nil, fmt.Errorf("messages: tool_choice is %s, want object or string", kindName(choice.Kind))
	}
}

func normalizeToolChoice(mode, name string) xir.ToolChoice {
	switch mode {
	case "auto", "none":
		return xir.ToolChoice{Mode: mode}
	case "any", "required":
		return xir.ToolChoice{Mode: "required"}
	case "tool":
		return xir.ToolChoice{Mode: "tool", Name: name}
	default:
		// Keep an unrecognized Messages selector verbatim so a same-protocol
		// re-encode does not silently change the request.
		return xir.ToolChoice{Mode: mode, Name: name}
	}
}

func decodeParams(root *wire.Node, params *xir.Params) error {
	if maxTokens, present, err := intMember(root, "max_tokens", "request"); err != nil {
		return err
	} else if present {
		params.MaxTokens = &maxTokens
	}
	if temperature, present, err := floatMember(root, "temperature", "request"); err != nil {
		return err
	} else if present {
		params.Temperature = &temperature
	}
	if topP, present, err := floatMember(root, "top_p", "request"); err != nil {
		return err
	} else if present {
		params.TopP = &topP
	}
	if topK, present, err := intMember(root, "top_k", "request"); err != nil {
		return err
	} else if present {
		params.TopK = &topK
	}
	if stop, ok := root.Get("stop_sequences"); ok && stop.Kind != wire.Null {
		if stop.Kind != wire.Array {
			return fmt.Errorf("messages: request.stop_sequences is %s, want array", kindName(stop.Kind))
		}
		params.Stop = make([]string, 0, len(stop.Items))
		for i, item := range stop.Items {
			value, ok := item.String()
			if !ok {
				return fmt.Errorf("messages: request.stop_sequences[%d] is %s, want string", i, kindName(item.Kind))
			}
			params.Stop = append(params.Stop, value)
		}
	}
	if err := decodeMetadata(root, params); err != nil {
		return err
	}
	return decodeThinking(root, params)
}

func decodeMetadata(root *wire.Node, params *xir.Params) error {
	metadata, ok := root.Get("metadata")
	if !ok || metadata.Kind == wire.Null {
		return nil
	}
	if metadata.Kind != wire.Object {
		return fmt.Errorf("messages: request.metadata is %s, want object", kindName(metadata.Kind))
	}
	params.Metadata = cloneRaw(metadata.Raw())
	userID, _, err := stringMember(metadata, "user_id", "request.metadata")
	if err != nil {
		return err
	}
	params.User = userID
	return nil
}

// decodeThinking keeps the raw thinking parameter in Params.Extra (Messages
// has adaptive variants the IR does not model) and lifts the enabled/disabled
// intent and the token budget.
func decodeThinking(root *wire.Node, params *xir.Params) error {
	thinking, ok := root.Get("thinking")
	if !ok || thinking.Kind == wire.Null {
		return nil
	}
	if thinking.Kind != wire.Object {
		return fmt.Errorf("messages: request.thinking is %s, want object", kindName(thinking.Kind))
	}
	if params.Extra == nil {
		params.Extra = map[string]json.RawMessage{}
	}
	params.Extra["thinking"] = cloneRaw(thinking.Raw())
	kind, _, err := stringMember(thinking, "type", "request.thinking")
	if err != nil {
		return err
	}
	switch kind {
	case "enabled":
		enabled := true
		params.Thinking = &enabled
	case "disabled":
		enabled := false
		params.Thinking = &enabled
	}
	if budget, present, err := intMember(thinking, "budget_tokens", "request.thinking"); err != nil {
		return err
	} else if present {
		params.ThinkingBudget = &budget
	}
	return nil
}

// EncodeRequest renders a normalized request as Messages JSON.
//
// Only values the IR carries are emitted: an unset pointer, empty string or
// empty slice is left out, and no default is manufactured for a parameter the
// caller did not supply. The two protocol-required exceptions are documented
// where they happen (a tool_use input object, and a non-empty messages array).
func (Codec) EncodeRequest(ctx context.Context, req *xir.Request, opts xir.EncodeOptions) ([]byte, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, fmt.Errorf("messages: nil request")
	}
	model := req.Model
	if opts.Model != "" {
		model = opts.Model
	}

	messages, err := encodeMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	system, err := encodeSystem(req.System)
	if err != nil {
		return nil, err
	}
	tools, err := encodeTools(req.Tools)
	if err != nil {
		return nil, err
	}
	toolChoice := encodeToolChoice(req.ToolChoice)
	// Messages has no top-level parallel_tool_calls flag: the only carrier is
	// tool_choice.disable_parallel_tool_use, so the flag is emitted only when
	// a tool_choice object exists.
	if toolChoice != nil && req.Params.ParallelTools != nil {
		disable := !*req.Params.ParallelTools
		toolChoice.DisableParallelToolUse = &disable
	}
	metadata, err := encodeMetadata(req.Params)
	if err != nil {
		return nil, err
	}
	thinking, err := encodeThinking(req.Params)
	if err != nil {
		return nil, err
	}
	maxTokens := req.Params.MaxTokens
	if maxTokens == nil {
		maxTokens = req.Params.MaxCompletion
	}

	out := requestWire{
		Model:         model,
		MaxTokens:     maxTokens,
		System:        system,
		Messages:      messages,
		Tools:         tools,
		ToolChoice:    toolChoice,
		Temperature:   req.Params.Temperature,
		TopP:          req.Params.TopP,
		TopK:          req.Params.TopK,
		StopSequences: req.Params.Stop,
		Metadata:      metadata,
		Stream:        req.Stream || opts.Stream,
		Thinking:      thinking,
	}
	return marshalWithExtensions(out, extensionRaw(req.Extensions), requestKnownKeys)
}

type requestWire struct {
	Model         string          `json:"model,omitempty"`
	MaxTokens     *int            `json:"max_tokens,omitempty"`
	System        json.RawMessage `json:"system,omitempty"`
	Messages      []messageWire   `json:"messages"`
	Tools         []toolWire      `json:"tools,omitempty"`
	ToolChoice    *toolChoiceWire `json:"tool_choice,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	TopK          *int            `json:"top_k,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Thinking      json.RawMessage `json:"thinking,omitempty"`
}

type messageWire struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type toolWire struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type toolChoiceWire struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"`
}

type thinkingWire struct {
	Type         string `json:"type"`
	BudgetTokens *int   `json:"budget_tokens,omitempty"`
}

func encodeMessages(messages []xir.Message) ([]messageWire, error) {
	out := make([]messageWire, 0, len(messages))
	for _, msg := range messages {
		content, err := encodeMessageContent(msg)
		if err != nil {
			return nil, err
		}
		out = append(out, messageWire{Role: encodeRole(msg.Role), Content: content})
	}
	return out, nil
}

// encodeSystem renders the system prompt. A single text part uses the string
// shorthand; anything structured (several parts, a non-text part, a sealed
// block) is a block array.
func encodeSystem(parts []xir.Part) (json.RawMessage, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	if len(parts) == 1 && parts[0].Kind == xir.PartText && parts[0].Sealed == nil {
		return json.Marshal(parts[0].Text)
	}
	blocks := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		if part.Sealed != nil && part.Sealed.Protocol == xir.ProtocolMessages &&
			len(part.Sealed.Raw) > 0 && json.Valid(part.Sealed.Raw) {
			blocks = append(blocks, cloneRaw(part.Sealed.Raw))
			continue
		}
		if part.Kind != xir.PartText && part.Text == "" {
			continue
		}
		block, err := marshalBlock(textBlockWire{Type: "text", Text: part.Text})
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	if len(blocks) == 0 {
		return nil, nil
	}
	return json.Marshal(blocks)
}

func encodeTools(tools []xir.Tool) ([]toolWire, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]toolWire, 0, len(tools))
	for _, tool := range tools {
		wireTool := toolWire{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.Schema,
			Strict:      tool.Strict,
		}
		switch tool.Kind {
		case "", "function":
			// Plain client tool: Messages has no type discriminator for it.
		default:
			wireTool.Type = tool.Kind
		}
		out = append(out, wireTool)
	}
	return out, nil
}

func encodeToolChoice(choice xir.ToolChoice) *toolChoiceWire {
	if choice.Mode == "" {
		return nil
	}
	out := &toolChoiceWire{}
	switch choice.Mode {
	case "auto":
		out.Type = "auto"
	case "none":
		out.Type = "none"
	case "required":
		out.Type = "any"
	case "tool":
		out.Type = "tool"
		out.Name = choice.Name
	default:
		out.Type = choice.Mode
		out.Name = choice.Name
	}
	return out
}

func encodeMetadata(params xir.Params) (json.RawMessage, error) {
	if len(params.Metadata) > 0 {
		if !json.Valid(params.Metadata) {
			return nil, fmt.Errorf("messages: params.metadata is not valid JSON")
		}
		return cloneRaw(params.Metadata), nil
	}
	if params.User == "" {
		return nil, nil
	}
	return json.Marshal(map[string]string{"user_id": params.User})
}

func encodeThinking(params xir.Params) (json.RawMessage, error) {
	if raw, ok := params.Extra["thinking"]; ok && len(raw) > 0 {
		if !json.Valid(raw) {
			return nil, fmt.Errorf("messages: params.extra thinking is not valid JSON")
		}
		if strings.TrimSpace(string(raw)) != "null" {
			return cloneRaw(raw), nil
		}
	}
	if params.ThinkingBudget != nil {
		return json.Marshal(thinkingWire{Type: "enabled", BudgetTokens: params.ThinkingBudget})
	}
	if params.Thinking != nil {
		if *params.Thinking {
			return json.Marshal(thinkingWire{Type: "enabled"})
		}
		return json.Marshal(thinkingWire{Type: "disabled"})
	}
	return nil, nil
}
