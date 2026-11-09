// Package chat implements the OpenAI Chat Completions codec for the Exchange
// IR: a tolerant semantic decode from the wire document and a normalized
// re-encode.
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// Codec implements xir.Codec for the OpenAI Chat Completions wire protocol.
// It holds no state, so one value can serve any number of concurrent
// exchanges.
type Codec struct{}

var _ xir.Codec = (*Codec)(nil)

// New returns a Chat Completions codec. The concrete type is returned so
// callers can keep helpers near it; it satisfies xir.Codec.
func New() *Codec { return &Codec{} }

// Protocol reports the protocol this codec speaks.
func (c *Codec) Protocol() xir.Protocol { return xir.ProtocolChatCompletions }

// DecodeRequest projects a Chat Completions request document onto the
// semantic IR. Unknown fields are ignored and missing optional fields are
// left zero; only structural mismatches (a non-object root, a non-array
// messages/tools array) are errors.
//
// System and developer messages stay in Request.Messages: role and ordering
// are chat facts and the separate Request.System field cannot carry both
// roles. A caller converting to a protocol with a dedicated system slot can
// lift them itself.
func (c *Codec) DecodeRequest(ctx context.Context, doc *xir.Document, opts xir.DecodeOptions) (*xir.Request, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if doc == nil || doc.Root == nil {
		return nil, fmt.Errorf("chat: nil request document")
	}
	root := doc.Root
	if root.Kind != wire.Object {
		return nil, fmt.Errorf("chat: request root is %s, want object", kindName(root.Kind))
	}

	req := &xir.Request{
		Model:  fieldString(root, "model"),
		Params: decodeParams(root),
		State:  decodeState(root),
		Ref:    root,
	}
	if stream, ok := root.BoolField("stream"); ok {
		req.Stream = stream
	}
	if msgs, ok := root.Get("messages"); ok && msgs != nil && msgs.Kind != wire.Null {
		messages, err := decodeMessages(msgs)
		if err != nil {
			return nil, err
		}
		req.Messages = messages
	}
	if tools, ok := root.Get("tools"); ok && tools != nil && tools.Kind != wire.Null {
		decoded, err := decodeTools(tools)
		if err != nil {
			return nil, err
		}
		req.Tools = decoded
	}
	// Legacy functions[] is only consulted when the modern tools[] is absent,
	// so a request declaring both cannot double its tool set.
	if len(req.Tools) == 0 {
		if fns, ok := root.Get("functions"); ok && fns != nil && fns.Kind != wire.Null {
			decoded, err := decodeFunctions(fns)
			if err != nil {
				return nil, err
			}
			req.Tools = decoded
		}
	}
	if choice, ok := root.Get("tool_choice"); ok && choice != nil && choice.Kind != wire.Null {
		req.ToolChoice = decodeToolChoice(choice)
	}
	return req, nil
}

// DecodeResponse projects a Chat Completions response document onto the
// semantic IR. An {"error":{...}} envelope produces a Response whose Error is
// set and whose Parts are empty; otherwise choices[0] is decoded and any
// further choices are ignored.
func (c *Codec) DecodeResponse(ctx context.Context, doc *xir.Document, opts xir.DecodeOptions) (*xir.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if doc == nil || doc.Root == nil {
		return nil, fmt.Errorf("chat: nil response document")
	}
	root := doc.Root
	if root.Kind != wire.Object {
		return nil, fmt.Errorf("chat: response root is %s, want object", kindName(root.Kind))
	}

	if errNode, ok := root.Get("error"); ok && errNode != nil && errNode.Kind != wire.Null {
		decodeErr, err := decodeError(errNode)
		if err != nil {
			return nil, err
		}
		return &xir.Response{Error: decodeErr, Ref: root}, nil
	}

	choices, ok := root.Get("choices")
	if !ok || choices == nil || choices.Kind != wire.Array {
		return nil, fmt.Errorf("chat: response has no choices array")
	}
	if len(choices.Items) == 0 {
		return nil, fmt.Errorf("chat: response choices array is empty")
	}
	choice := choices.Items[0]
	if choice == nil || choice.Kind != wire.Object {
		return nil, fmt.Errorf("chat: response choice is not an object")
	}

	resp := &xir.Response{
		ID:    fieldString(root, "id"),
		Model: fieldString(root, "model"),
		Ref:   root,
	}
	if reason, ok := choice.StringField("finish_reason"); ok {
		resp.StopReason = decodeFinishReason(reason)
	}
	if msg, ok := choice.Get("message"); ok && msg != nil && msg.Kind == wire.Object {
		decodeResponseMessage(resp, msg)
	} else if msg != nil && msg.Kind != wire.Null {
		return nil, fmt.Errorf("chat: response choice message is not an object")
	}
	if u, ok := root.Get("usage"); ok && u != nil && u.Kind == wire.Object {
		resp.Usage = decodeUsage(u)
	}
	return resp, nil
}

