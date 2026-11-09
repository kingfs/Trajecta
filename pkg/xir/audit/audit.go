// Package audit projects the protocol-neutral Exchange IR onto an
// audit-friendly observation.
//
// The projection is derived and read-only. It is built from the semantic
// xir.Request/xir.Response plus the wire.Node references those values carry,
// so a node's byte span points back into the payload the codec parsed. This
// package does not parse a protocol, does not re-encode a payload, does not
// redact credentials and does not persist anything: the raw cassette remains
// the source of truth.
//
// Media values are reduced to metadata (kind, URL, media type and filename).
// Inline bytes (xir.Media.Data and data: URLs) are never copied into a node.
// A sealed protocol-native block is kept as a raw JSON reference and is never
// decoded into text.
package audit

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// Node sections. A Node is always rooted in exactly one of them.
const (
	SectionRequest  = "request"
	SectionResponse = "response"
)

// Observation is the audit projection of one exchange.
//
// Request and Response are section views for readers that want the structure
// back. Nodes is the flat, deduplicated list of every node in the exchange;
// every node in the section views also appears there. A nil req or resp
// produces the zero section and leaves the observation otherwise valid.
type Observation struct {
	Protocol  xir.Protocol  `json:"protocol"`
	Operation xir.Operation `json:"operation"`
	// Model is the model reported by the response, falling back to the
	// request model when the response does not name one.
	Model string `json:"model,omitempty"`
	// Status is the response status string as the protocol reported it (for
	// example Responses' "completed"). It is empty when the protocol carries
	// no status; a response error with no status is recorded as "error".
	Status   string       `json:"status,omitempty"`
	Request  RequestView  `json:"request"`
	Response ResponseView `json:"response"`
	// Usage is the provider-reported usage of the response, nil when absent.
	Usage *usage.Usage `json:"usage,omitempty"`
	Error *ErrorView   `json:"error,omitempty"`
	// Nodes is never nil, so an empty observation still serializes as [].
	Nodes []Node `json:"nodes"`
}

// RequestView is the request section of an observation.
//
// Instructions holds xir.Request.System parts. Protocols that carry the
// system prompt as a system/developer message instead keep those messages in
// Messages with their role, so no content is lifted or duplicated.
type RequestView struct {
	Model        string        `json:"model,omitempty"`
	Stream       bool          `json:"stream,omitempty"`
	Instructions []Node        `json:"instructions,omitempty"`
	Messages     []MessageView `json:"messages,omitempty"`
	Tools        []Node        `json:"tools,omitempty"`
}

// MessageView is one conversation turn and its content parts.
type MessageView struct {
	Node  Node   `json:"node"`
	Parts []Node `json:"parts,omitempty"`
}

// ResponseView is the response section of an observation.
//
// Parts preserves the response order. ToolCalls, ToolResults, Reasoning and
// Refusals group the same part nodes by kind; they are copies, not extra
// nodes, so the node IDs match the entries in Observation.Nodes.
type ResponseView struct {
	ID          string           `json:"id,omitempty"`
	Status      string           `json:"status,omitempty"`
	Parts       []Node           `json:"parts,omitempty"`
	ToolCalls   []Node           `json:"tool_calls,omitempty"`
	ToolResults []Node           `json:"tool_results,omitempty"`
	Reasoning   []Node           `json:"reasoning,omitempty"`
	Refusals    []Node           `json:"refusals,omitempty"`
	Usage       *usage.Usage     `json:"usage,omitempty"`
	StopReason  xir.FinishReason `json:"stop_reason,omitempty"`
	StopDetail  string           `json:"stop_detail,omitempty"`
}

