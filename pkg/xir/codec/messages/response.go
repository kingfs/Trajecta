package messages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// responseKnownKeys are the response fields this codec models. Unknown
// top-level fields, including stop_details, are kept in Extensions.
var responseKnownKeys = map[string]struct{}{
	"id":            {},
	"model":         {},
	"type":          {},
	"role":          {},
	"content":       {},
	"stop_reason":   {},
	"stop_sequence": {},
	"usage":         {},
	"error":         {},
}

// DecodeResponse projects a Messages response document onto the semantic IR.
//
// A {"type":"error","error":{...}} envelope produces a Response whose Error is
// set. A normal message decodes its content blocks, stop reason and usage;
// stop_details and any unknown top-level fields are preserved in Extensions
// under ExtensionKey so a Messages re-encode can restore them.
func (Codec) DecodeResponse(ctx context.Context, doc *xir.Document, opts xir.DecodeOptions) (*xir.Response, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	_ = opts
	if doc == nil || doc.Root == nil {
		return nil, fmt.Errorf("messages: nil response document")
	}
	root := doc.Root
	if root.Kind != wire.Object {
		return nil, fmt.Errorf("messages: response root is %s, want object", kindName(root.Kind))
	}

	responseType, _, err := stringMember(root, "type", "response")
	if err != nil {
		return nil, err
	}
	if responseType == "error" {
		return decodeResponseError(root)
	}
	if _, hasContent := root.Get("content"); !hasContent {
		if errNode, ok := root.Get("error"); ok && errNode.Kind == wire.Object {
			return decodeResponseError(root)
		}
		return nil, fmt.Errorf("messages: response has no content")
	}

	resp := &xir.Response{
		ID:         "",
		StopReason: xir.FinishUnknown,
		Ref:        root,
	}
	if resp.ID, _, err = stringMember(root, "id", "response"); err != nil {
		return nil, err
	}
	if resp.Model, _, err = stringMember(root, "model", "response"); err != nil {
		return nil, err
	}
	// Messages fixes these to "message" and "assistant"; they are validated
	// but have no separate IR field.
	if _, _, err := stringMember(root, "role", "response"); err != nil {
		return nil, err
	}
	if resp.Parts, err = decodeContentMember(root, "content", "response"); err != nil {
		return nil, err
	}
	stopReasonNode, hasStopReason := root.Get("stop_reason")
	if reason, present, err := stringMember(root, "stop_reason", "response"); err != nil {
		return nil, err
	} else if present {
		resp.StopReason = decodeStopReason(reason)
	}
	if sequence, present, err := stringMember(root, "stop_sequence", "response"); err != nil {
		return nil, err
	} else if present {
		resp.StopDetail = sequence
	}
	if stopDetails, ok := root.Get("stop_details"); ok && stopDetails.Kind == wire.Object {
		if resp.StopDetail == "" {
			if explanation, _, err := stringMember(stopDetails, "explanation", "response.stop_details"); err != nil {
				return nil, err
			} else if explanation != "" {
				resp.StopDetail = explanation
			} else if category, _, err := stringMember(stopDetails, "category", "response.stop_details"); err != nil {
				return nil, err
			} else {
				resp.StopDetail = category
			}
		}
	}
	if usageNode, ok := root.Get("usage"); ok && usageNode.Kind != wire.Null {
		decoded, err := decodeUsage(usageNode)
		if err != nil {
			return nil, err
		}
		resp.Usage = decoded
	}
	extras := extraMembers(root, responseKnownKeys)
	if hasStopReason && resp.StopReason == xir.FinishUnknown {
		// There is no IR value for this wire token: keep it so a Messages
		// re-encode can restore it instead of silently dropping it.
		if extras == nil {
			extras = map[string]json.RawMessage{}
		}
		extras["stop_reason"] = cloneRaw(stopReasonNode.Raw())
	}
	if extras != nil {
		resp.Extensions = map[string]json.RawMessage{ExtensionKey: marshalExtras(extras)}
	}
	return resp, nil
}

