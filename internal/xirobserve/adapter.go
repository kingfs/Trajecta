// Package xirobserve projects an XIR-decoded exchange onto the persisted
// observation IR (observe.TraceObservation). It is the first stage of the
// observation-lane convergence: the stored shape, the detectors and the
// consumers stay unchanged, while the semantic decode comes from the XIR
// codecs instead of the per-protocol observe parsers.
//
// Scope of this stage: non-streaming model exchanges for all four wire
// protocols. Streaming responses and client entry exchanges return
// ErrUnsupported so the caller can fall back to pkg/observe; those are the
// remaining gaps before the engine can become the default. The caller chooses
// the engine; this package never changes a default by itself.
package xirobserve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/codec/registry"
)

// ErrUnsupported marks an exchange this stage of the XIR observation engine
// does not model. Callers must fall back to the observe parser rather than
// recording a parse failure.
var ErrUnsupported = errors.New("xirobserve: exchange is not supported by the XIR observation engine")

// ParserName and ParserVersion identify the engine in parser_versions and the
// observation JSON.
const (
	ParserName    = "xir"
	ParserVersion = "0.1.0"
)

// Parse decodes one cassette exchange through the XIR codecs and projects it
// onto the stored observation shape.
func Parse(ctx context.Context, input observe.ParseInput) (observe.TraceObservation, error) {
	if err := ctx.Err(); err != nil {
		return observe.TraceObservation{}, err
	}
	if unsupported(input) {
		return observe.TraceObservation{}, ErrUnsupported
	}
	protocol, operation, ok := resolveExchange(input)
	if !ok {
		return observe.TraceObservation{}, ErrUnsupported
	}
	codec, ok := registry.Default().Lookup(protocol)
	if !ok {
		return observe.TraceObservation{}, ErrUnsupported
	}
	obs := baseObservation(input)
	decodeOptions := xir.DecodeOptions{NeedParts: true, NeedTools: true, NeedUsage: true, NeedState: true}
	builder := &nodeBuilder{traceID: input.TraceID, obs: obs}

	reqDoc, err := xir.ParseDocument(protocol, operation, input.RequestBody)
	if err != nil {
		return obs, fmt.Errorf("xirobserve: parse %s request: %w", protocol, err)
	}
	request, err := codec.DecodeRequest(ctx, reqDoc, decodeOptions)
	if err != nil {
		return obs, fmt.Errorf("xirobserve: decode %s request: %w", protocol, err)
	}
	obs.Model = request.Model
	builder.request(request, operation)

	var response *xir.Response
	if len(strings.TrimSpace(string(input.ResponseBody))) > 0 {
		respDoc, err := xir.ParseDocument(protocol, operation, input.ResponseBody)
		if err != nil {
			return obs, fmt.Errorf("xirobserve: parse %s response: %w", protocol, err)
		}
		response, err = codec.DecodeResponse(ctx, respDoc, decodeOptions)
		if err != nil {
			return obs, fmt.Errorf("xirobserve: decode %s response: %w", protocol, err)
		}
	} else {
		obs.Warnings = append(obs.Warnings, observe.ParseWarning{Code: "empty_response", Message: "the cassette has no response body"})
	}
	if response != nil {
		builder.response(response)
		if obs.Model == "" {
			obs.Model = response.Model
		}
	}

	obs = builder.obs
	obs.Usage = observationUsage(response)
	if obs.Usage.InputTokens == 0 && obs.Usage.OutputTokens == 0 {
		obs.Usage = observationUsageFromHeader(input)
	}
	obs.Safety = builder.safety
	return obs, nil
}

// unsupported reports the exchanges left for the fallback engine in this
// stage: client entry exchanges (no xir codec) and any streamed response
// (XIR stream assembly currently exists for chat only).
func unsupported(input observe.ParseInput) bool {
	if input.ExchangeKind == "entry" || input.Header.Meta.ExchangeKind == "entry" {
		return true
	}
	if input.IsStream || input.Header.Layout.IsStream || looksLikeSSE(input.ResponseBody) {
		return true
	}
	return false
}

func looksLikeSSE(body []byte) bool {
	trimmed := strings.TrimLeft(string(body), " \t\r\n")
	return strings.HasPrefix(trimmed, "data:") || strings.HasPrefix(trimmed, "event:")
}