// ErrorView is the normalized provider/proxy error of a response.
type ErrorView struct {
	Status  int             `json:"status,omitempty"`
	Type    string          `json:"type,omitempty"`
	Code    string          `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
	Param   string          `json:"param,omitempty"`
	Raw     json.RawMessage `json:"raw,omitempty"`
}

// MediaView is the audit-safe representation of one media part. It never
// carries inline bytes; a data: URL contributes only its media type.
type MediaView struct {
	Kind      xir.MediaKind `json:"kind,omitempty"`
	URL       string        `json:"url,omitempty"`
	MediaType string        `json:"media_type,omitempty"`
	Filename  string        `json:"filename,omitempty"`
}

// Node is one flat audit node: a container (message or tool declaration), a
// content part, or a sealed child block.
//
// ProtocolType is the protocol-native shape the node came from (a chat
// function tool, a messages "thinking" block, ...), while Kind is the
// normalized kind (text, image, tool_call, reasoning, sealed, message, tool,
// ...). Role is the normalized role of the containing turn.
//
// Path is a $-rooted logical JSON path into the node's section of the
// observation, for example $.messages[0].parts[1]. It is stable for a given
// xir.Request/xir.Response; it is not a byte pointer into the provider
// payload. SpanStart/SpanEnd are that raw byte range when the semantic value
// carried a wire.Node ref, and both are zero otherwise.
//
// Raw, when present, is a copy of protocol-native JSON that the semantic model
// does not fully express (sealed blocks, tool arguments/results, tool schema).
// It is never generated by decoding a sealed block into text. Text carries
// human-readable content only; typed media bytes are never copied there.
type Node struct {
	ID           string          `json:"id"`
	Section      string          `json:"section"`
	ProtocolType string          `json:"protocol_type"`
	Kind         string          `json:"kind"`
	Role         xir.Role        `json:"role,omitempty"`
	Name         string          `json:"name,omitempty"`
	ToolCallID   string          `json:"tool_call_id,omitempty"`
	Path         string          `json:"path"`
	Text         string          `json:"text,omitempty"`
	Raw          json.RawMessage `json:"raw,omitempty"`
	Media        *MediaView      `json:"media,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	SpanStart    int             `json:"span_start"`
	SpanEnd      int             `json:"span_end"`
	ParentID     string          `json:"parent_id,omitempty"`
}

// Build projects one exchange onto an observation. It accepts a nil request,
// a nil response, or both; the missing side becomes its zero section. The
// returned value is never nil.
func Build(protocol xir.Protocol, operation xir.Operation, req *xir.Request, resp *xir.Response) *Observation {
	observation := &Observation{
		Protocol:  protocol,
		Operation: operation,
		Nodes:     make([]Node, 0),
	}
	b := &builder{}

	if req != nil {
		observation.Model = req.Model
		observation.Request = b.request(req)
	}
	if resp != nil {
		if resp.Model != "" {
			observation.Model = resp.Model
		}
		observation.Status = resp.Status
		if observation.Status == "" && resp.Error != nil {
			observation.Status = "error"
		}
		observation.Response = b.response(resp)
		observation.Usage = cloneUsage(resp.Usage)
		if resp.Error != nil {
			observation.Error = errorView(resp.Error)
		}
	}

	observation.Nodes = dedupeNodes(b.nodes)
	return observation
}

// BuildObservation is an alias for Build, matching the entry-point name used
// by the architecture design. Build is the canonical name for new callers.
func BuildObservation(protocol xir.Protocol, operation xir.Operation, req *xir.Request, resp *xir.Response) *Observation {
	return Build(protocol, operation, req, resp)
}

// StableNodeID returns the deterministic ID for one node position. The key is
// section|path|protocolType|index; the ID is "node_" plus the first 16 hex
// characters of its SHA-1 digest. Identical input always yields the same ID.
func StableNodeID(section, path, protocolType string, index int) string {
	key := fmt.Sprintf("%s|%s|%s|%d", section, path, protocolType, index)
	sum := sha1.Sum([]byte(key))
	return "node_" + hex.EncodeToString(sum[:])[:16]
}

// EvidencePath returns the stable evidence locator for a node:
// trace#<traceID>#node#<nodeID>#path#<path>. An empty node ID or path omits
// that pair, mirroring the node shape a caller may hold before building.
func EvidencePath(traceID string, node Node) string {
	parts := make([]string, 0, 6)
	parts = append(parts, "trace", traceID)
	if node.ID != "" {
		parts = append(parts, "node", node.ID)
	}
	if node.Path != "" {
		parts = append(parts, "path", node.Path)
	}
	return strings.Join(parts, "#")
}

type builder struct {
	nodes []Node
}

