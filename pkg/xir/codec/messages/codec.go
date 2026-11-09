// Package messages implements the Anthropic Messages protocol codec for the
// Exchange IR.
//
// The codec projects a Messages JSON document onto xir.Request/xir.Response and
// renders them back. Decoding is structural: the shape of the protocol is
// parsed (a string or block array for content, the block types Messages
// defines) while unknown top-level fields are kept in Extensions under
// ExtensionKey so a same-protocol re-encode can carry them back.
//
// Two protocol facts drive the mapping:
//
//   - tool_use / tool_result blocks are the same semantic values as the IR's
//     ToolCall / ToolResult;
//   - thinking and redacted_thinking are reasoning. A redacted_thinking block
//     is opaque: its bytes are kept in Sealed, tagged with ProtocolMessages,
//     and only this codec re-emits them. The visible thinking signature is
//     kept in Reasoning.Signature.
//
// The codec is stateless and safe for concurrent use.
package messages

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// ExtensionKey is the Request.Extensions / Response.Extensions key holding
// Messages fields the semantic IR does not model. Keeping them per protocol
// means a later Messages encode can restore them while a different protocol's
// encoder ignores them.
const ExtensionKey = "messages"

// Codec implements xir.Codec for the Anthropic Messages wire protocol.
type Codec struct{}

// New returns a Messages codec. The returned pointer satisfies xir.Codec.
func New() *Codec { return &Codec{} }

var (
	_ xir.Codec = Codec{}
	_ xir.Codec = (*Codec)(nil)
)

// Protocol reports the protocol this codec speaks.
func (Codec) Protocol() xir.Protocol { return xir.ProtocolMessages }

// checkContext reports a cancelled context without panicking on a nil one.
func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// cloneRaw copies raw so the semantic value never aliases a document buffer
// that a caller may rewrite.
func cloneRaw(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

// stringMember reads an optional object string member. present is false for a
// missing or null member; a member of another JSON kind is an error.
func stringMember(n *wire.Node, key, path string) (value string, present bool, err error) {
	v, ok := n.Get(key)
	if !ok || v.Kind == wire.Null {
		return "", false, nil
	}
	s, ok := v.String()
	if !ok {
		return "", false, fmt.Errorf("%s.%s is %s, want string", path, key, kindName(v.Kind))
	}
	return s, true, nil
}

// intMember reads an optional object integer member.
func intMember(n *wire.Node, key, path string) (value int, present bool, err error) {
	v, ok := n.Get(key)
	if !ok || v.Kind == wire.Null {
		return 0, false, nil
	}
	i, ok := v.Int()
	if !ok {
		return 0, false, fmt.Errorf("%s.%s is %s, want integer", path, key, kindName(v.Kind))
	}
	return int(i), true, nil
}

// floatMember reads an optional object number member.
func floatMember(n *wire.Node, key, path string) (value float64, present bool, err error) {
	v, ok := n.Get(key)
	if !ok || v.Kind == wire.Null {
		return 0, false, nil
	}
	f, ok := v.Float()
	if !ok {
		return 0, false, fmt.Errorf("%s.%s is %s, want number", path, key, kindName(v.Kind))
	}
	return f, true, nil
}

// boolMember reads an optional object boolean member.
func boolMember(n *wire.Node, key, path string) (value bool, present bool, err error) {
	v, ok := n.Get(key)
	if !ok || v.Kind == wire.Null {
		return false, false, nil
	}
	b, ok := v.Bool()
	if !ok {
		return false, false, fmt.Errorf("%s.%s is %s, want boolean", path, key, kindName(v.Kind))
	}
	return b, true, nil
}

// rawMember reads an optional member as raw JSON. A null member is treated as
// absent.
func rawMember(n *wire.Node, key string) (json.RawMessage, bool) {
	v, ok := n.Get(key)
	if !ok || v.Kind == wire.Null {
		return nil, false
	}
	return cloneRaw(v.Raw()), true
}

func kindName(k wire.Kind) string {
	switch k {
	case wire.Object:
		return "object"
	case wire.Array:
		return "array"
	case wire.String:
		return "string"
	case wire.Number:
		return "number"
	case wire.Bool:
		return "boolean"
	case wire.Null:
		return "null"
	default:
		return "unknown"
	}
}

// extraMembers collects object members outside known as a JSON object. The
// result is nil when nothing is left over.
func extraMembers(n *wire.Node, known map[string]struct{}) map[string]json.RawMessage {
	var out map[string]json.RawMessage
	for _, m := range n.Members {
		if _, skip := known[m.Key]; skip {
			continue
		}
		if out == nil {
			out = make(map[string]json.RawMessage, len(n.Members))
		}
		out[m.Key] = cloneRaw(m.Value.Raw())
	}
	return out
}

// marshalExtras renders collected extra members. Raw values come from a parsed
// document, so marshalling cannot fail in practice.
func marshalExtras(extras map[string]json.RawMessage) json.RawMessage {
	if len(extras) == 0 {
		return nil
	}
	raw, err := json.Marshal(extras)
	if err != nil {
		return nil
	}
	return raw
}

// extensionRaw returns the Messages extension object of a request/response.
func extensionRaw(ext map[string]json.RawMessage) json.RawMessage {
	if len(ext) == 0 {
		return nil
	}
	return ext[ExtensionKey]
}

// marshalWithExtensions renders v and restores unmodeled extension members.
// A key the encoder owns is never taken from the extension (the modeled value
// wins, even when it is omitted), and a key v already produced wins as well.
func marshalWithExtensions(v any, ext json.RawMessage, owned map[string]struct{}) ([]byte, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("messages: encode: %w", err)
	}
	if len(ext) == 0 {
		return encoded, nil
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, fmt.Errorf("messages: encode: %w", err)
	}
	extra := map[string]json.RawMessage{}
	if err := json.Unmarshal(ext, &extra); err != nil {
		return nil, fmt.Errorf("messages: %s extension is not a JSON object: %w", ExtensionKey, err)
	}
	for key, value := range extra {
		if _, skip := owned[key]; skip {
			continue
		}
		if _, exists := fields[key]; exists {
			continue
		}
		fields[key] = value
	}
	return json.Marshal(fields)
}

// decodeRole maps a Messages message role onto the IR role.
func decodeRole(role string) xir.Role {
	switch role {
	case "user":
		return xir.RoleUser
	case "assistant":
		return xir.RoleAssistant
	case "system":
		return xir.RoleSystem
	case "tool":
		return xir.RoleTool
	case "model":
		return xir.RoleModel
	default:
		return xir.RoleUnknown
	}
}

// encodeRole maps an IR role onto a Messages message role. Messages only
// defines user and assistant: a tool turn is a user turn carrying
// tool_result blocks, and any other role is rendered as user rather than
// dropped, because dropping a turn loses context.
func encodeRole(role xir.Role) string {
	switch role {
	case xir.RoleAssistant, xir.RoleModel:
		return "assistant"
	default:
		return "user"
	}
}
