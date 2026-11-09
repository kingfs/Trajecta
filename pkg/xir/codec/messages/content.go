package messages

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// decodeContentMember decodes an object member that holds Messages content
// (a bare string, a block array, or null).
func decodeContentMember(n *wire.Node, key, path string) ([]xir.Part, error) {
	v, ok := n.Get(key)
	if !ok || v.Kind == wire.Null {
		return nil, nil
	}
	return decodeContent(v, path+"."+key)
}

// decodeContent decodes the two Messages content shapes. The shorthand string
// is exactly one text block, which is how Messages itself defines it.
func decodeContent(v *wire.Node, path string) ([]xir.Part, error) {
	switch v.Kind {
	case wire.Null:
		return nil, nil
	case wire.String:
		return []xir.Part{{Kind: xir.PartText, Text: v.Str, Ref: v}}, nil
	case wire.Array:
		parts := make([]xir.Part, 0, len(v.Items))
		for i, item := range v.Items {
			if item == nil {
				continue
			}
			part, err := decodeBlock(item, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
		}
		return parts, nil
	default:
		return nil, fmt.Errorf("messages: %s is %s, want a string or an array of content blocks", path, kindName(v.Kind))
	}
}

// decodeBlock decodes one Messages content block. A block type the IR does not
// model becomes PartUnknown carrying the block's original bytes as a Sealed
// value, so a same-protocol re-encode is lossless.
func decodeBlock(n *wire.Node, path string) (xir.Part, error) {
	if n == nil || n.Kind != wire.Object {
		return xir.Part{}, fmt.Errorf("messages: %s must be a JSON object", path)
	}
	blockType, present, err := stringMember(n, "type", path)
	if err != nil {
		return xir.Part{}, err
	}
	if !present || blockType == "" {
		return xir.Part{}, fmt.Errorf("messages: %s.type is required", path)
	}
	switch blockType {
	case "text":
		text, _, err := stringMember(n, "text", path)
		if err != nil {
			return xir.Part{}, err
		}
		return xir.Part{Kind: xir.PartText, Text: text, Ref: n}, nil

	case "image", "document":
		media, err := decodeMedia(n, path, blockType)
		if err != nil {
			return xir.Part{}, err
		}
		kind := xir.PartImage
		if blockType == "document" {
			kind = xir.PartFile
		}
		return xir.Part{Kind: kind, Media: media, Ref: n}, nil

	case "tool_use":
		call, err := decodeToolUse(n, path)
		if err != nil {
			return xir.Part{}, err
		}
		return xir.Part{Kind: xir.PartToolCall, ToolCall: call, Ref: n}, nil

	case "tool_result":
		result, err := decodeToolResult(n, path)
		if err != nil {
			return xir.Part{}, err
		}
		return xir.Part{Kind: xir.PartToolResult, ToolResult: result, Ref: n}, nil

	case "thinking":
		text, _, err := stringMember(n, "thinking", path)
		if err != nil {
			return xir.Part{}, err
		}
		signature, _, err := stringMember(n, "signature", path)
		if err != nil {
			return xir.Part{}, err
		}
		return xir.Part{
			Kind:      xir.PartReasoning,
			Reasoning: &xir.Reasoning{Text: text, Signature: signature},
			Ref:       n,
		}, nil

	case "redacted_thinking":
		data, _, err := stringMember(n, "data", path)
		if err != nil {
			return xir.Part{}, err
		}
		// The block is opaque and is restored byte for byte. It is reachable
		// through both Part.Sealed (the part-level opaque slot) and
		// Reasoning.Sealed (the reasoning-specific slot); they share one
		// value so callers do not have to know which one the IR consumer
		// looks at.
		sealed := &xir.Sealed{
			Protocol: xir.ProtocolMessages,
			Kind:     "redacted_thinking",
			Raw:      cloneRaw(n.Raw()),
		}
		return xir.Part{
			Kind: xir.PartReasoning,
			Reasoning: &xir.Reasoning{
				EncryptedContent: data,
				Sealed:           sealed,
			},
			Sealed: sealed,
			Ref:    n,
		}, nil

	default:
		part := xir.Part{
			Kind: xir.PartUnknown,
			Sealed: &xir.Sealed{
				Protocol: xir.ProtocolMessages,
				Kind:     blockType,
				Raw:      cloneRaw(n.Raw()),
			},
			Ref: n,
		}
		// Unknown blocks occasionally still carry text. Keeping it makes the
		// part useful for audit/search without affecting the sealed bytes.
		if text, _, err := stringMember(n, "text", path); err == nil {
			part.Text = text
		}
		return part, nil
	}
}

// decodeToolUse maps a tool_use block onto ToolCall. Messages has no separate
// call type: every tool_use is a function-style call.
func decodeToolUse(n *wire.Node, path string) (*xir.ToolCall, error) {
	id, _, err := stringMember(n, "id", path)
	if err != nil {
		return nil, err
	}
	name, _, err := stringMember(n, "name", path)
	if err != nil {
		return nil, err
	}
	call := &xir.ToolCall{ID: id, Name: name, Type: "function", Ref: n}
	if input, ok := n.Get("input"); ok && input.Kind != wire.Null {
		if input.Kind == wire.String {
			// Defensive: Messages requires an object, but a string input is
			// still recoverable as argument text.
			call.ArgsText = input.Str
		} else {
			call.Args = cloneRaw(input.Raw())
		}
	}
	return call, nil
}

// decodeToolResult maps a tool_result block onto ToolResult. Content keeps the
// shape it arrived in: a string result stays in Text, a block array stays in
// Content, so encoding can reproduce either form.
func decodeToolResult(n *wire.Node, path string) (*xir.ToolResult, error) {
	callID, _, err := stringMember(n, "tool_use_id", path)
	if err != nil {
		return nil, err
	}
	isError, _, err := boolMember(n, "is_error", path)
	if err != nil {
		return nil, err
	}
	result := &xir.ToolResult{CallID: callID, IsError: isError, Ref: n}
	if content, ok := n.Get("content"); ok && content.Kind != wire.Null {
		switch content.Kind {
		case wire.String:
			result.Text = content.Str
		case wire.Array:
			parts, err := decodeContent(content, path+".content")
			if err != nil {
				return nil, err
			}
			result.Content = parts
		default:
			return nil, fmt.Errorf("messages: %s.content is %s, want a string or an array", path, kindName(content.Kind))
		}
	}
	return result, nil
}

// decodeMedia decodes the source object shared by image and document blocks.
// The source's own discriminator is not modeled: URL means a URL source and
// Data means inline bytes.
func decodeMedia(n *wire.Node, path, blockType string) (*xir.Media, error) {
	media := &xir.Media{}
	if blockType == "image" {
		media.Kind = xir.MediaImage
	} else {
		media.Kind = xir.MediaFile
	}
	if title, _, err := stringMember(n, "title", path); err != nil {
		return nil, err
	} else {
		media.Filename = title
	}
	source, ok := n.Get("source")
	if !ok || source.Kind == wire.Null {
		return media, nil
	}
	if source.Kind != wire.Object {
		return nil, fmt.Errorf("messages: %s.source is %s, want object", path, kindName(source.Kind))
	}
	mediaType, _, err := stringMember(source, "media_type", path+".source")
	if err != nil {
		return nil, err
	}
	data, _, err := stringMember(source, "data", path+".source")
	if err != nil {
		return nil, err
	}
	url, _, err := stringMember(source, "url", path+".source")
	if err != nil {
		return nil, err
	}
	media.MediaType = mediaType
	media.Data = data
	media.URL = url
	return media, nil
}

type mediaSourceWire struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type textBlockWire struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type imageBlockWire struct {
	Type   string          `json:"type"`
	Source mediaSourceWire `json:"source"`
}

type documentBlockWire struct {
	Type   string          `json:"type"`
	Source mediaSourceWire `json:"source"`
	Title  string          `json:"title,omitempty"`
}

type toolUseBlockWire struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type toolResultBlockWire struct {
	Type      string          `json:"type"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type thinkingBlockWire struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature,omitempty"`
}

type redactedThinkingBlockWire struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

// encodeMessageContent renders one message's content. A single text part uses
// the string shorthand; everything else is a block array. In a user turn,
// tool_result blocks are moved ahead of the other blocks, which is the order
// Messages requires when a turn both answers calls and adds prose.
func encodeMessageContent(msg xir.Message) (json.RawMessage, error) {
	parts := msg.Parts
	if len(parts) == 1 && parts[0].Kind == xir.PartText && parts[0].Sealed == nil {
		return json.Marshal(parts[0].Text)
	}
	if encodeRole(msg.Role) == "user" {
		parts = toolResultsFirst(parts)
	}
	blocks, err := encodeBlocks(parts)
	if err != nil {
		return nil, err
	}
	return json.Marshal(blocks)
}

// toolResultsFirst is a stable partition: tool results keep their relative
// order and precede every other block.
func toolResultsFirst(parts []xir.Part) []xir.Part {
	out := make([]xir.Part, 0, len(parts))
	for _, part := range parts {
		if part.Kind == xir.PartToolResult {
			out = append(out, part)
		}
	}
	for _, part := range parts {
		if part.Kind != xir.PartToolResult {
			out = append(out, part)
		}
	}
	return out
}

// encodeBlocks renders parts as content blocks, skipping parts that have no
// Messages representation at all rather than emitting an empty block.
func encodeBlocks(parts []xir.Part) ([]json.RawMessage, error) {
	blocks := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		block, err := encodeBlock(part)
		if err != nil {
			return nil, err
		}
		if block != nil {
			blocks = append(blocks, block)
		}
	}
	return blocks, nil
}