func (b *builder) request(req *xir.Request) RequestView {
	view := RequestView{Model: req.Model, Stream: req.Stream}

	for i, part := range req.System {
		path := fmt.Sprintf("$.instructions[%d]", i)
		view.Instructions = append(view.Instructions,
			b.partNode(SectionRequest, path, part, xir.RoleSystem, "", i))
	}

	for i, message := range req.Messages {
		path := fmt.Sprintf("$.messages[%d]", i)
		messageNode := b.messageNode(path, message, i)
		viewMessage := MessageView{Node: messageNode}
		for j, part := range message.Parts {
			partPath := fmt.Sprintf("%s.parts[%d]", path, j)
			viewMessage.Parts = append(viewMessage.Parts,
				b.partNode(SectionRequest, partPath, part, message.Role, messageNode.ID, j))
		}
		view.Messages = append(view.Messages, viewMessage)
	}

	for i, tool := range req.Tools {
		path := fmt.Sprintf("$.tools[%d]", i)
		view.Tools = append(view.Tools, b.toolNode(path, tool, i))
	}

	return view
}

func (b *builder) response(resp *xir.Response) ResponseView {
	view := ResponseView{
		ID:         resp.ID,
		Status:     resp.Status,
		Usage:      cloneUsage(resp.Usage),
		StopReason: resp.StopReason,
		StopDetail: resp.StopDetail,
	}

	for i, part := range resp.Parts {
		path := fmt.Sprintf("$.parts[%d]", i)
		node := b.partNode(SectionResponse, path, part, responseRole(part), "", i)
		view.Parts = append(view.Parts, node)
		switch part.Kind {
		case xir.PartToolCall:
			view.ToolCalls = append(view.ToolCalls, node)
		case xir.PartToolResult:
			view.ToolResults = append(view.ToolResults, node)
		case xir.PartReasoning, xir.PartSealed:
			view.Reasoning = append(view.Reasoning, node)
		case xir.PartRefusal:
			view.Refusals = append(view.Refusals, node)
		}
	}

	return view
}

func (b *builder) messageNode(path string, message xir.Message, index int) Node {
	node := Node{
		ID:           StableNodeID(SectionRequest, path, "message", index),
		Section:      SectionRequest,
		ProtocolType: "message",
		Kind:         "message",
		Role:         message.Role,
		Name:         message.Name,
		Path:         path,
	}
	setSpan(&node, message.Ref)
	b.nodes = append(b.nodes, node)
	return node
}

func (b *builder) toolNode(path string, tool xir.Tool, index int) Node {
	protocolType := strings.TrimSpace(tool.Kind)
	if protocolType == "" {
		protocolType = "tool"
	}
	node := Node{
		ID:           StableNodeID(SectionRequest, path, protocolType, index),
		Section:      SectionRequest,
		ProtocolType: protocolType,
		Kind:         "tool",
		Name:         tool.Name,
		Text:         tool.Description,
		Path:         path,
	}
	setSpan(&node, tool.Ref)
	if len(tool.Schema) > 0 && json.Valid(tool.Schema) {
		node.Raw = cloneRaw(tool.Schema)
	}
	b.nodes = append(b.nodes, node)
	return node
}

// partNode appends one content part and then any child nodes it owns (a
// sealed block inside visible reasoning, or the content parts of a tool
// result). The returned node is the part itself.
func (b *builder) partNode(section, path string, part xir.Part, role xir.Role, parentID string, index int) Node {
	protocolType, kind := partType(part)
	node := Node{
		ID:           StableNodeID(section, path, protocolType, index),
		Section:      section,
		ProtocolType: protocolType,
		Kind:         kind,
		Role:         role,
		Path:         path,
		ParentID:     parentID,
	}
	setSpan(&node, part.Ref)

	switch part.Kind {
	case xir.PartText, xir.PartRefusal:
		node.Text = part.Text
	case xir.PartReasoning:
		applyReasoning(&node, part.Reasoning)
	case xir.PartSealed:
		applySealed(&node, part.Sealed)
	case xir.PartToolCall:
		applyToolCall(&node, part.ToolCall)
	case xir.PartToolResult:
		applyToolResult(&node, part.ToolResult)
	case xir.PartImage, xir.PartFile, xir.PartAudio, xir.PartVideo:
		node.Media = mediaView(part.Media, part.Kind)
	case xir.PartCitation:
		if part.Citation != nil {
			node.Text = firstNonEmpty(part.Citation.CitedText, part.Citation.Title, part.Citation.URL)
		}
	case xir.PartSearch:
		if part.Search != nil {
			node.Text = part.Search.Query
		}
	default:
		// Unknown parts may still carry human-readable text. Their raw JSON
		// is copied only when a codec preserved the block as sealed, because
		// an unrecognized block can hold media bytes.
		node.Text = part.Text
		if part.Sealed != nil {
			node.Raw = cloneRaw(part.Sealed.Raw)
		}
	}

	b.nodes = append(b.nodes, node)
	b.partChildren(section, path, part, node.ID, role)
	return node
}

