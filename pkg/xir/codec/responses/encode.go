package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// members builds one JSON object. Map keys are sorted by encoding/json, so the
// encoded bytes are deterministic. Raw members are embedded verbatim (without
// HTML escaping) so protocol-native blocks survive a re-encode.
type members map[string]json.RawMessage

func newMembers() members { return members{} }

func (m members) set(key string, value any) {
	raw, err := marshalValueErr(value)
	if err != nil {
		return
	}
	m[key] = raw
}

func (m members) setRaw(key string, raw json.RawMessage) {
	if len(raw) == 0 || !json.Valid(raw) {
		return
	}
	m[key] = raw
}

func (m members) setString(key, value string) {
	if value != "" {
		m.set(key, value)
	}
}

func (m members) setBoolPtr(key string, value *bool) {
	if value != nil {
		m.set(key, *value)
	}
}

func (m members) setIntPtr(key string, value *int) {
	if value != nil {
		m.set(key, *value)
	}
}

func (m members) setFloatPtr(key string, value *float64) {
	if value != nil {
		m.set(key, *value)
	}
}

func (m members) raw() json.RawMessage {
	raw, err := marshalValueErr(map[string]json.RawMessage(m))
	if err != nil {
		return nil
	}
	return raw
}

// mergeExtras copies unmodeled protocol members into m unless a modeled field
// already claimed the key.
func (m members) mergeExtras(raw json.RawMessage) {
	if len(raw) == 0 || !json.Valid(raw) {
		return
	}
	var extras map[string]json.RawMessage
	if err := json.Unmarshal(raw, &extras); err != nil {
		return
	}
	for key, value := range extras {
		if _, exists := m[key]; exists {
			continue
		}
		m[key] = cloneRaw(value)
	}
}

func marshalValue(value any) json.RawMessage {
	raw, err := marshalValueErr(value)
	if err != nil {
		return nil
	}
	return raw
}

func marshalValueErr(value any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage("null"), nil
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func marshalArray(items []json.RawMessage) json.RawMessage {
	if items == nil {
		items = []json.RawMessage{}
	}
	raw, err := marshalValueErr(items)
	if err != nil {
		return json.RawMessage("[]")
	}
	return raw
}

// EncodeRequest renders the semantic request as a Responses API request body.
// Only fields present in the IR (or preserved in Extensions) are emitted.
func (Codec) EncodeRequest(ctx context.Context, req *xir.Request, opts xir.EncodeOptions) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, fmt.Errorf("responses: cannot encode a nil request")
	}
	m := newMembers()

	model := req.Model
	if opts.Model != "" {
		model = opts.Model
	}
	m.setString("model", model)

	instructions, err := encodeInstructions(req.System)
	if err != nil {
		return nil, err
	}
	m.setRaw("instructions", instructions)

	input, err := encodeInput(req.Messages)
	if err != nil {
		return nil, err
	}
	m.setRaw("input", input)

	if len(req.Tools) > 0 {
		tools, err := encodeTools(req.Tools)
		if err != nil {
			return nil, err
		}
		m.setRaw("tools", tools)
	}
	if raw, ok := encodeToolChoice(req.ToolChoice); ok {
		m.setRaw("tool_choice", raw)
	}

	params := req.Params
	if limit := outputTokenLimit(params); limit != nil {
		m.set("max_output_tokens", *limit)
	}
	m.setFloatPtr("temperature", params.Temperature)
	m.setFloatPtr("top_p", params.TopP)
	m.setIntPtr("top_k", params.TopK)
	m.setBoolPtr("parallel_tool_calls", params.ParallelTools)
	if raw := encodeTextConfig(params); len(raw) > 0 {
		m.setRaw("text", raw)
	}
	if raw := encodeReasoningConfig(params); len(raw) > 0 {
		m.setRaw("reasoning", raw)
	}
	if len(params.Metadata) > 0 && json.Valid(params.Metadata) {
		m.setRaw("metadata", params.Metadata)
	}
	m.setString("user", params.User)

	if req.Stream || opts.Stream {
		m.set("stream", true)
	}

	m.setString("previous_response_id", req.State.PreviousResponseID)
	m.setString("conversation", req.State.ConversationID)
	m.setString("prompt_cache_key", req.State.PromptCacheKey)
	m.setBoolPtr("store", req.State.Store)

	m.mergeExtras(req.Extensions[ExtensionKey])

	raw := m.raw()
	if len(raw) == 0 {
		return nil, fmt.Errorf("responses: failed to encode request")
	}
	return raw, nil
}