func decodeMessages(n *wire.Node) ([]xir.Message, error) {
	if n.Kind != wire.Array {
		return nil, fmt.Errorf("chat: messages is %s, want array", kindName(n.Kind))
	}
	out := make([]xir.Message, 0, len(n.Items))
	for _, item := range n.Items {
		if item == nil || item.Kind != wire.Object {
			continue
		}
		out = append(out, decodeMessage(item))
	}
	return out, nil
}

func decodeMessage(n *wire.Node) xir.Message {
	role := strings.ToLower(strings.TrimSpace(fieldString(n, "role")))
	msg := xir.Message{
		Role: normalizeRole(role),
		Name: fieldString(n, "name"),
		Ref:  n,
	}

	// Legacy role=function and modern role=tool both answer a call.
	if role == "tool" || role == "function" {
		msg.Parts = []xir.Part{decodeToolResultPart(n)}
		return msg
	}

	if content, ok := n.Get("content"); ok && content != nil && content.Kind != wire.Null {
		msg.Parts = append(msg.Parts, decodeContentParts(content)...)
	}
	if calls, ok := n.Get("tool_calls"); ok && calls != nil && calls.Kind == wire.Array {
		msg.Parts = append(msg.Parts, decodeToolCalls(calls)...)
	}
	if fn, ok := n.Get("function_call"); ok && fn != nil && fn.Kind == wire.Object {
		msg.Parts = append(msg.Parts, xir.Part{
			Kind:     xir.PartToolCall,
			ToolCall: decodeLegacyFunctionCall(fn),
			Ref:      fn,
		})
	}
	// reasoning_content is the de-facto DeepSeek/OpenRouter assistant field;
	// plain reasoning is accepted when it is a string.
	for _, key := range []string{"reasoning_content", "reasoning"} {
		if v, ok := n.Get(key); ok && v != nil && v.Kind == wire.String && v.Str != "" {
			msg.Parts = append(msg.Parts, xir.Part{
				Kind:      xir.PartReasoning,
				Reasoning: &xir.Reasoning{Text: v.Str},
				Ref:       v,
			})
			break
		}
	}
	return msg
}

func normalizeRole(role string) xir.Role {
	switch role {
	case "system":
		return xir.RoleSystem
	case "developer":
		return xir.RoleDeveloper
	case "user":
		return xir.RoleUser
	case "assistant":
		return xir.RoleAssistant
	case "tool", "function":
		return xir.RoleTool
	default:
		return xir.RoleUnknown
	}
}

func decodeToolResultPart(n *wire.Node) xir.Part {
	tr := &xir.ToolResult{
		CallID: fieldString(n, "tool_call_id"),
		Name:   fieldString(n, "name"),
		Ref:    n,
	}
	part := xir.Part{
		Kind:       xir.PartToolResult,
		ToolResult: tr,
		Ref:        n,
	}
	content, ok := n.Get("content")
	if !ok || content == nil || content.Kind == wire.Null {
		return part
	}
	part.Ref = content
	switch content.Kind {
	case wire.String:
		tr.Text = content.Str
	case wire.Array:
		tr.Content = decodeContentParts(content)
	}
	return part
}