func (b *builder) partChildren(section, path string, part xir.Part, parentID string, role xir.Role) {
	switch part.Kind {
	case xir.PartReasoning:
		reasoning := part.Reasoning
		if reasoning == nil || reasoning.Sealed == nil || reasoningText(reasoning) == "" {
			return
		}
		childPath := path + ".sealed"
		protocolType := sealedProtocolType(reasoning.Sealed)
		child := Node{
			ID:           StableNodeID(section, childPath, protocolType, 0),
			Section:      section,
			ProtocolType: protocolType,
			Kind:         string(xir.PartSealed),
			Role:         role,
			Path:         childPath,
			Raw:          cloneRaw(reasoning.Sealed.Raw),
			ParentID:     parentID,
		}
		setSpan(&child, part.Ref)
		b.nodes = append(b.nodes, child)
	case xir.PartToolResult:
		if part.ToolResult == nil {
			return
		}
		for i, content := range part.ToolResult.Content {
			childPath := fmt.Sprintf("%s.content[%d]", path, i)
			b.partNode(section, childPath, content, xir.RoleTool, parentID, i)
		}
	}
}

func partType(part xir.Part) (protocolType, kind string) {
	switch part.Kind {
	case xir.PartText:
		return "text", string(part.Kind)
	case xir.PartImage:
		return "image", string(part.Kind)
	case xir.PartFile:
		return "file", string(part.Kind)
	case xir.PartAudio:
		return "audio", string(part.Kind)
	case xir.PartVideo:
		return "video", string(part.Kind)
	case xir.PartToolCall:
		if part.ToolCall != nil {
			if typ := strings.TrimSpace(part.ToolCall.Type); typ != "" {
				return typ, string(part.Kind)
			}
		}
		return string(part.Kind), string(part.Kind)
	case xir.PartToolResult, xir.PartRefusal, xir.PartCitation, xir.PartSearch:
		return string(part.Kind), string(part.Kind)
	case xir.PartReasoning:
		reasoning := part.Reasoning
		if reasoning != nil && reasoningText(reasoning) == "" {
			if reasoning.Sealed != nil {
				return sealedProtocolType(reasoning.Sealed), string(xir.PartSealed)
			}
			if reasoning.EncryptedContent != "" {
				return string(xir.PartSealed), string(xir.PartSealed)
			}
		}
		return string(xir.PartReasoning), string(xir.PartReasoning)
	case xir.PartSealed:
		return sealedProtocolType(part.Sealed), string(xir.PartSealed)
	default:
		if part.Sealed != nil {
			return sealedProtocolType(part.Sealed), string(xir.PartUnknown)
		}
		return string(xir.PartUnknown), string(xir.PartUnknown)
	}
}

func applyReasoning(node *Node, reasoning *xir.Reasoning) {
	if reasoning == nil {
		return
	}
	node.Text = reasoningText(reasoning)

	if reasoning.Sealed != nil {
		if reasoningText(reasoning) == "" {
			applySealed(node, reasoning.Sealed)
		}
		return
	}
	if reasoning.EncryptedContent != "" || reasoning.Signature != "" {
		raw, err := json.Marshal(struct {
			EncryptedContent string `json:"encrypted_content,omitempty"`
			Signature        string `json:"signature,omitempty"`
		}{
			EncryptedContent: reasoning.EncryptedContent,
			Signature:        reasoning.Signature,
		})
		if err == nil {
			node.Raw = raw
		}
	}
}