// encodeBlock renders one IR part as a Messages content block. A sealed block
// tagged with this protocol is re-emitted byte for byte and before every other
// interpretation, because its content must not be rebuilt.
func encodeBlock(part xir.Part) (json.RawMessage, error) {
	if part.Sealed != nil && part.Sealed.Protocol == xir.ProtocolMessages &&
		len(part.Sealed.Raw) > 0 && json.Valid(part.Sealed.Raw) {
		return cloneRaw(part.Sealed.Raw), nil
	}
	switch part.Kind {
	case xir.PartText:
		return marshalBlock(textBlockWire{Type: "text", Text: part.Text})
	case xir.PartImage:
		return encodeMediaBlock(part.Media, false)
	case xir.PartFile:
		return encodeMediaBlock(part.Media, true)
	case xir.PartToolCall:
		return encodeToolUseBlock(part.ToolCall)
	case xir.PartToolResult:
		return encodeToolResultBlock(part.ToolResult)
	case xir.PartReasoning:
		return encodeReasoningBlock(part.Reasoning)
	case xir.PartRefusal:
		if part.Text == "" {
			return nil, nil
		}
		return marshalBlock(textBlockWire{Type: "text", Text: part.Text})
	case xir.PartUnknown, xir.PartCitation, xir.PartSearch:
		if part.Text == "" {
			return nil, nil
		}
		return marshalBlock(textBlockWire{Type: "text", Text: part.Text})
	default:
		if part.Text == "" {
			return nil, nil
		}
		return marshalBlock(textBlockWire{Type: "text", Text: part.Text})
	}
}

