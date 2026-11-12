// Package xir defines the protocol-neutral Exchange IR shared by the hot
// forwarding path and the audit path.
//
// The IR has two representations:
//
//   - wire: the lossless JSON tree in pkg/xir/wire, with byte spans;
//   - semantic: the typed Request/Response in this package.
//
// A codec decodes semantic values from a wire Document and encodes them back
// for a target protocol. Detection and rewrite hooks can operate on either
// representation. The IR deliberately does not carry routing, database or
// agent state.
package xir

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// Protocol is one wire protocol a client can speak or an upstream can serve.
type Protocol string

const (
	ProtocolChatCompletions Protocol = "chat_completions"
	ProtocolResponses       Protocol = "responses"
	ProtocolMessages        Protocol = "messages"
	ProtocolGemini          Protocol = "gemini"
)

// ParseProtocol normalizes a protocol name. It accepts the aliases used by
// the existing codebase (chat, claude, generate_content, vertex).
func ParseProtocol(s string) (Protocol, error) {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", "_")) {
	case "chat", "chat_completions", "openai_chat", "chatcompletions":
		return ProtocolChatCompletions, nil
	case "responses", "openai_responses":
		return ProtocolResponses, nil
	case "messages", "anthropic", "anthropic_messages", "claude":
		return ProtocolMessages, nil
	case "gemini", "generate_content", "google", "vertex":
		return ProtocolGemini, nil
	default:
		return "", fmt.Errorf("xir: unknown protocol %q", s)
	}
}

// Valid reports whether p is a supported protocol.
func (p Protocol) Valid() bool {
	switch p {
	case ProtocolChatCompletions, ProtocolResponses, ProtocolMessages, ProtocolGemini:
		return true
	}
	return false
}

// Endpoint is the canonical client endpoint for p.
func (p Protocol) Endpoint() string {
	switch p {
	case ProtocolChatCompletions:
		return "/v1/chat/completions"
	case ProtocolResponses:
		return "/v1/responses"
	case ProtocolMessages:
		return "/v1/messages"
	case ProtocolGemini:
		return "/v1beta/models:generateContent"
	}
	return ""
}

// Operation is the logical operation of an exchange. It is separate from the
// protocol because the same protocol carries several operations.
type Operation string

const (
	OperationChat            Operation = "chat_completions"
	OperationResponses       Operation = "responses"
	OperationMessages        Operation = "messages"
	OperationGenerateContent Operation = "generate_content"
	OperationCountTokens     Operation = "count_tokens"
	OperationEmbeddings      Operation = "embeddings"
	OperationUnknown         Operation = "unknown"
)

// Role is a normalized message role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleModel     Role = "model"
	RoleUnknown   Role = "unknown"
)

// PartKind is one content block kind.
type PartKind string

const (
	PartText       PartKind = "text"
	PartImage      PartKind = "image"
	PartFile       PartKind = "file"
	PartAudio      PartKind = "audio"
	PartVideo      PartKind = "video"
	PartToolCall   PartKind = "tool_call"
	PartToolResult PartKind = "tool_result"
	PartReasoning  PartKind = "reasoning"
	PartRefusal    PartKind = "refusal"
	PartCitation   PartKind = "citation"
	PartSearch     PartKind = "search"
	PartSealed     PartKind = "sealed"
	PartUnknown    PartKind = "unknown"
)

// FinishReason is a normalized completion stop reason.
type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolUse       FinishReason = "tool_use"
	FinishContentFilter FinishReason = "content_filter"
	FinishRefusal       FinishReason = "refusal"
	FinishError         FinishReason = "error"
	FinishUnknown       FinishReason = "unknown"
)

// MediaKind is the normalized media payload shape.
type MediaKind string

const (
	MediaImage MediaKind = "image"
	MediaFile  MediaKind = "file"
	MediaAudio MediaKind = "audio"
	MediaVideo MediaKind = "video"
)

// Document is a parsed wire payload plus its protocol identity.
type Document struct {
	Protocol  Protocol
	Operation Operation
	Raw       []byte
	Root      *wire.Node
}

// ParseDocument parses raw JSON for protocol into a Document. It does not
// interpret protocol semantics; codecs do that.
func ParseDocument(protocol Protocol, operation Operation, raw []byte) (*Document, error) {
	root, err := wire.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &Document{Protocol: protocol, Operation: operation, Raw: raw, Root: root}, nil
}

// DecodeOptions controls how much of a payload a codec materializes.
type DecodeOptions struct {
	NeedParts bool
	NeedTools bool
	NeedUsage bool
	// NeedState asks for previous_response_id / conversation / prompt_cache_key.
	NeedState bool
}