// resolveExchange maps the cassette metadata onto the XIR protocol/operation.
// The endpoint is authoritative; the recorded operation is the fallback.
func resolveExchange(input observe.ParseInput) (xir.Protocol, xir.Operation, bool) {
	endpoint := llm.NormalizeEndpoint(input.Header.Meta.Endpoint)
	switch {
	case endpoint == "/v1/chat/completions":
		return xir.ProtocolChatCompletions, xir.OperationChat, true
	case endpoint == "/v1/responses":
		return xir.ProtocolResponses, xir.OperationResponses, true
	case endpoint == "/v1/messages":
		return xir.ProtocolMessages, xir.OperationMessages, true
	case strings.Contains(endpoint, ":generateContent"):
		return xir.ProtocolGemini, xir.OperationGenerateContent, true
	}
	switch input.Header.Meta.Operation {
	case llm.OperationChatCompletions:
		return xir.ProtocolChatCompletions, xir.OperationChat, true
	case llm.OperationResponses:
		return xir.ProtocolResponses, xir.OperationResponses, true
	case llm.OperationMessages:
		return xir.ProtocolMessages, xir.OperationMessages, true
	case llm.OperationGenerateContent:
		return xir.ProtocolGemini, xir.OperationGenerateContent, true
	}
	return "", "", false
}