func encodeMediaBlock(media *xir.Media, document bool) (json.RawMessage, error) {
	if media == nil || (media.URL == "" && media.Data == "") {
		return nil, nil
	}
	source := mediaSourceWire{}
	switch {
	case media.URL != "":
		source.Type = "url"
		source.URL = media.URL
	case document && strings.HasPrefix(media.MediaType, "text/"):
		// A document text source carries plain text, not base64. The media
		// type is the only discriminator the IR carries.
		source.Type = "text"
		source.MediaType = media.MediaType
		source.Data = media.Data
	default:
		source.Type = "base64"
		source.MediaType = media.MediaType
		source.Data = media.Data
	}
	if document {
		return marshalBlock(documentBlockWire{Type: "document", Source: source, Title: media.Filename})
	}
	return marshalBlock(imageBlockWire{Type: "image", Source: source})
}

func encodeToolUseBlock(call *xir.ToolCall) (json.RawMessage, error) {
	if call == nil {
		return nil, nil
	}
	return marshalBlock(toolUseBlockWire{
		Type:  "tool_use",
		ID:    call.ID,
		Name:  call.Name,
		Input: toolInput(call),
	})
}

// toolInput returns the JSON object a tool_use block needs. Messages requires
// input to be present, so a call with no arguments gets the empty object; this
// is the one manufactured value in the codec and it carries no information.
func toolInput(call *xir.ToolCall) json.RawMessage {
	if len(call.Args) > 0 && json.Valid(call.Args) {
		if trimmed := strings.TrimSpace(string(call.Args)); trimmed != "null" {
			return cloneRaw(call.Args)
		}
	}
	if call.ArgsText != "" && json.Valid([]byte(call.ArgsText)) {
		return cloneRaw([]byte(call.ArgsText))
	}
	return json.RawMessage(`{}`)
}

func encodeToolResultBlock(result *xir.ToolResult) (json.RawMessage, error) {
	if result == nil {
		return nil, nil
	}
	block := toolResultBlockWire{
		Type:      "tool_result",
		ToolUseID: result.CallID,
		IsError:   result.IsError,
	}
	if len(result.Content) > 0 {
		blocks, err := encodeBlocks(result.Content)
		if err != nil {
			return nil, err
		}
		if len(blocks) > 0 {
			raw, err := json.Marshal(blocks)
			if err != nil {
				return nil, fmt.Errorf("messages: encode tool_result content: %w", err)
			}
			block.Content = raw
		}
	}
	if block.Content == nil && result.Text != "" {
		raw, err := json.Marshal(result.Text)
		if err != nil {
			return nil, fmt.Errorf("messages: encode tool_result text: %w", err)
		}
		block.Content = raw
	}
	return marshalBlock(block)
}

func encodeReasoningBlock(reasoning *xir.Reasoning) (json.RawMessage, error) {
	if reasoning == nil {
		return nil, nil
	}
	if sealed := reasoning.Sealed; sealed != nil && sealed.Protocol == xir.ProtocolMessages &&
		len(sealed.Raw) > 0 && json.Valid(sealed.Raw) {
		return cloneRaw(sealed.Raw), nil
	}
	if reasoning.EncryptedContent != "" {
		return marshalBlock(redactedThinkingBlockWire{Type: "redacted_thinking", Data: reasoning.EncryptedContent})
	}
	return marshalBlock(thinkingBlockWire{
		Type:      "thinking",
		Thinking:  reasoning.Text,
		Signature: reasoning.Signature,
	})
}

func marshalBlock(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("messages: encode content block: %w", err)
	}
	return raw, nil
}