// EncodeOptions controls target-protocol encoding.
type EncodeOptions struct {
	// Model overrides Request.Model / Response.Model when non-empty.
	Model string
	// Stream marks a request as streaming when the target protocol needs it.
	Stream bool
	// Vertex selects the Vertex wire shape for Gemini.
	Vertex bool
}

// Codec decodes and encodes one protocol. Implementations are stateless and
// safe for concurrent use.
type Codec interface {
	Protocol() Protocol
	DecodeRequest(ctx context.Context, doc *Document, opts DecodeOptions) (*Request, error)
	EncodeRequest(ctx context.Context, req *Request, opts EncodeOptions) ([]byte, error)
	DecodeResponse(ctx context.Context, doc *Document, opts DecodeOptions) (*Response, error)
	EncodeResponse(ctx context.Context, resp *Response, opts EncodeOptions) ([]byte, error)
}

// Exchange is one decoded request/response pair.
type Exchange struct {
	Protocol  Protocol
	Operation Operation
	Request   *Request
	Response  *Response
	Loss      []Loss
}

// Request is a protocol-neutral model request.
type Request struct {
	Model      string
	System     []Part
	Messages   []Message
	Tools      []Tool
	ToolChoice ToolChoice
	Params     Params
	State      StateRefs
	Stream     bool
	// Extensions preserves protocol-specific fields that are not modeled.
	Extensions map[string]json.RawMessage
	Ref        *wire.Node
}

// Message is one conversation turn.
type Message struct {
	Role  Role
	Name  string
	Parts []Part
	Ref   *wire.Node
}

// Part is one content block.
type Part struct {
	Kind PartKind

	Text       string
	ToolCall   *ToolCall
	ToolResult *ToolResult
	Media      *Media
	Reasoning  *Reasoning
	Citation   *Citation
	Search     *Search
	Sealed     *Sealed

	Ref *wire.Node
}

// ToolCall is a model-issued function/tool call.
type ToolCall struct {
	ID       string
	Name     string
	Type     string
	Args     json.RawMessage
	ArgsText string
	Ref      *wire.Node
}

// ToolResult is the client's answer to a ToolCall.
type ToolResult struct {
	CallID  string
	Name    string
	Content []Part
	Text    string
	JSON    json.RawMessage
	IsError bool
	Ref     *wire.Node
}

// Tool is a tool declaration.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Strict      *bool
	Kind        string // function, custom, web_search, file_search, mcp, ...
	Namespace   string
	Ref         *wire.Node
}

// ToolChoice is the normalized tool-selection directive. Mode is one of
// auto, none, required, tool; Name is set only for mode=tool.
type ToolChoice struct {
	Mode string
	Name string
}

// Params is the protocol-neutral generation parameter set. A nil pointer
// means the client did not send the field.
type Params struct {
	Temperature    *float64
	TopP           *float64
	TopK           *int
	MaxTokens      *int
	MaxCompletion  *int
	Stop           []string
	ParallelTools  *bool
	ResponseFormat json.RawMessage

	ReasoningEffort string
	ThinkingBudget  *int
	Thinking        *bool

	Seed             *int64
	PresencePenalty  *float64
	FrequencyPenalty *float64
	Logprobs         *bool
	TopLogprobs      *int
	User             string
	Metadata         json.RawMessage
	// Extra keeps parameter fields the semantic model does not carry.
	Extra map[string]json.RawMessage
}

// StateRefs are server-side conversation references. They are separate from
// ordinary metadata because they force hard upstream affinity.
type StateRefs struct {
	PreviousResponseID string
	ConversationID     string
	PromptCacheKey     string
	Store              *bool
}

// HasServerState reports whether the request depends on server-side state.
func (s StateRefs) HasServerState() bool {
	return strings.TrimSpace(s.PreviousResponseID) != "" || strings.TrimSpace(s.ConversationID) != ""
}

// Response is a protocol-neutral model response.
type Response struct {
	ID         string
	Model      string
	Status     string
	Parts      []Part
	Usage      *usage.Usage
	StopReason FinishReason
	StopDetail string
	Error      *Error
	Extensions map[string]json.RawMessage
	Ref        *wire.Node
}

// Error is a normalized provider/proxy error.
type Error struct {
	Status  int
	Type    string
	Code    string
	Message string
	Param   string
	Raw     json.RawMessage
}

// Media is an image/file/audio/video payload. Exactly one of URL or Data is
// normally set; Data is base64 when the protocol carries bytes inline.
type Media struct {
	Kind      MediaKind
	URL       string
	Data      string
	MediaType string
	Detail    string
	Filename  string
}