func applySealed(node *Node, sealed *xir.Sealed) {
	if sealed == nil {
		return
	}
	node.ProtocolType = sealedProtocolType(sealed)
	node.Kind = string(xir.PartSealed)
	node.Text = ""
	node.Raw = cloneRaw(sealed.Raw)
}

func applyToolCall(node *Node, call *xir.ToolCall) {
	if call == nil {
		return
	}
	node.Name = call.Name
	node.ToolCallID = call.ID
	node.Text = call.ArgsText
	if node.Text == "" && len(call.Args) > 0 {
		node.Text = string(call.Args)
	}
	switch {
	case len(call.Args) > 0 && json.Valid(call.Args):
		node.Raw = cloneRaw(call.Args)
	case strings.TrimSpace(call.ArgsText) != "" && json.Valid([]byte(strings.TrimSpace(call.ArgsText))):
		node.Raw = cloneRaw([]byte(strings.TrimSpace(call.ArgsText)))
	}
}

func applyToolResult(node *Node, result *xir.ToolResult) {
	if result == nil {
		return
	}
	node.Name = result.Name
	node.ToolCallID = result.CallID
	node.Text = result.Text
	node.IsError = result.IsError
	if len(result.JSON) > 0 && json.Valid(result.JSON) {
		node.Raw = cloneRaw(result.JSON)
	}
}

func responseRole(part xir.Part) xir.Role {
	if part.Kind == xir.PartToolResult {
		return xir.RoleTool
	}
	return xir.RoleAssistant
}

func reasoningText(reasoning *xir.Reasoning) string {
	if reasoning == nil {
		return ""
	}
	if reasoning.Text != "" {
		return reasoning.Text
	}
	if len(reasoning.Summary) == 0 {
		return ""
	}
	return strings.Join(reasoning.Summary, "\n")
}

func sealedProtocolType(sealed *xir.Sealed) string {
	if sealed == nil {
		return string(xir.PartSealed)
	}
	if kind := strings.TrimSpace(sealed.Kind); kind != "" {
		return kind
	}
	return string(xir.PartSealed)
}

func mediaView(media *xir.Media, fallback xir.PartKind) *MediaView {
	view := &MediaView{}
	if media == nil {
		view.Kind = xir.MediaKind(fallback)
		return view
	}
	view.Kind = media.Kind
	if view.Kind == "" {
		view.Kind = xir.MediaKind(fallback)
	}
	view.MediaType = media.MediaType
	view.Filename = media.Filename
	url, dataType := splitDataURL(media.URL)
	view.URL = url
	if view.MediaType == "" {
		view.MediaType = dataType
	}
	return view
}

// splitDataURL drops the payload of a data: URL and returns the declared
// media type instead. A non-data URL is returned unchanged.
func splitDataURL(raw string) (url, mediaType string) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		return raw, ""
	}
	comma := strings.IndexByte(trimmed, ',')
	if comma < 0 {
		return "", ""
	}
	for _, parameter := range strings.Split(trimmed[len("data:"):comma], ";") {
		parameter = strings.TrimSpace(parameter)
		if strings.Contains(parameter, "/") {
			return "", parameter
		}
	}
	return "", ""
}

func setSpan(node *Node, ref *wire.Node) {
	if ref == nil {
		return
	}
	node.SpanStart = ref.Span.Start
	node.SpanEnd = ref.Span.End
}

func errorView(err *xir.Error) *ErrorView {
	if err == nil {
		return nil
	}
	return &ErrorView{
		Status:  err.Status,
		Type:    err.Type,
		Code:    err.Code,
		Message: err.Message,
		Param:   err.Param,
		Raw:     cloneRaw(err.Raw),
	}
}

func cloneUsage(u *usage.Usage) *usage.Usage {
	if u == nil {
		return nil
	}
	cloned := *u
	return &cloned
}

func cloneRaw(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func dedupeNodes(nodes []Node) []Node {
	if len(nodes) == 0 {
		return []Node{}
	}
	out := make([]Node, 0, len(nodes))
	seen := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if node.ID != "" {
			if _, ok := seen[node.ID]; ok {
				continue
			}
			seen[node.ID] = struct{}{}
		}
		out = append(out, node)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