func baseObservation(input observe.ParseInput) observe.TraceObservation {
	meta := input.Header.Meta
	return observe.TraceObservation{
		TraceID:          input.TraceID,
		Provider:         meta.Provider,
		Operation:        meta.Operation,
		Endpoint:         meta.Endpoint,
		Model:            meta.Model,
		ExchangeKind:     firstNonEmpty(input.ExchangeKind, meta.ExchangeKind),
		ExchangeRole:     firstNonEmpty(input.ExchangeRole, meta.ExchangeRole),
		ParentExchangeID: firstNonEmpty(input.ParentExchangeID, meta.ParentExchangeID),
		SequenceIndex:    input.SequenceIndex,
		RequestAuditID:   firstNonEmpty(input.RequestAuditID, meta.RequestAuditID),
		ResponseID:       firstNonEmpty(input.ResponseID, meta.ResponseID),
		Parser:           ParserName,
		ParserVersion:    ParserVersion,
		Status:           observe.ParseStatusParsed,
		RawRefs:          observe.RawReferences{CassettePath: input.CassettePath},
		Timings: observe.ObservationTimings{
			StartedAt:  meta.Time,
			DurationMs: meta.DurationMs,
			TTFTMs:     meta.TTFTMs,
		},
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func observationUsage(response *xir.Response) observe.ObservationUsage {
	if response == nil || response.Usage == nil {
		return observe.ObservationUsage{}
	}
	return usageToObservation(response.Usage)
}

func observationUsageFromHeader(input observe.ParseInput) observe.ObservationUsage {
	u := input.Header.Usage
	return observe.ObservationUsage{
		InputTokens:         u.PromptTokens,
		OutputTokens:        u.CompletionTokens,
		TotalTokens:         u.TotalTokens,
		CacheReadTokens:     cachedTokens(input),
		CacheCreationTokens: 0,
	}
}

func cachedTokens(input observe.ParseInput) int {
	if details := input.Header.Usage.PromptTokenDetails; details != nil {
		return details.CachedTokens
	}
	return 0
}

func usageToObservation(u *usage.Usage) observe.ObservationUsage {
	normalized := u.Normalize()
	return observe.ObservationUsage{
		InputTokens:         normalized.InputTokens,
		OutputTokens:        normalized.OutputTokens,
		TotalTokens:         normalized.TotalTokens,
		ReasoningTokens:     normalized.ReasoningTokens,
		CacheReadTokens:     normalized.CacheReadTokens,
		CacheCreationTokens: normalized.CacheCreationTokens,
	}
}

// nodeBuilder accumulates the projected nodes. The observe IR stores every
// root in request.response.Nodes and the content parts as children, so
// observe.TraceNodes (and therefore every detector) sees the same content.
type nodeBuilder struct {
	traceID string
	obs     observe.TraceObservation
	safety  observe.ObservationSafety
}

func (b *nodeBuilder) request(request *xir.Request, operation xir.Operation) {
	for i := range request.System {
		node := b.partNode("instruction", fmt.Sprintf("$.instructions[%d]", i), request.System[i], xir.RoleSystem, false)
		b.obs.Request.Instructions = append(b.obs.Request.Instructions, node)
	}
	if operation == xir.OperationResponses {
		// Responses carries the conversation as input items; keep the message
		// nodes under Inputs so the UI groups them the same way observe does.
	} else {
		b.obs.Request.Messages = make([]observe.SemanticNode, 0, len(request.Messages))
	}
	for i := range request.Messages {
		message := &request.Messages[i]
		path := fmt.Sprintf("$.messages[%d]", i)
		if operation == xir.OperationResponses {
			path = fmt.Sprintf("$.input[%d]", i)
		}
		node := observe.SemanticNode{
			ID:             observe.StableNodeID("request", path, "message", i),
			ProviderType:   "message",
			NormalizedType: observe.NodeMessage,
			Role:           string(message.Role),
			Path:           path,
			Index:          i,
		}
		for j := range message.Parts {
			node.Children = append(node.Children, b.partNode("request", fmt.Sprintf("%s.parts[%d]", path, j), message.Parts[j], message.Role, false))
		}
		if operation == xir.OperationResponses {
			b.obs.Request.Inputs = append(b.obs.Request.Inputs, node)
		} else {
			b.obs.Request.Messages = append(b.obs.Request.Messages, node)
		}
	}
	b.obs.Request.Nodes = append(b.obs.Request.Nodes, b.obs.Request.Instructions...)
	if operation != xir.OperationResponses {
		b.obs.Request.Nodes = append(b.obs.Request.Nodes, b.obs.Request.Messages...)
	}
	b.obs.Request.Nodes = append(b.obs.Request.Nodes, b.obs.Request.Inputs...)
	for i := range request.Tools {
		node := b.toolNode(fmt.Sprintf("$.tools[%d]", i), &request.Tools[i], i)
		b.obs.Request.Tools = append(b.obs.Request.Tools, node)
		b.obs.Request.Nodes = append(b.obs.Request.Nodes, node)
		b.obs.Tools.Declarations = append(b.obs.Tools.Declarations, observe.ToolDeclaration{
			ID:           node.ID,
			Name:         request.Tools[i].Name,
			Kind:         firstNonEmpty(request.Tools[i].Kind, "function"),
			Description:  request.Tools[i].Description,
			Schema:       request.Tools[i].Schema,
			NodeID:       node.ID,
			Path:         node.Path,
			ProviderType: node.ProviderType,
		})
	}
	b.obs.Request.Config = requestConfig(request)
}

func requestConfig(request *xir.Request) map[string]any {
	config := make(map[string]any)
	if request.Params.MaxTokens != nil {
		config["max_tokens"] = *request.Params.MaxTokens
	}
	if request.Params.Temperature != nil {
		config["temperature"] = *request.Params.Temperature
	}
	if request.Params.TopP != nil {
		config["top_p"] = *request.Params.TopP
	}
	if request.Params.Stop != nil {
		config["stop"] = request.Params.Stop
	}
	if len(config) == 0 {
		return nil
	}
	return config
}

func (b *nodeBuilder) toolNode(path string, tool *xir.Tool, index int) observe.SemanticNode {
	kind := firstNonEmpty(tool.Kind, "function")
	node := observe.SemanticNode{
		ID:             observe.StableNodeID("request", path, kind, index),
		ProviderType:   kind,
		NormalizedType: observe.NodeToolDeclaration,
		Path:           path,
		Index:          index,
		Text:           tool.Name,
		Raw:            cloneJSON(tool.Schema),
		Metadata:       map[string]any{"name": tool.Name, "description": tool.Description},
	}
	return node
}

func (b *nodeBuilder) response(response *xir.Response) {
	if response.Error != nil {
		node := observe.SemanticNode{
			ID:             observe.StableNodeID("response", "$.error", "error", 0),
			ProviderType:   "error",
			NormalizedType: observe.NodeError,
			Path:           "$.error",
			Text:           response.Error.Message,
			Raw:            cloneJSON(response.Error.Raw),
		}
		b.obs.Response.Errors = append(b.obs.Response.Errors, node)
		b.obs.Response.Nodes = append(b.obs.Response.Nodes, node)
		return
	}
	path := "$.response"
	message := observe.SemanticNode{
		ID:             observe.StableNodeID("response", path, "message", 0),
		ProviderType:   "message",
		NormalizedType: observe.NodeMessage,
		Role:           "assistant",
		Path:           path,
	}
	for i := range response.Parts {
		part := &response.Parts[i]
		partPath := fmt.Sprintf("%s.parts[%d]", path, i)
		node := b.partNode("response", partPath, *part, xir.RoleAssistant, true)
		message.Children = append(message.Children, node)
		switch node.NormalizedType {
		case observe.NodeToolCall:
			b.obs.Response.ToolCalls = append(b.obs.Response.ToolCalls, node)
		case observe.NodeToolResult:
			b.obs.Response.ToolResults = append(b.obs.Response.ToolResults, node)
		case observe.NodeReasoning:
			b.obs.Response.Reasoning = append(b.obs.Response.Reasoning, node)
		case observe.NodeRefusal:
			b.obs.Response.Refusals = append(b.obs.Response.Refusals, node)
			b.safety.Refused = true
		}
	}
	if len(message.Children) > 0 {
		b.obs.Response.Outputs = append(b.obs.Response.Outputs, message)
		b.obs.Response.Nodes = append(b.obs.Response.Nodes, message)
	}
	if response.StopReason == xir.FinishError {
		b.obs.Response.Errors = append(b.obs.Response.Errors, observe.SemanticNode{
			ID: observe.StableNodeID("response", "$.stop_reason", "stop_reason", 0), ProviderType: "stop_reason",
			NormalizedType: observe.NodeError, Path: "$.stop_reason", Text: response.StopDetail,
		})
	}
}

func (b *nodeBuilder) partNode(section, path string, part xir.Part, role xir.Role, aggregate bool) observe.SemanticNode {
	normalized, providerType := normalizedPart(part)
	node := observe.SemanticNode{
		ID:             observe.StableNodeID(section, path, providerType, 0),
		ProviderType:   providerType,
		NormalizedType: normalized,
		Role:           string(role),
		Path:           path,
	}
	switch {
	case part.Reasoning != nil:
		node.Text = part.Reasoning.Text
		if node.Text == "" && len(part.Reasoning.Summary) > 0 {
			node.Text = strings.Join(part.Reasoning.Summary, "\n")
		}
		if part.Reasoning.Sealed != nil {
			node.Raw = cloneJSON(part.Reasoning.Sealed.Raw)
		}
	case part.ToolCall != nil:
		node.Metadata = map[string]any{"name": part.ToolCall.Name}
		if part.ToolCall.ArgsText != "" {
			node.Metadata["arguments"] = part.ToolCall.ArgsText
		}
		if part.ToolCall.ID != "" {
			node.Metadata["id"] = part.ToolCall.ID
		}
		node.Raw = cloneJSON(part.ToolCall.Args)
		if aggregate {
			b.obs.Tools.Calls = append(b.obs.Tools.Calls, observe.ToolCallObservation{
				ID:       part.ToolCall.ID,
				Name:     part.ToolCall.Name,
				Kind:     firstNonEmpty(part.ToolCall.Type, "function"),
				Owner:    observe.ToolOwnerModelRequested,
				ArgsText: part.ToolCall.ArgsText,
				ArgsJSON: cloneJSON(part.ToolCall.Args),
				NodeID:   node.ID,
				Path:     path,
			})
		}
	case part.ToolResult != nil:
		node.Text = part.ToolResult.Text
		if node.Text == "" {
			node.Text = string(part.ToolResult.JSON)
		}
		node.Metadata = map[string]any{"name": part.ToolResult.Name}
		if part.ToolResult.CallID != "" {
			node.Metadata["id"] = part.ToolResult.CallID
		}
		node.Raw = cloneJSON(part.ToolResult.JSON)
		for j := range part.ToolResult.Content {
			child := b.partNode(section, fmt.Sprintf("%s.content[%d]", path, j), part.ToolResult.Content[j], role, aggregate)
			node.Children = append(node.Children, child)
			if child.Text != "" {
				if node.Text != "" {
					node.Text += "\n"
				}
				node.Text += child.Text
			}
		}
		if aggregate {
			b.obs.Tools.Results = append(b.obs.Tools.Results, observe.ToolResultObservation{
				ID:      part.ToolResult.CallID,
				Name:    part.ToolResult.Name,
				Kind:    "function",
				Owner:   observe.ToolOwnerClientExecuted,
				Text:    node.Text,
				JSON:    cloneJSON(part.ToolResult.JSON),
				NodeID:  node.ID,
				Path:    path,
				IsError: false,
			})
		}
	case part.Media != nil:
		node.Metadata = map[string]any{"media_kind": string(part.Media.Kind)}
		if part.Media.URL != "" {
			node.Metadata["url"] = part.Media.URL
		}
		if part.Media.MediaType != "" {
			node.Metadata["media_type"] = part.Media.MediaType
		}
		if part.Media.Filename != "" {
			node.Metadata["filename"] = part.Media.Filename
		}
	default:
		node.Text = part.Text
	}
	if part.Sealed != nil && node.Raw == nil {
		node.Raw = cloneJSON(part.Sealed.Raw)
	}
	return node
}

func normalizedPart(part xir.Part) (observe.NormalizedType, string) {
	switch part.Kind {
	case xir.PartText:
		return observe.NodeText, "text"
	case xir.PartReasoning:
		return observe.NodeReasoning, "reasoning"
	case xir.PartRefusal:
		return observe.NodeRefusal, "refusal"
	case xir.PartToolCall:
		return observe.NodeToolCall, "tool_call"
	case xir.PartToolResult:
		return observe.NodeToolResult, "tool_result"
	case xir.PartImage:
		return observe.NodeImage, "image"
	case xir.PartFile:
		return observe.NodeFile, "file"
	case xir.PartCitation:
		return observe.NodeCitation, "citation"
	case xir.PartSealed:
		return observe.NodeUnknown, "sealed"
	case xir.PartSearch:
		return observe.NodeUnknown, "search"
	case xir.PartUnknown:
		return observe.NodeUnknown, firstNonEmpty("unknown", string(part.Kind))
	default:
		return observe.NodeUnknown, string(part.Kind)
	}
}

func cloneJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