// EncodeResponse renders the semantic response as a Responses API response
// body with typed output items and usage.
func (Codec) EncodeResponse(ctx context.Context, resp *xir.Response, opts xir.EncodeOptions) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("responses: cannot encode a nil response")
	}
	m := newMembers()
	m.set("object", "response")
	m.setString("id", resp.ID)

	model := resp.Model
	if opts.Model != "" {
		model = opts.Model
	}
	m.setString("model", model)

	status, incomplete := responseStatus(resp)
	m.setString("status", status)

	output, err := encodeOutput(resp.Parts)
	if err != nil {
		return nil, err
	}
	m.setRaw("output", output)

	if raw := encodeUsage(resp.Usage); len(raw) > 0 {
		m.setRaw("usage", raw)
	}
	if raw := encodeError(resp.Error); len(raw) > 0 {
		m.setRaw("error", raw)
	}
	if incomplete != "" {
		m.setRaw("incomplete_details", marshalValue(map[string]string{"reason": incomplete}))
	}

	m.mergeExtras(resp.Extensions[ExtensionKey])

	raw := m.raw()
	if len(raw) == 0 {
		return nil, fmt.Errorf("responses: failed to encode response")
	}
	return raw, nil
}

// outputTokenLimit is the output cap to emit. max_completion_tokens is the
// newer spelling and wins when both are present.
func outputTokenLimit(params xir.Params) *int {
	if params.MaxCompletion != nil {
		return params.MaxCompletion
	}
	return params.MaxTokens
}