func decodeResponseError(root *wire.Node) (*xir.Response, error) {
	errNode, ok := root.Get("error")
	if !ok || errNode.Kind != wire.Object {
		return nil, fmt.Errorf("messages: error response has no error object")
	}
	decodeErr := &xir.Error{Raw: cloneRaw(errNode.Raw())}
	var err error
	if decodeErr.Type, _, err = stringMember(errNode, "type", "error"); err != nil {
		return nil, err
	}
	if decodeErr.Message, _, err = stringMember(errNode, "message", "error"); err != nil {
		return nil, err
	}
	if decodeErr.Param, _, err = stringMember(errNode, "param", "error"); err != nil {
		return nil, err
	}
	if decodeErr.Code, _, err = stringMember(errNode, "code", "error"); err != nil {
		return nil, err
	}
	return &xir.Response{
		Error:      decodeErr,
		StopReason: xir.FinishError,
		Ref:        root,
	}, nil
}

// decodeStopReason maps the Messages stop reasons. Only values with a stable
// IR meaning are mapped; pause_turn and newer wire values become FinishUnknown
// and stay in Extensions so the original token is not lost.
func decodeStopReason(reason string) xir.FinishReason {
	switch reason {
	case "end_turn", "stop_sequence":
		return xir.FinishStop
	case "max_tokens":
		return xir.FinishLength
	case "tool_use":
		return xir.FinishToolUse
	case "refusal":
		return xir.FinishRefusal
	default:
		return xir.FinishUnknown
	}
}

func decodeUsage(n *wire.Node) (*usage.Usage, error) {
	if n.Kind != wire.Object {
		return nil, fmt.Errorf("messages: usage is %s, want object", kindName(n.Kind))
	}
	u := &usage.Usage{Source: "messages"}
	var err error
	if u.InputTokens, _, err = intMember(n, "input_tokens", "usage"); err != nil {
		return nil, err
	}
	if u.OutputTokens, _, err = intMember(n, "output_tokens", "usage"); err != nil {
		return nil, err
	}
	if u.CacheCreationTokens, _, err = intMember(n, "cache_creation_input_tokens", "usage"); err != nil {
		return nil, err
	}
	if u.CacheReadTokens, _, err = intMember(n, "cache_read_input_tokens", "usage"); err != nil {
		return nil, err
	}
	if cacheCreation, ok := n.Get("cache_creation"); ok && cacheCreation.Kind != wire.Null {
		if cacheCreation.Kind != wire.Object {
			return nil, fmt.Errorf("messages: usage.cache_creation is %s, want object", kindName(cacheCreation.Kind))
		}
		if u.CacheCreation5mTokens, _, err = intMember(cacheCreation, "ephemeral_5m_input_tokens", "usage.cache_creation"); err != nil {
			return nil, err
		}
		if u.CacheCreation1hTokens, _, err = intMember(cacheCreation, "ephemeral_1h_input_tokens", "usage.cache_creation"); err != nil {
			return nil, err
		}
	}
	normalized := u.Normalize()
	return &normalized, nil
}

// EncodeResponse renders a normalized response as Messages JSON. A Response
// carrying Error is emitted as an error envelope instead of a message.
func (Codec) EncodeResponse(ctx context.Context, resp *xir.Response, opts xir.EncodeOptions) ([]byte, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("messages: nil response")
	}
	if resp.Error != nil {
		return encodeResponseError(resp.Error)
	}
	model := resp.Model
	if opts.Model != "" {
		model = opts.Model
	}
	blocks, err := encodeBlocks(resp.Parts)
	if err != nil {
		return nil, err
	}
	if blocks == nil {
		blocks = []json.RawMessage{}
	}
	stopReason, stopSequence := encodeStopReason(resp.StopReason, resp.StopDetail)
	out := responseWire{
		ID:           resp.ID,
		Type:         "message",
		Role:         "assistant",
		Model:        model,
		Content:      blocks,
		StopReason:   stopReason,
		StopSequence: stopSequence,
		Usage:        encodeUsage(resp.Usage),
	}
	return marshalWithExtensions(out, extensionRaw(resp.Extensions), responseOwnedKeys)
}

