// Package xirobserve projects an XIR-decoded exchange onto the persisted
// observation IR (observe.TraceObservation). It is the first stage of the
// observation-lane convergence: the stored shape, the detectors and the
// consumers stay unchanged, while the semantic decode comes from the XIR
// codecs instead of the per-protocol observe parsers.
//
// Scope: non-streaming and streamed model exchanges for all four wire
// protocols. Streamed responses are folded into the protocol's non-streaming
// wire shape and decoded by the same codec. Client entry exchanges have no xir
// codec and return ErrUnsupported so the caller falls back to pkg/observe. The
// caller chooses the engine; this package never changes a default by itself.
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
	"github.com/kingfs/Trajecta/pkg/xir/wire"
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
	builder := &nodeBuilder{traceID: input.TraceID, obs: obs, protocol: protocol}

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

	streaming := input.IsStream || input.Header.Layout.IsStream || looksLikeSSE(input.ResponseBody)
	var response *xir.Response
	if len(strings.TrimSpace(string(input.ResponseBody))) > 0 {
		wireBody := input.ResponseBody
		var streamSection observe.ObservationStream
		if streaming {
			wireBody, streamSection, err = assembleStream(protocol, input.ResponseBody)
			if err != nil {
				return obs, fmt.Errorf("xirobserve: assemble %s stream: %w", protocol, err)
			}
			builder.obs.Stream = streamSection
		}
		respDoc, err := xir.ParseDocument(protocol, operation, wireBody)
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

// unsupported reports the exchanges left for the fallback engine: client entry
// exchanges have no xir codec (they are a harness envelope, not a model
// protocol), and operations outside the four model protocols are unsupported.
// Streamed model responses are assembled from SSE for all four protocols.
func unsupported(input observe.ParseInput) bool {
	if input.ExchangeKind == "entry" || input.Header.Meta.ExchangeKind == "entry" {
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
	traceID  string
	protocol xir.Protocol
	obs      observe.TraceObservation
	safety   observe.ObservationSafety
}

func (b *nodeBuilder) request(request *xir.Request, operation xir.Operation) {
	paths := indexWirePaths(request.Ref)
	for i := range request.System {
		part := request.System[i]
		path := wireNodePath(paths, part.Ref, fmt.Sprintf("$.instructions[%d]", i))
		node := b.partNode("instruction", path, part, xir.RoleSystem, false, paths)
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
		fallbackPath := fmt.Sprintf("$.messages[%d]", i)
		if operation == xir.OperationResponses {
			fallbackPath = fmt.Sprintf("$.input[%d]", i)
		}
		path := wireNodePath(paths, message.Ref, fallbackPath)

		// Responses input items such as function_call and function_call_output
		// are semantic nodes in their own right, not messages with a synthetic
		// `parts` child. Keep them at the path where the client recorded them.
		if operation == xir.OperationResponses && len(message.Parts) == 1 &&
			message.Ref != nil && messagePartRef(message.Parts[0]) == message.Ref {
			node := b.partNode("request", path, message.Parts[0], message.Role, false, paths)
			node.Index = i
			b.obs.Request.Inputs = append(b.obs.Request.Inputs, node)
			continue
		}

		// Chat Completions represents a tool answer as a tool-role message.
		// Normalize that message itself as the result, so it can be correlated
		// with the call from an earlier assistant message and merged into that
		// call's conversation step.
		if resultPart := messageToolResultPart(message); resultPart != nil {
			node := b.toolResultMessageNode(path, i, message, resultPart, paths)
			if operation == xir.OperationResponses {
				b.obs.Request.Inputs = append(b.obs.Request.Inputs, node)
			} else {
				b.obs.Request.Messages = append(b.obs.Request.Messages, node)
			}
			continue
		}

		node := observe.SemanticNode{
			ID:             observe.StableNodeID("request", path, "message", i),
			ProviderType:   "message",
			NormalizedType: observe.NodeMessage,
			Role:           string(message.Role),
			Path:           path,
			Index:          i,
			Raw:            rawWireNode(message.Ref),
		}
		for j := range message.Parts {
			part := message.Parts[j]
			partPath := wireNodePath(paths, messagePartRef(part), fmt.Sprintf("%s.parts[%d]", path, j))
			node.Children = append(node.Children, b.partNode("request", partPath, part, message.Role, false, paths))
		}
		node.Text = firstTextNodeText(node.Children)
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
		tool := &request.Tools[i]
		path := wireNodePath(paths, tool.Ref, fmt.Sprintf("$.tools[%d]", i))
		node := b.toolNode(path, tool, i)
		b.obs.Request.Tools = append(b.obs.Request.Tools, node)
		b.obs.Request.Nodes = append(b.obs.Request.Nodes, node)
		b.obs.Tools.Declarations = append(b.obs.Tools.Declarations, observe.ToolDeclaration{
			ID:           node.ID,
			Name:         tool.Name,
			Kind:         firstNonEmpty(tool.Kind, "function"),
			Description:  tool.Description,
			Schema:       tool.Schema,
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
	return observe.SemanticNode{
		ID:             observe.StableNodeID("request", path, kind, index),
		ProviderType:   kind,
		NormalizedType: observe.NodeToolDeclaration,
		Path:           path,
		Index:          index,
		Text:           tool.Name,
		Raw:            rawWireNode(tool.Ref),
		Metadata:       map[string]any{"name": tool.Name, "description": tool.Description},
	}
}

func (b *nodeBuilder) response(response *xir.Response) {
	paths := indexWirePaths(response.Ref)
	if response.Error != nil {
		errorRef, _ := response.Ref.Get("error")
		path := wireNodePath(paths, errorRef, "$.error")
		raw := rawWireNode(errorRef)
		if raw == nil {
			raw = cloneJSON(response.Error.Raw)
		}
		node := observe.SemanticNode{
			ID:             observe.StableNodeID("response", path, "error", 0),
			ProviderType:   "error",
			NormalizedType: observe.NodeError,
			Path:           path,
			Text:           response.Error.Message,
			Raw:            raw,
		}
		b.obs.Response.Errors = append(b.obs.Response.Errors, node)
		b.obs.Response.Nodes = append(b.obs.Response.Nodes, node)
		return
	}

	switch b.protocol {
	case xir.ProtocolChatCompletions:
		if b.chatResponse(response, paths) {
			b.appendStopReasonError(response)
			return
		}
	case xir.ProtocolResponses:
		if b.responsesResponse(response, paths) {
			b.appendStopReasonError(response)
			return
		}
	case xir.ProtocolGemini:
		if b.geminiResponse(response, paths) {
			b.appendStopReasonError(response)
			return
		}
	}

	// Anthropic-style responses are a flat list of content blocks. Their wire
	// references already identify the provider paths, so expose those blocks as
	// roots rather than inventing a synthetic $.response.parts tree.
	for i := range response.Parts {
		part := response.Parts[i]
		path := wireNodePath(paths, messagePartRef(part), fmt.Sprintf("$.response.parts[%d]", i))
		node := b.partNode("response", path, part, xir.RoleAssistant, true, paths)
		b.appendResponseNode(node, true)
	}
	b.appendStopReasonError(response)
}

func (b *nodeBuilder) chatResponse(response *xir.Response, paths map[*wire.Node]string) bool {
	if response.Ref == nil {
		return false
	}
	choices, ok := response.Ref.Get("choices")
	if !ok || choices == nil || choices.Kind != wire.Array {
		return false
	}

	decoded := make([]*xir.Response, 1, 1+len(response.Alternatives))
	decoded[0] = response
	decoded = append(decoded, response.Alternatives...)
	for i, choiceRef := range choices.Items {
		if choiceRef == nil || choiceRef.Kind != wire.Object || i >= len(decoded) {
			continue
		}
		choicePath := wireNodePath(paths, choiceRef, fmt.Sprintf("$.choices[%d]", i))
		choice := observe.SemanticNode{
			ID:             observe.StableNodeID("response", choicePath, "choice", i),
			ProviderType:   "choice",
			NormalizedType: observe.NodeMessage,
			Path:           choicePath,
			Index:          i,
			Raw:            rawWireNode(choiceRef),
			Metadata:       map[string]any{"finish_reason": wireStringField(choiceRef, "finish_reason")},
		}
		messageRef, _ := choiceRef.Get("message")
		if messageRef == nil || messageRef.Kind != wire.Object {
			continue
		}
		role := firstNonEmpty(wireStringField(messageRef, "role"), "assistant")
		messagePath := wireNodePath(paths, messageRef, choicePath+".message")
		message := observe.SemanticNode{
			ID:             observe.StableNodeID("response", messagePath, "chat.message", 0),
			ProviderType:   "chat.message",
			NormalizedType: observe.NodeMessage,
			Role:           role,
			Path:           messagePath,
			Raw:            rawWireNode(messageRef),
			Metadata:       map[string]any{"role": role},
		}
		for j := range decoded[i].Parts {
			part := decoded[i].Parts[j]
			partPath := wireNodePath(paths, messagePartRef(part), fmt.Sprintf("%s.parts[%d]", messagePath, j))
			node := b.partNode("response", partPath, part, xir.Role(role), true, paths)
			message.Children = append(message.Children, node)
			b.trackResponsePart(node)
		}
		message.Text = firstTextNodeText(message.Children)
		choice.Children = append(choice.Children, message)
		b.obs.Response.Candidates = append(b.obs.Response.Candidates, choice)
		b.obs.Response.Nodes = append(b.obs.Response.Nodes, choice)
		if wireStringField(choiceRef, "finish_reason") == "content_filter" {
			safety := observe.SemanticNode{
				ID:             observe.StableNodeID("response", choicePath+".finish_reason", "finish_reason", 0),
				ProviderType:   "finish_reason",
				NormalizedType: observe.NodeSafety,
				Path:           choicePath + ".finish_reason",
				Text:           "content_filter",
			}
			b.obs.Response.Safety = append(b.obs.Response.Safety, safety)
			b.obs.Response.Nodes = append(b.obs.Response.Nodes, safety)
			b.safety.Blocked = true
		}
	}
	return true
}

func (b *nodeBuilder) responsesResponse(response *xir.Response, paths map[*wire.Node]string) bool {
	if response.Ref == nil {
		return false
	}
	output, ok := response.Ref.Get("output")
	if !ok || output == nil || output.Kind != wire.Array {
		return false
	}
	for i, itemRef := range output.Items {
		if itemRef == nil {
			continue
		}
		path := wireNodePath(paths, itemRef, fmt.Sprintf("$.output[%d]", i))
		providerType := wireStringField(itemRef, "type")
		role := wireStringField(itemRef, "role")
		if providerType == "" && role != "" {
			providerType = "message"
		}
		if providerType == "message" {
			node := observe.SemanticNode{
				ID:             observe.StableNodeID("response", path, providerType, i),
				ProviderType:   providerType,
				NormalizedType: observe.NodeMessage,
				Role:           firstNonEmpty(role, "assistant"),
				Path:           path,
				Index:          i,
				Raw:            rawWireNode(itemRef),
			}
			for _, part := range partsWithinPath(response.Parts, paths, path) {
				partPath := wireNodePath(paths, messagePartRef(part), path)
				if partPath == path {
					continue
				}
				child := b.partNode("response", partPath, part, xir.Role(node.Role), true, paths)
				node.Children = append(node.Children, child)
				b.trackResponsePart(child)
			}
			node.Text = firstTextNodeText(node.Children)
			if node.Text == "" {
				node.Text = wireStringField(itemRef, "text")
			}
			b.appendResponseNode(node, true)
			continue
		}

		part, found := responsePartAtPath(response.Parts, paths, path)
		if found {
			node := b.partNode("response", path, part, xir.Role(firstNonEmpty(role, "assistant")), true, paths)
			node.Index = i
			b.appendResponseNode(node, true)
			continue
		}

		normalized := responseItemKind(providerType)
		node := observe.SemanticNode{
			ID:             observe.StableNodeID("response", path, providerType, i),
			ProviderType:   firstNonEmpty(providerType, "unknown"),
			NormalizedType: normalized,
			Role:           role,
			Path:           path,
			Index:          i,
			Text:           firstNonEmpty(wireStringField(itemRef, "name"), wireStringField(itemRef, "text"), wireStringField(itemRef, "arguments"), wireStringField(itemRef, "output")),
			Raw:            rawWireNode(itemRef),
		}
		b.appendResponseNode(node, true)
	}
	return true
}

func (b *nodeBuilder) geminiResponse(response *xir.Response, paths map[*wire.Node]string) bool {
	if response.Ref == nil {
		return false
	}
	candidates, ok := response.Ref.Get("candidates")
	if !ok || candidates == nil || candidates.Kind != wire.Array {
		return false
	}
	decoded := make([]*xir.Response, 1, 1+len(response.Alternatives))
	decoded[0] = response
	decoded = append(decoded, response.Alternatives...)
	for i, candidateRef := range candidates.Items {
		if candidateRef == nil || i >= len(decoded) {
			continue
		}
		path := wireNodePath(paths, candidateRef, fmt.Sprintf("$.candidates[%d]", i))
		role := "assistant"
		if content, ok := candidateRef.Get("content"); ok && content != nil {
			role = firstNonEmpty(wireStringField(content, "role"), role)
		}
		candidate := observe.SemanticNode{
			ID:             observe.StableNodeID("response", path, "candidate", i),
			ProviderType:   "candidate",
			NormalizedType: observe.NodeMessage,
			Role:           role,
			Path:           path,
			Index:          i,
			Raw:            rawWireNode(candidateRef),
		}
		for j := range decoded[i].Parts {
			part := decoded[i].Parts[j]
			partPath := wireNodePath(paths, messagePartRef(part), fmt.Sprintf("%s.parts[%d]", path, j))
			node := b.partNode("response", partPath, part, xir.Role(role), true, paths)
			candidate.Children = append(candidate.Children, node)
			b.trackResponsePart(node)
		}
		candidate.Text = firstTextNodeText(candidate.Children)
		b.obs.Response.Candidates = append(b.obs.Response.Candidates, candidate)
		b.obs.Response.Nodes = append(b.obs.Response.Nodes, candidate)
	}
	return true
}

func (b *nodeBuilder) appendResponseNode(node observe.SemanticNode, output bool) {
	if output {
		b.obs.Response.Outputs = append(b.obs.Response.Outputs, node)
	}
	b.obs.Response.Nodes = append(b.obs.Response.Nodes, node)
	b.trackResponsePart(node)
}

func (b *nodeBuilder) trackResponsePart(node observe.SemanticNode) {
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

func (b *nodeBuilder) appendStopReasonError(response *xir.Response) {
	if response.StopReason != xir.FinishError {
		return
	}
	node := observe.SemanticNode{
		ID:             observe.StableNodeID("response", "$.stop_reason", "stop_reason", 0),
		ProviderType:   "stop_reason",
		NormalizedType: observe.NodeError,
		Path:           "$.stop_reason",
		Text:           response.StopDetail,
	}
	b.obs.Response.Errors = append(b.obs.Response.Errors, node)
	b.obs.Response.Nodes = append(b.obs.Response.Nodes, node)
}

func responseItemKind(providerType string) observe.NormalizedType {
	switch providerType {
	case "function_call", "tool_call":
		return observe.NodeToolCall
	case "function_call_output", "tool_result":
		return observe.NodeToolResult
	case "reasoning":
		return observe.NodeReasoning
	case "refusal":
		return observe.NodeRefusal
	case "message":
		return observe.NodeMessage
	case "output_text", "input_text", "text":
		return observe.NodeText
	default:
		return observe.NodeUnknown
	}
}

func responsePartAtPath(parts []xir.Part, paths map[*wire.Node]string, path string) (xir.Part, bool) {
	for _, part := range parts {
		if wireNodePath(paths, messagePartRef(part), "") == path {
			return part, true
		}
	}
	return xir.Part{}, false
}

func partsWithinPath(parts []xir.Part, paths map[*wire.Node]string, parentPath string) []xir.Part {
	out := make([]xir.Part, 0, len(parts))
	for _, part := range parts {
		path := wireNodePath(paths, messagePartRef(part), "")
		if path == parentPath || strings.HasPrefix(path, parentPath+".") || strings.HasPrefix(path, parentPath+"[") {
			out = append(out, part)
		}
	}
	return out
}

func (b *nodeBuilder) toolResultMessageNode(path string, index int, message *xir.Message, part *xir.Part, paths map[*wire.Node]string) observe.SemanticNode {
	result := part.ToolResult
	node := observe.SemanticNode{
		ID:             observe.StableNodeID("request", path, "message", index),
		ProviderType:   "message",
		NormalizedType: observe.NodeToolResult,
		Role:           string(message.Role),
		Path:           path,
		Index:          index,
		Text:           result.Text,
		Raw:            rawWireNode(message.Ref),
		Metadata:       map[string]any{"tool_call_id": result.CallID, "name": result.Name},
	}
	if node.Text == "" {
		node.Text = string(result.JSON)
	}
	for i, childPart := range result.Content {
		childPath := wireNodePath(paths, messagePartRef(childPart), fmt.Sprintf("%s.content[%d]", path, i))
		node.Children = append(node.Children, b.partNode("request", childPath, childPart, message.Role, false, paths))
	}
	if len(result.Content) == 0 && result.Text != "" && result.Ref != nil {
		contentRef, _ := result.Ref.Get("content")
		if contentRef != nil && contentRef.Kind == wire.String {
			contentPath := wireNodePath(paths, contentRef, path+".content")
			node.Children = append(node.Children, observe.SemanticNode{
				ID:             observe.StableNodeID("request", contentPath, "content", 0),
				ProviderType:   "content",
				NormalizedType: observe.NodeText,
				Role:           string(message.Role),
				Path:           contentPath,
				Text:           result.Text,
				Raw:            rawWireNode(contentRef),
			})
		}
	}
	return node
}

func messageToolResultPart(message *xir.Message) *xir.Part {
	if message == nil {
		return nil
	}
	for i := range message.Parts {
		if message.Parts[i].ToolResult != nil && message.Role == xir.RoleTool {
			return &message.Parts[i]
		}
	}
	return nil
}

func firstTextNodeText(nodes []observe.SemanticNode) string {
	for _, node := range nodes {
		if node.NormalizedType == observe.NodeText && node.Text != "" {
			return node.Text
		}
	}
	return ""
}

func messagePartRef(part xir.Part) *wire.Node {
	if part.ToolCall != nil && part.ToolCall.Ref != nil {
		return part.ToolCall.Ref
	}
	if part.ToolResult != nil && part.ToolResult.Ref != nil {
		return part.ToolResult.Ref
	}
	return part.Ref
}

func indexWirePaths(root *wire.Node) map[*wire.Node]string {
	paths := make(map[*wire.Node]string)
	var walk func(*wire.Node, string)
	walk = func(node *wire.Node, path string) {
		if node == nil {
			return
		}
		paths[node] = path
		for _, member := range node.Members {
			walk(member.Value, path+"."+member.Key)
		}
		for i, item := range node.Items {
			walk(item, fmt.Sprintf("%s[%d]", path, i))
		}
	}
	walk(root, "$")
	return paths
}

func wireNodePath(paths map[*wire.Node]string, node *wire.Node, fallback string) string {
	if node != nil {
		if path := paths[node]; path != "" {
			return path
		}
	}
	return fallback
}

func rawWireNode(node *wire.Node) json.RawMessage {
	if node == nil {
		return nil
	}
	return cloneJSON(json.RawMessage(node.Raw()))
}

func wireStringField(node *wire.Node, key string) string {
	value, ok := node.Get(key)
	if !ok || value == nil || value.Kind != wire.String {
		return ""
	}
	return value.Str
}

func (b *nodeBuilder) partNode(section, path string, part xir.Part, role xir.Role, aggregate bool, paths map[*wire.Node]string) observe.SemanticNode {
	normalized, providerType := normalizedPart(part)
	node := observe.SemanticNode{
		ID:             observe.StableNodeID(section, path, providerType, 0),
		ProviderType:   providerType,
		NormalizedType: normalized,
		Role:           string(role),
		Path:           path,
		Raw:            rawWireNode(messagePartRef(part)),
	}
	switch {
	case part.Reasoning != nil:
		node.Text = part.Reasoning.Text
		if node.Text == "" && len(part.Reasoning.Summary) > 0 {
			node.Text = strings.Join(part.Reasoning.Summary, "\n")
		}
		if part.Reasoning.Sealed != nil && node.Raw == nil {
			node.Raw = cloneJSON(part.Reasoning.Sealed.Raw)
		}
	case part.ToolCall != nil:
		node.Text = part.ToolCall.Name
		node.Metadata = map[string]any{"name": part.ToolCall.Name}
		if part.ToolCall.ArgsText != "" {
			node.Metadata["arguments"] = part.ToolCall.ArgsText
		}
		if part.ToolCall.ID != "" {
			node.Metadata["id"] = part.ToolCall.ID
		}
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
		for j := range part.ToolResult.Content {
			childPart := part.ToolResult.Content[j]
			childPath := wireNodePath(paths, messagePartRef(childPart), fmt.Sprintf("%s.content[%d]", path, j))
			child := b.partNode(section, childPath, childPart, role, aggregate, paths)
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
				IsError: part.ToolResult.IsError,
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