// decodeContentParts decodes the two chat content shapes: a bare string or an
// ordered array of typed parts. Unrecognized array members are skipped rather
// than failing the whole request.
func decodeContentParts(n *wire.Node) []xir.Part {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case wire.String:
		return []xir.Part{{Kind: xir.PartText, Text: n.Str, Ref: n}}
	case wire.Array:
		out := make([]xir.Part, 0, len(n.Items))
		for _, item := range n.Items {
			if item == nil || item.Kind != wire.Object {
				continue
			}
			out = append(out, decodeContentPart(item))
		}
		return out
	default:
		return nil
	}
}

func decodeContentPart(n *wire.Node) xir.Part {
	typ := strings.ToLower(fieldString(n, "type"))
	switch typ {
	case "text", "input_text", "output_text":
		return xir.Part{Kind: xir.PartText, Text: fieldString(n, "text"), Ref: n}
	case "image_url", "image":
		return xir.Part{Kind: xir.PartImage, Media: decodeImageMedia(n), Ref: n}
	case "input_audio", "audio":
		return xir.Part{Kind: xir.PartAudio, Media: decodeAudioMedia(n), Ref: n}
	case "file":
		return xir.Part{Kind: xir.PartFile, Media: decodeFileMedia(n), Ref: n}
	case "refusal":
		text := fieldString(n, "refusal")
		if text == "" {
			text = fieldString(n, "text")
		}
		return xir.Part{Kind: xir.PartRefusal, Text: text, Ref: n}
	case "reasoning", "thinking":
		return xir.Part{Kind: xir.PartReasoning, Reasoning: &xir.Reasoning{Text: fieldString(n, "text")}, Ref: n}
	default:
		part := xir.Part{Kind: xir.PartUnknown, Ref: n}
		part.Text = fieldString(n, "text")
		return part
	}
}

func decodeImageMedia(n *wire.Node) *xir.Media {
	media := &xir.Media{Kind: xir.MediaImage}
	if v, ok := n.Get("image_url"); ok && v != nil {
		switch v.Kind {
		case wire.String:
			media.URL = v.Str
		case wire.Object:
			media.URL = fieldString(v, "url")
			media.Detail = fieldString(v, "detail")
		}
	}
	if media.URL == "" {
		media.URL = fieldString(n, "url")
	}
	if media.Detail == "" {
		media.Detail = fieldString(n, "detail")
	}
	return media
}

func decodeAudioMedia(n *wire.Node) *xir.Media {
	media := &xir.Media{Kind: xir.MediaAudio}
	if v, ok := n.Get("input_audio"); ok && v != nil && v.Kind == wire.Object {
		media.Data = fieldString(v, "data")
		media.MediaType = fieldString(v, "format")
	}
	if media.Data == "" {
		media.Data = fieldString(n, "data")
	}
	if media.MediaType == "" {
		media.MediaType = fieldString(n, "format")
	}
	return media
}

func decodeFileMedia(n *wire.Node) *xir.Media {
	media := &xir.Media{Kind: xir.MediaFile}
	if v, ok := n.Get("file"); ok && v != nil && v.Kind == wire.Object {
		// file_id is an opaque server-side reference with no Media field; it is
		// deliberately dropped instead of being mistaken for a URL.
		media.Data = fieldString(v, "file_data")
		media.Filename = fieldString(v, "filename")
		media.MediaType = fieldString(v, "media_type")
	}
	if media.Data == "" {
		media.Data = fieldString(n, "file_data")
	}
	if media.Filename == "" {
		media.Filename = fieldString(n, "filename")
	}
	return media
}