// Reasoning is visible or sealed model reasoning.
type Reasoning struct {
	Text             string
	Signature        string
	EncryptedContent string
	Summary          []string
	Sealed           *Sealed
}

// Sealed is a protocol-native opaque block. Its bytes are preserved, but it
// must only be re-emitted by the protocol that produced it.
//
// MustRoundTrip separates two kinds of preserved block that look the same in
// the IR but mean opposite things to a content detector:
//
//   - true: the surrounding wire object is bound by a signature, ciphertext or
//     integrity check. A stand-in that rewrites any byte of it (including
//     visible text next to the signature, such as an Anthropic thinking block
//     or a Gemini thought part) makes the provider reject the request, so a
//     content scanner must never be fed it.
//   - false: the block is merely unmodeled - a protocol-native part this codec
//     does not type, such as Gemini executableCode. It is preserved for
//     re-emission, but it is ordinary content: a credential inside it must
//     still be detected and masked.
//
// A codec sets this flag; a consumer must not infer it from Kind.
type Sealed struct {
	Protocol      Protocol
	Kind          string
	Raw           json.RawMessage
	MustRoundTrip bool
}

// Citation is one model citation.
type Citation struct {
	Title     string
	URL       string
	Start     int
	End       int
	CitedText string
}

// Search is one model-run web search.
type Search struct {
	Query string
	Hits  []Citation
}

// LossKind describes how a conversion affected one field.
type LossKind string

const (
	LossUnsupported  LossKind = "unsupported"
	LossDegraded     LossKind = "degraded"
	LossApproximated LossKind = "approximated"
	LossRejected     LossKind = "rejected"
)

// Loss is one conversion diagnostic.
type Loss struct {
	Field  string
	Kind   LossKind
	Reason string
	Fatal  bool
	From   Protocol
	To     Protocol
}

// Error implements error for a fatal loss.
func (l Loss) Error() string {
	return fmt.Sprintf("xir: %s: %s (%s -> %s)", l.Field, l.Reason, l.From, l.To)
}

// Capabilities is the set of protocols one upstream serves natively.
type Capabilities struct {
	ChatCompletions bool
	Responses       bool
	Messages        bool
	Gemini          bool
}

// Has reports whether p is served natively.
func (c Capabilities) Has(p Protocol) bool {
	switch p {
	case ProtocolChatCompletions:
		return c.ChatCompletions
	case ProtocolResponses:
		return c.Responses
	case ProtocolMessages:
		return c.Messages
	case ProtocolGemini:
		return c.Gemini
	}
	return false
}

// Protocols returns the served protocols in a stable order.
func (c Capabilities) Protocols() []Protocol {
	out := make([]Protocol, 0, 4)
	for _, p := range []Protocol{ProtocolChatCompletions, ProtocolResponses, ProtocolMessages, ProtocolGemini} {
		if c.Has(p) {
			out = append(out, p)
		}
	}
	return out
}

// StreamEventKind is the kind of one streamed event.
type StreamEventKind string

const (
	StreamStart     StreamEventKind = "start"
	StreamText      StreamEventKind = "text"
	StreamReasoning StreamEventKind = "reasoning"
	StreamToolStart StreamEventKind = "tool_start"
	StreamToolArgs  StreamEventKind = "tool_args"
	StreamToolEnd   StreamEventKind = "tool_end"
	StreamUsage     StreamEventKind = "usage"
	StreamStop      StreamEventKind = "stop"
	StreamError     StreamEventKind = "error"
	StreamPing      StreamEventKind = "ping"
	StreamSealed    StreamEventKind = "sealed"
	StreamUnknown   StreamEventKind = "unknown"
)

// StreamEvent is one normalized streaming event. Span points into the raw
// upstream or client frame when the codec can provide it.
type StreamEvent struct {
	Kind       StreamEventKind
	Protocol   Protocol
	Span       wire.Span
	Text       string
	ToolID     string
	ToolName   string
	Reasoning  *Reasoning
	Sealed     *Sealed
	Usage      *usage.Usage
	StopReason FinishReason
	Error      *Error
	At         time.Time
}

// StreamDecoder decodes upstream frames one at a time.
type StreamDecoder interface {
	Next(ctx context.Context) (*StreamEvent, error)
}

// StreamEncoder encodes normalized events for a client protocol.
type StreamEncoder interface {
	Write(ctx context.Context, ev *StreamEvent) error
	Flush(ctx context.Context) error
	Close(ctx context.Context) error
}