func encodeInstructions(parts []xir.Part) (json.RawMessage, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	allText := true
	for i := range parts {
		if parts[i].Kind != xir.PartText {
			allText = false
			break
		}
	}
	if allText && len(parts) == 1 {
		return marshalValue(parts[0].Text), nil
	}
	items := make([]json.RawMessage, 0, len(parts))
	for i := range parts {
		item, err := encodeContentPart(&parts[i], xir.RoleUser)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return marshalArray(items), nil
}

func encodeInput(messages []xir.Message) (json.RawMessage, error) {
	items := make([]json.RawMessage, 0, len(messages))
	for i := range messages {
		encoded, err := encodeMessage(&messages[i])
		if err != nil {
			return nil, err
		}
		items = append(items, encoded...)
	}
	return marshalArray(items), nil
}

// encodeMessage renders one message as its Responses input items. Content
// parts are grouped into a message item; tool calls, tool results and
// reasoning become their own typed items, so a mixed message keeps the part
// order.
func encodeMessage(message *xir.Message) ([]json.RawMessage, error) {
	items := make([]json.RawMessage, 0, 2)
	content := make([]json.RawMessage, 0, 2)

	flush := func() error {
		if len(content) == 0 {
			return nil
		}
		m := newMembers()
		m.set("type", "message")
		m.set("role", inputRole(message.Role))
		m.setRaw("content", marshalArray(content))
		if id := refID(message.Ref, "message"); id != "" {
			m.set("id", id)
		}
		raw := m.raw()
		if len(raw) == 0 {
			return fmt.Errorf("responses: failed to encode message item")
		}
		items = append(items, raw)
		content = content[:0]
		return nil
	}

	for i := range message.Parts {
		part := &message.Parts[i]
		switch part.Kind {
		case xir.PartText, xir.PartRefusal, xir.PartImage, xir.PartFile, xir.PartAudio:
			item, err := encodeContentPart(part, message.Role)
			if err != nil {
				return nil, err
			}
			content = append(content, item)
		default:
			if err := flush(); err != nil {
				return nil, err
			}
			item, err := encodeInputItem(part)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return items, nil
}

func encodeInputItem(part *xir.Part) (json.RawMessage, error) {
	switch part.Kind {
	case xir.PartToolCall:
		return encodeFunctionCall(part)
	case xir.PartToolResult:
		return encodeFunctionCallOutput(part)
	case xir.PartReasoning:
		return encodeReasoning(part)
	case xir.PartSealed, xir.PartUnknown:
		return sealedBlock(part.Sealed)
	}
	return nil, fmt.Errorf("responses: cannot encode part kind %q in request input", part.Kind)
}

func encodeContentPart(part *xir.Part, role xir.Role) (json.RawMessage, error) {
	m := newMembers()
	switch part.Kind {
	case xir.PartText:
		m.set("type", contentTextType(role))
		m.set("text", part.Text)
	case xir.PartRefusal:
		m.set("type", "refusal")
		m.set("refusal", part.Text)
	case xir.PartImage:
		if part.Media == nil {
			return nil, fmt.Errorf("responses: image part has no media payload")
		}
		url := part.Media.URL
		if url == "" && part.Media.Data != "" {
			url = dataURL(part.Media.MediaType, part.Media.Data)
		}
		if url == "" {
			return nil, fmt.Errorf("responses: image part has neither a URL nor data")
		}
		m.set("type", "input_image")
		m.set("image_url", url)
		m.setString("detail", part.Media.Detail)
	case xir.PartFile:
		if part.Media == nil {
			return nil, fmt.Errorf("responses: file part has no media payload")
		}
		m.set("type", "input_file")
		if part.Media.Data != "" {
			m.set("file_data", part.Media.Data)
		}
		if part.Media.URL != "" {
			m.set("file_id", part.Media.URL)
		}
		m.setString("filename", part.Media.Filename)
	case xir.PartAudio:
		if part.Media == nil {
			return nil, fmt.Errorf("responses: audio part has no media payload")
		}
		audio := newMembers()
		audio.set("data", part.Media.Data)
		audio.setString("format", part.Media.MediaType)
		m.set("type", "input_audio")
		m.setRaw("input_audio", audio.raw())
	default:
		return nil, fmt.Errorf("responses: cannot encode part kind %q as content", part.Kind)
	}

	raw := m.raw()
	if len(raw) == 0 {
		return nil, fmt.Errorf("responses: failed to encode content part")
	}
	return raw, nil
}

func encodeFunctionCall(part *xir.Part) (json.RawMessage, error) {
	call := part.ToolCall
	if call == nil {
		return nil, fmt.Errorf("responses: tool-call part has no call payload")
	}
	kind := call.Type
	if kind == "" {
		kind = "function"
	}
	if kind != "function" {
		// A protocol-native call type (custom_tool_call, ...) can only be
		// re-emitted byte for byte; the IR does not model its payload field.
		if raw := refRaw(call.Ref, kind); len(raw) > 0 {
			return raw, nil
		}
		return nil, fmt.Errorf("responses: cannot encode tool call type %q", kind)
	}

	m := newMembers()
	m.set("type", "function_call")
	if id := refID(call.Ref, "function_call"); id != "" {
		m.set("id", id)
	}
	m.setString("call_id", call.ID)
	m.setString("name", call.Name)
	m.set("arguments", argumentsText(call))

	raw := m.raw()
	if len(raw) == 0 {
		return nil, fmt.Errorf("responses: failed to encode function_call item")
	}
	return raw, nil
}

func encodeFunctionCallOutput(part *xir.Part) (json.RawMessage, error) {
	result := part.ToolResult
	if result == nil {
		return nil, fmt.Errorf("responses: tool-result part has no result payload")
	}

	m := newMembers()
	m.set("type", "function_call_output")
	if id := refID(result.Ref, "function_call_output"); id != "" {
		m.set("id", id)
	}
	m.setString("call_id", result.CallID)
	m.setString("name", result.Name)

	switch {
	case len(result.Content) > 0:
		content := make([]json.RawMessage, 0, len(result.Content))
		for i := range result.Content {
			item, err := encodeContentPart(&result.Content[i], xir.RoleUser)
			if err != nil {
				return nil, err
			}
			content = append(content, item)
		}
		m.setRaw("output", marshalArray(content))
	case result.Text != "":
		m.set("output", result.Text)
	case len(result.JSON) > 0:
		m.setRaw("output", result.JSON)
	default:
		m.set("output", "")
	}

	raw := m.raw()
	if len(raw) == 0 {
		return nil, fmt.Errorf("responses: failed to encode function_call_output item")
	}
	return raw, nil
}

func encodeReasoning(part *xir.Part) (json.RawMessage, error) {
	reasoning := part.Reasoning
	if reasoning == nil {
		if part.Sealed != nil {
			return sealedBlock(part.Sealed)
		}
		return nil, fmt.Errorf("responses: reasoning part has no payload")
	}
	// Sealed reasoning (encrypted_content) is re-emitted verbatim so the
	// provider's opaque state round-trips byte for byte.
	if reasoning.Sealed != nil {
		return sealedBlock(reasoning.Sealed)
	}

	m := newMembers()
	m.set("type", "reasoning")
	if id := refID(part.Ref, "reasoning"); id != "" {
		m.set("id", id)
	}
	if len(reasoning.Summary) > 0 {
		summary := make([]json.RawMessage, 0, len(reasoning.Summary))
		for _, text := range reasoning.Summary {
			entry := newMembers()
			entry.set("type", "summary_text")
			entry.set("text", text)
			summary = append(summary, entry.raw())
		}
		m.setRaw("summary", marshalArray(summary))
	}
	if reasoning.Text != "" {
		entry := newMembers()
		entry.set("type", "reasoning_text")
		entry.set("text", reasoning.Text)
		m.setRaw("content", marshalArray([]json.RawMessage{entry.raw()}))
	}
	m.setString("encrypted_content", reasoning.EncryptedContent)

	raw := m.raw()
	if len(raw) == 0 {
		return nil, fmt.Errorf("responses: failed to encode reasoning item")
	}
	return raw, nil
}

func encodeTools(tools []xir.Tool) (json.RawMessage, error) {
	items := make([]json.RawMessage, 0, len(tools))
	for i := range tools {
		item, err := encodeTool(&tools[i])
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return marshalArray(items), nil
}

// encodeTool keeps the Responses flat tool shape: function properties live on
// the tool object, never under a nested "function".
func encodeTool(tool *xir.Tool) (json.RawMessage, error) {
	kind := tool.Kind
	if kind == "" {
		kind = "function"
	}

	m := newMembers()
	m.set("type", kind)
	switch kind {
	case "function":
		m.setString("name", tool.Name)
		m.setString("description", tool.Description)
		m.setRaw("parameters", tool.Schema)
		m.setBoolPtr("strict", tool.Strict)
	case "custom":
		m.setString("name", tool.Name)
		m.setString("description", tool.Description)
		// The custom tool's format is protocol-native; re-emit it when the
		// tool came from a Responses document.
		mergeRefExtras(m, tool.Ref, "type", "name", "description")
	default:
		m.setString("name", tool.Name)
		m.setString("description", tool.Description)
		m.setRaw("parameters", tool.Schema)
		m.setBoolPtr("strict", tool.Strict)
		mergeRefExtras(m, tool.Ref, "type", "name", "description", "parameters", "strict")
	}

	raw := m.raw()
	if len(raw) == 0 {
		return nil, fmt.Errorf("responses: failed to encode tool %q", tool.Name)
	}
	return raw, nil
}

func encodeToolChoice(choice xir.ToolChoice) (json.RawMessage, bool) {
	mode := strings.ToLower(strings.TrimSpace(choice.Mode))
	if mode == "" && choice.Name != "" {
		mode = "tool"
	}
	switch mode {
	case "":
		return nil, false
	case "tool":
		if choice.Name == "" {
			return marshalValue("required"), true
		}
		m := newMembers()
		m.set("type", "function")
		m.set("name", choice.Name)
		raw := m.raw()
		return raw, len(raw) > 0
	default:
		return marshalValue(mode), true
	}
}

func encodeTextConfig(params xir.Params) json.RawMessage {
	m := newMembers()
	if len(params.ResponseFormat) > 0 {
		m.setRaw("format", params.ResponseFormat)
	}
	if verbosity, ok := params.Extra[ParamTextVerbosity]; ok {
		m.setRaw("verbosity", verbosity)
	}
	if len(m) == 0 {
		return nil
	}
	return m.raw()
}

func encodeReasoningConfig(params xir.Params) json.RawMessage {
	m := newMembers()
	m.setString("effort", params.ReasoningEffort)
	if summary, ok := params.Extra[ParamReasoningSummary]; ok {
		m.setRaw("summary", summary)
	}
	if len(m) == 0 {
		return nil
	}
	return m.raw()
}

func encodeOutput(parts []xir.Part) (json.RawMessage, error) {
	items := make([]json.RawMessage, 0, len(parts))
	content := make([]json.RawMessage, 0, 2)

	flush := func() error {
		if len(content) == 0 {
			return nil
		}
		m := newMembers()
		m.set("type", "message")
		m.set("role", "assistant")
		m.setRaw("content", marshalArray(content))
		raw := m.raw()
		if len(raw) == 0 {
			return fmt.Errorf("responses: failed to encode output message item")
		}
		items = append(items, raw)
		content = content[:0]
		return nil
	}

	for i := range parts {
		part := &parts[i]
		switch part.Kind {
		case xir.PartText:
			content = append(content, outputTextItem(part.Text))
		case xir.PartRefusal:
			item := newMembers()
			item.set("type", "refusal")
			item.set("refusal", part.Text)
			content = append(content, item.raw())
		default:
			if err := flush(); err != nil {
				return nil, err
			}
			item, err := encodeOutputItem(part)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return marshalArray(items), nil
}

func encodeOutputItem(part *xir.Part) (json.RawMessage, error) {
	switch part.Kind {
	case xir.PartToolCall:
		return encodeFunctionCall(part)
	case xir.PartToolResult:
		return encodeFunctionCallOutput(part)
	case xir.PartReasoning:
		return encodeReasoning(part)
	case xir.PartSealed, xir.PartUnknown:
		return sealedBlock(part.Sealed)
	}
	return nil, fmt.Errorf("responses: cannot encode part kind %q in a response", part.Kind)
}

func outputTextItem(text string) json.RawMessage {
	m := newMembers()
	m.set("type", "output_text")
	m.set("text", text)
	return m.raw()
}

func responseStatus(resp *xir.Response) (string, string) {
	status := resp.Status
	if status == "" {
		switch resp.StopReason {
		case xir.FinishLength:
			return "incomplete", "max_output_tokens"
		case xir.FinishContentFilter:
			return "incomplete", "content_filter"
		case xir.FinishError:
			return "failed", ""
		default:
			return "completed", ""
		}
	}
	if status != "incomplete" {
		return status, ""
	}
	incomplete := resp.StopDetail
	if incomplete == "" {
		switch resp.StopReason {
		case xir.FinishLength:
			incomplete = "max_output_tokens"
		case xir.FinishContentFilter:
			incomplete = "content_filter"
		}
	}
	return status, incomplete
}

func encodeUsage(u *usage.Usage) json.RawMessage {
	if u == nil {
		return nil
	}
	normalized := u.Normalize()
	m := newMembers()
	m.set("input_tokens", normalized.InputTokens)
	m.set("output_tokens", normalized.OutputTokens)
	m.set("total_tokens", normalized.TotalTokens)
	if normalized.CacheReadTokens != 0 {
		m.setRaw("input_tokens_details", marshalValue(map[string]int{"cached_tokens": normalized.CacheReadTokens}))
	}
	if normalized.ReasoningTokens != 0 {
		m.setRaw("output_tokens_details", marshalValue(map[string]int{"reasoning_tokens": normalized.ReasoningTokens}))
	}
	return m.raw()
}

func encodeError(providerError *xir.Error) json.RawMessage {
	if providerError == nil {
		return nil
	}
	m := newMembers()
	m.setString("message", providerError.Message)
	m.setString("type", providerError.Type)
	m.setString("code", providerError.Code)
	m.setString("param", providerError.Param)
	if len(m) == 0 {
		if len(providerError.Raw) > 0 && json.Valid(providerError.Raw) {
			return cloneRaw(providerError.Raw)
		}
		return nil
	}
	return m.raw()
}

func sealedBlock(sealed *xir.Sealed) (json.RawMessage, error) {
	if sealed == nil {
		return nil, fmt.Errorf("responses: sealed part has no payload")
	}
	if sealed.Protocol != xir.ProtocolResponses {
		return nil, fmt.Errorf("responses: cannot encode sealed %q block from protocol %q", sealed.Kind, sealed.Protocol)
	}
	if len(sealed.Raw) == 0 || !json.Valid(sealed.Raw) {
		return nil, fmt.Errorf("responses: sealed %q block is not valid JSON", sealed.Kind)
	}
	return cloneRaw(sealed.Raw), nil
}

func argumentsText(call *xir.ToolCall) string {
	if call.ArgsText != "" {
		return call.ArgsText
	}
	if len(call.Args) > 0 {
		return compactJSON(call.Args)
	}
	return "{}"
}

func compactJSON(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

func dataURL(mediaType, data string) string {
	if strings.HasPrefix(data, "data:") {
		return data
	}
	if mediaType != "" {
		return "data:" + mediaType + ";base64," + data
	}
	return ""
}

func contentTextType(role xir.Role) string {
	switch role {
	case xir.RoleAssistant, xir.RoleModel:
		return "output_text"
	}
	return "input_text"
}

func inputRole(role xir.Role) string {
	switch role {
	case xir.RoleSystem:
		return "system"
	case xir.RoleDeveloper:
		return "developer"
	case xir.RoleAssistant, xir.RoleModel:
		return "assistant"
	default:
		return "user"
	}
}

func refID(ref *wire.Node, wantType string) string {
	if ref == nil || ref.Kind != wire.Object || nodeType(ref) != wantType {
		return ""
	}
	id, _ := ref.StringField("id")
	return id
}

func refRaw(ref *wire.Node, wantType string) json.RawMessage {
	if ref == nil || ref.Kind != wire.Object || nodeType(ref) != wantType {
		return nil
	}
	return cloneRaw(ref.Raw())
}

func mergeRefExtras(m members, ref *wire.Node, skip ...string) {
	if ref == nil || ref.Kind != wire.Object {
		return
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(ref.Raw(), &all); err != nil {
		return
	}
	for _, key := range skip {
		delete(all, key)
	}
	for key, value := range all {
		if _, exists := m[key]; exists {
			continue
		}
		m[key] = cloneRaw(value)
	}
}