func decodeToolCalls(arr *wire.Node) []xir.Part {
	out := make([]xir.Part, 0, len(arr.Items))
	for _, item := range arr.Items {
		if item == nil || item.Kind != wire.Object {
			continue
		}
		out = append(out, xir.Part{
			Kind:     xir.PartToolCall,
			ToolCall: decodeToolCall(item),
			Ref:      item,
		})
	}
	return out
}

func decodeToolCall(n *wire.Node) *xir.ToolCall {
	tc := &xir.ToolCall{
		ID:   fieldString(n, "id"),
		Type: fieldString(n, "type"),
		Ref:  n,
	}
	if fn, ok := n.Get("function"); ok && fn != nil && fn.Kind == wire.Object {
		tc.Name = fieldString(fn, "name")
		tc.ArgsText = fieldString(fn, "arguments")
	} else {
		// A few compatible gateways flatten function into the call object.
		tc.Name = fieldString(n, "name")
		tc.ArgsText = fieldString(n, "arguments")
	}
	setArgs(tc)
	return tc
}

func decodeLegacyFunctionCall(n *wire.Node) *xir.ToolCall {
	tc := &xir.ToolCall{
		Type:     "function",
		Ref:      n,
		Name:     fieldString(n, "name"),
		ArgsText: fieldString(n, "arguments"),
	}
	setArgs(tc)
	return tc
}

func setArgs(tc *xir.ToolCall) {
	if tc.ArgsText == "" || !json.Valid([]byte(tc.ArgsText)) {
		return
	}
	tc.Args = json.RawMessage(tc.ArgsText)
}

func decodeTools(n *wire.Node) ([]xir.Tool, error) {
	if n.Kind != wire.Array {
		return nil, fmt.Errorf("chat: tools is %s, want array", kindName(n.Kind))
	}
	out := make([]xir.Tool, 0, len(n.Items))
	for _, item := range n.Items {
		if item == nil || item.Kind != wire.Object {
			continue
		}
		out = append(out, decodeTool(item))
	}
	return out, nil
}

func decodeTool(n *wire.Node) xir.Tool {
	tool := xir.Tool{
		Kind: strings.ToLower(fieldString(n, "type")),
		Ref:  n,
	}
	if tool.Kind == "" {
		tool.Kind = "function"
	}
	if fn, ok := n.Get("function"); ok && fn != nil && fn.Kind == wire.Object {
		// The function object carries the declaration; pointing Ref at it lets
		// a rewrite target only the schema/description.
		tool.Ref = fn
		fillTool(&tool, fn)
		return tool
	}
	fillTool(&tool, n)
	return tool
}

func fillTool(tool *xir.Tool, n *wire.Node) {
	tool.Name = fieldString(n, "name")
	tool.Description = fieldString(n, "description")
	if schema, ok := n.Get("parameters"); ok && schema != nil && schema.Kind != wire.Null {
		tool.Schema = schema.Raw()
	}
	if strict, ok := n.BoolField("strict"); ok {
		tool.Strict = &strict
	}
}

func decodeFunctions(n *wire.Node) ([]xir.Tool, error) {
	if n.Kind != wire.Array {
		return nil, fmt.Errorf("chat: functions is %s, want array", kindName(n.Kind))
	}
	out := make([]xir.Tool, 0, len(n.Items))
	for _, item := range n.Items {
		if item == nil || item.Kind != wire.Object {
			continue
		}
		tool := xir.Tool{Kind: "function", Ref: item}
		fillTool(&tool, item)
		out = append(out, tool)
	}
	return out, nil
}

func decodeToolChoice(n *wire.Node) xir.ToolChoice {
	switch n.Kind {
	case wire.String:
		return xir.ToolChoice{Mode: n.Str}
	case wire.Object:
		typ := strings.ToLower(fieldString(n, "type"))
		switch typ {
		case "auto", "none", "required":
			return xir.ToolChoice{Mode: typ}
		case "function":
			if fn, ok := n.Get("function"); ok && fn != nil && fn.Kind == wire.Object {
				if name := fieldString(fn, "name"); name != "" {
					return xir.ToolChoice{Mode: "tool", Name: name}
				}
			}
		}
	}
	return xir.ToolChoice{}
}