type responseWire struct {
	ID           string            `json:"id,omitempty"`
	Type         string            `json:"type"`
	Role         string            `json:"role"`
	Model        string            `json:"model,omitempty"`
	Content      []json.RawMessage `json:"content"`
	StopReason   string            `json:"stop_reason,omitempty"`
	StopSequence string            `json:"stop_sequence,omitempty"`
	Usage        *usageWire        `json:"usage,omitempty"`
}

// responseOwnedKeys are fields the response encoder always controls. It leaves
// stop_reason out so an unmapped wire token preserved in Extensions can fill
// the field, but the computed value always wins when there is one.
var responseOwnedKeys = map[string]struct{}{
	"id":            {},
	"type":          {},
	"role":          {},
	"model":         {},
	"content":       {},
	"stop_sequence": {},
	"usage":         {},
	"error":         {},
}

// encodeStopReason maps an IR finish reason back to Messages. A stop detail on
// a plain stop records which sequence matched, so it is restored as
// stop_sequence; otherwise a stop is end_turn.
func encodeStopReason(reason xir.FinishReason, detail string) (stopReason, stopSequence string) {
	switch reason {
	case xir.FinishStop:
		if detail != "" {
			return "stop_sequence", detail
		}
		return "end_turn", ""
	case xir.FinishLength:
		return "max_tokens", ""
	case xir.FinishToolUse:
		return "tool_use", ""
	case xir.FinishRefusal, xir.FinishContentFilter:
		return "refusal", ""
	default:
		return "", ""
	}
}

type usageWire struct {
	InputTokens              int                `json:"input_tokens"`
	OutputTokens             int                `json:"output_tokens"`
	CacheCreationInputTokens int                `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int                `json:"cache_read_input_tokens,omitempty"`
	CacheCreation            *cacheCreationWire `json:"cache_creation,omitempty"`
}

type cacheCreationWire struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
}

func encodeUsage(u *usage.Usage) *usageWire {
	if u == nil {
		return nil
	}
	out := &usageWire{
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheCreationInputTokens: u.CacheCreationTokens,
		CacheReadInputTokens:     u.CacheReadTokens,
	}
	if u.CacheCreation5mTokens != 0 || u.CacheCreation1hTokens != 0 {
		out.CacheCreation = &cacheCreationWire{
			Ephemeral5mInputTokens: u.CacheCreation5mTokens,
			Ephemeral1hInputTokens: u.CacheCreation1hTokens,
		}
	}
	return out
}

type errorEnvelopeWire struct {
	Type  string          `json:"type"`
	Error json.RawMessage `json:"error"`
}

type errorBodyWire struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message"`
	Param   string `json:"param,omitempty"`
	Code    string `json:"code,omitempty"`
}

func encodeResponseError(decodeErr *xir.Error) ([]byte, error) {
	body := json.RawMessage(nil)
	if len(decodeErr.Raw) > 0 && json.Valid(decodeErr.Raw) {
		trimmed := bytes.TrimSpace(decodeErr.Raw)
		if len(trimmed) > 0 && trimmed[0] == '{' {
			body = cloneRaw(decodeErr.Raw)
		}
	}
	if body == nil {
		encoded, err := json.Marshal(errorBodyWire{
			Type:    decodeErr.Type,
			Message: decodeErr.Message,
			Param:   decodeErr.Param,
			Code:    decodeErr.Code,
		})
		if err != nil {
			return nil, fmt.Errorf("messages: encode error: %w", err)
		}
		body = encoded
	}
	return json.Marshal(errorEnvelopeWire{Type: "error", Error: body})
}