func decodeParams(root *wire.Node) xir.Params {
	var p xir.Params
	p.Temperature = floatFieldPtr(root, "temperature")
	p.TopP = floatFieldPtr(root, "top_p")
	p.TopK = intFieldPtr(root, "top_k")
	p.MaxTokens = intFieldPtr(root, "max_tokens")
	p.MaxCompletion = intFieldPtr(root, "max_completion_tokens")
	p.Seed = int64FieldPtr(root, "seed")
	p.PresencePenalty = floatFieldPtr(root, "presence_penalty")
	p.FrequencyPenalty = floatFieldPtr(root, "frequency_penalty")
	p.Logprobs = boolFieldPtr(root, "logprobs")
	p.TopLogprobs = intFieldPtr(root, "top_logprobs")
	p.ParallelTools = boolFieldPtr(root, "parallel_tool_calls")
	p.ReasoningEffort = fieldString(root, "reasoning_effort")
	p.User = fieldString(root, "user")

	if v, ok := root.Get("stop"); ok && v != nil && v.Kind != wire.Null {
		p.Stop = decodeStop(v)
	}
	if v, ok := root.Get("response_format"); ok && v != nil && v.Kind != wire.Null {
		p.ResponseFormat = v.Raw()
	}
	if v, ok := root.Get("metadata"); ok && v != nil && v.Kind != wire.Null && v.Kind == wire.Object {
		p.Metadata = v.Raw()
	}
	// Top-level reasoning is not part of the semantic parameter set; keep the
	// raw value so a same-protocol re-encode can carry it back.
	if v, ok := root.Get("reasoning"); ok && v != nil && v.Kind != wire.Null {
		if p.Extra == nil {
			p.Extra = map[string]json.RawMessage{}
		}
		p.Extra["reasoning"] = v.Raw()
	}
	return p
}

func decodeStop(n *wire.Node) []string {
	switch n.Kind {
	case wire.String:
		return []string{n.Str}
	case wire.Array:
		out := make([]string, 0, len(n.Items))
		for _, item := range n.Items {
			if item == nil || item.Kind != wire.String {
				continue
			}
			out = append(out, item.Str)
		}
		return out
	default:
		return nil
	}
}

func decodeState(root *wire.Node) xir.StateRefs {
	var state xir.StateRefs
	state.PreviousResponseID = fieldString(root, "previous_response_id")
	state.PromptCacheKey = fieldString(root, "prompt_cache_key")
	// metadata.session_id is how OpenAI-compatible clients hint soft affinity
	// without prompt_cache_key. It is only promoted when the explicit field is
	// absent; the raw metadata object is preserved separately.
	if state.PromptCacheKey == "" {
		if md, ok := root.Get("metadata"); ok && md != nil && md.Kind == wire.Object {
			state.PromptCacheKey = fieldString(md, "session_id")
		}
	}
	if store, ok := root.BoolField("store"); ok {
		state.Store = &store
	}
	return state
}

func decodeUsage(n *wire.Node) *usage.Usage {
	u := &usage.Usage{Source: "chat"}
	if v, ok := n.IntField("prompt_tokens"); ok {
		u.InputTokens = int(v)
	}
	if v, ok := n.IntField("completion_tokens"); ok {
		u.OutputTokens = int(v)
	}
	if v, ok := n.IntField("total_tokens"); ok {
		u.TotalTokens = int(v)
	}
	u.CacheReadTokens = intAt(n, "prompt_tokens_details", "cached_tokens")
	if u.CacheReadTokens == 0 {
		// DeepSeek-compatible providers split hits and misses instead.
		u.CacheReadTokens = intField(n, "prompt_cache_hit_tokens")
	}
	u.ReasoningTokens = intAt(n, "completion_tokens_details", "reasoning_tokens")
	normalized := u.Normalize()
	return &normalized
}

func decodeError(n *wire.Node) (*xir.Error, error) {
	switch n.Kind {
	case wire.Object:
		e := &xir.Error{
			Type:    fieldString(n, "type"),
			Message: fieldString(n, "message"),
			Param:   fieldString(n, "param"),
			Code:    scalarString(n, "code"),
			Raw:     n.Raw(),
		}
		return e, nil
	case wire.String:
		return &xir.Error{Message: n.Str, Raw: n.Raw()}, nil
	default:
		return nil, fmt.Errorf("chat: error envelope is %s, want object", kindName(n.Kind))
	}
}

func decodeResponseMessage(resp *xir.Response, msg *wire.Node) {
	if content, ok := msg.Get("content"); ok && content != nil && content.Kind != wire.Null {
		resp.Parts = append(resp.Parts, decodeContentParts(content)...)
	}
	if calls, ok := msg.Get("tool_calls"); ok && calls != nil && calls.Kind == wire.Array {
		resp.Parts = append(resp.Parts, decodeToolCalls(calls)...)
	}
	if fn, ok := msg.Get("function_call"); ok && fn != nil && fn.Kind == wire.Object {
		resp.Parts = append(resp.Parts, xir.Part{
			Kind:     xir.PartToolCall,
			ToolCall: decodeLegacyFunctionCall(fn),
			Ref:      fn,
		})
	}
	if refusal, ok := msg.StringField("refusal"); ok && refusal != "" {
		resp.Parts = append(resp.Parts, xir.Part{Kind: xir.PartRefusal, Text: refusal, Ref: msg})
	}
	for _, key := range []string{"reasoning_content", "reasoning"} {
		if v, ok := msg.Get(key); ok && v != nil && v.Kind == wire.String && v.Str != "" {
			resp.Parts = append(resp.Parts, xir.Part{
				Kind:      xir.PartReasoning,
				Reasoning: &xir.Reasoning{Text: v.Str},
				Ref:       v,
			})
			break
		}
	}
}

func decodeFinishReason(reason string) xir.FinishReason {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "stop":
		return xir.FinishStop
	case "length":
		return xir.FinishLength
	case "tool_calls", "function_call":
		return xir.FinishToolUse
	case "content_filter":
		return xir.FinishContentFilter
	case "refusal":
		return xir.FinishRefusal
	case "error":
		return xir.FinishError
	case "":
		return ""
	default:
		return xir.FinishUnknown
	}
}

func fieldString(n *wire.Node, key string) string {
	v, _ := n.StringField(key)
	return v
}

func scalarString(n *wire.Node, key string) string {
	v, ok := n.Get(key)
	if !ok || v == nil {
		return ""
	}
	switch v.Kind {
	case wire.String:
		return v.Str
	case wire.Number:
		return string(v.Raw())
	default:
		return ""
	}
}

func intField(n *wire.Node, key string) int {
	v, ok := n.IntField(key)
	if !ok {
		return 0
	}
	return int(v)
}

func intAt(n *wire.Node, key, sub string) int {
	v, ok := n.Get(key)
	if !ok || v == nil || v.Kind != wire.Object {
		return 0
	}
	return intField(v, sub)
}

func intFieldPtr(n *wire.Node, key string) *int {
	v, ok := n.IntField(key)
	if !ok {
		return nil
	}
	i := int(v)
	return &i
}

func int64FieldPtr(n *wire.Node, key string) *int64 {
	v, ok := n.IntField(key)
	if !ok {
		return nil
	}
	return &v
}

func floatFieldPtr(n *wire.Node, key string) *float64 {
	v, ok := n.Get(key)
	if !ok || v == nil {
		return nil
	}
	f, ok := v.Float()
	if !ok {
		return nil
	}
	return &f
}

func boolFieldPtr(n *wire.Node, key string) *bool {
	b, ok := n.BoolField(key)
	if !ok {
		return nil
	}
	return &b
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
		return "bool"
	case wire.Null:
		return "null"
	default:
		return "unknown"
	}
}
