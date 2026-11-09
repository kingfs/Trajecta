package responses

import (
	"encoding/json"
	"strings"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// decodeInstructions decodes the optional instructions member. The API
// accepts a plain string or an array of input content items.
func decodeInstructions(root *wire.Node) []xir.Part {
	node, ok := root.Get("instructions")
	if !ok || node == nil {
		return nil
	}
	switch node.Kind {
	case wire.String:
		return []xir.Part{{Kind: xir.PartText, Text: node.Str, Ref: node}}
	case wire.Array:
		parts := make([]xir.Part, 0, len(node.Items))
		for _, item := range node.Items {
			if part, ok := decodeContentPart(item); ok {
				parts = append(parts, part)
			}
		}
		return parts
	case wire.Object:
		if part, ok := decodeContentPart(node); ok {
			return []xir.Part{part}
		}
	}
	return nil
}

// decodeInput flattens the item-based input into messages, keeping the
// original item order.
func decodeInput(root *wire.Node) []xir.Message {
	node, ok := root.Get("input")
	if !ok || node == nil {
		return nil
	}
	switch node.Kind {
	case wire.String:
		return []xir.Message{{
			Role:  xir.RoleUser,
			Parts: []xir.Part{{Kind: xir.PartText, Text: node.Str, Ref: node}},
			Ref:   node,
		}}
	case wire.Array:
		messages := make([]xir.Message, 0, len(node.Items))
		for _, item := range node.Items {
			if message, ok := decodeInputItem(item); ok {
				messages = append(messages, message)
			}
		}
		return messages
	case wire.Object:
		// A single item instead of an array is tolerated.
		if message, ok := decodeInputItem(node); ok {
			return []xir.Message{message}
		}
	}
	return nil
}

func decodeInputItem(item *wire.Node) (xir.Message, bool) {
	if item == nil {
		return xir.Message{}, false
	}
	if item.Kind == wire.String {
		return xir.Message{
			Role:  xir.RoleUser,
			Parts: []xir.Part{{Kind: xir.PartText, Text: item.Str, Ref: item}},
			Ref:   item,
		}, true
	}
	if item.Kind != wire.Object {
		return xir.Message{}, false
	}

	switch typ := nodeType(item); typ {
	case "message":
		return decodeMessageItem(item, xir.RoleUser), true
	case "function_call":
		return decodeFunctionCallItem(item), true
	case "function_call_output":
		return decodeFunctionCallOutputItem(item), true
	case "reasoning":
		return decodeReasoningItem(item), true
	case "refusal":
		return xir.Message{
			Role:  xir.RoleAssistant,
			Parts: []xir.Part{decodeRefusalPart(item)},
			Ref:   item,
		}, true
	case "":
		// Easy input messages omit the type; tolerate a bare function call or
		// output as well.
		if _, ok := item.StringField("role"); ok {
			return decodeMessageItem(item, xir.RoleUser), true
		}
		if _, ok := item.Get("content"); ok {
			return decodeMessageItem(item, xir.RoleUser), true
		}
		if _, ok := item.Get("arguments"); ok {
			return decodeFunctionCallItem(item), true
		}
		if _, ok := item.Get("output"); ok {
			return decodeFunctionCallOutputItem(item), true
		}
		if _, ok := item.StringField("text"); ok {
			return decodeMessageItem(item, xir.RoleUser), true
		}
	}
	// Unknown item types are preserved byte for byte so a same-protocol
	// re-encode does not drop them.
	return xir.Message{
		Role:  xir.RoleUnknown,
		Parts: []xir.Part{unknownPart(item)},
		Ref:   item,
	}, true
}

func decodeMessageItem(item *wire.Node, defaultRole xir.Role) xir.Message {
	roleName, _ := item.StringField("role")
	role := roleFromString(roleName)
	if role == xir.RoleUnknown && roleName == "" {
		role = defaultRole
	}
	message := xir.Message{Role: role, Ref: item}
	message.Name, _ = item.StringField("name")

	if content, ok := item.Get("content"); ok && content != nil {
		message.Parts = decodeContentList(content)
		return message
	}
	if text, ok := item.StringField("text"); ok {
		message.Parts = []xir.Part{{Kind: xir.PartText, Text: text, Ref: item}}
	}
	return message
}

func decodeFunctionCallItem(item *wire.Node) xir.Message {
	call := &xir.ToolCall{Type: "function", Ref: item}
	call.ID, _ = item.StringField("call_id")
	if call.ID == "" {
		call.ID, _ = item.StringField("id")
	}
	call.Name, _ = item.StringField("name")
	decodeArguments(item, call)

	return xir.Message{
		Role:  xir.RoleAssistant,
		Parts: []xir.Part{{Kind: xir.PartToolCall, ToolCall: call, Ref: item}},
		Ref:   item,
	}
}

// decodeArguments accepts the documented string form and the object/array
// form some compatible providers emit.
func decodeArguments(item *wire.Node, call *xir.ToolCall) {
	arguments, ok := item.Get("arguments")
	if !ok || arguments == nil {
		return
	}
	if text, ok := arguments.String(); ok {
		call.ArgsText = text
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			if json.Valid([]byte(text)) {
				call.Args = cloneRaw([]byte(text))
			}
		}
		return
	}
	if arguments.Kind == wire.Object || arguments.Kind == wire.Array {
		call.Args = cloneRaw(arguments.Raw())
		call.ArgsText = string(arguments.Raw())
	}
}

func decodeFunctionCallOutputItem(item *wire.Node) xir.Message {
	result := &xir.ToolResult{Ref: item}
	result.CallID, _ = item.StringField("call_id")
	if result.CallID == "" {
		result.CallID, _ = item.StringField("tool_call_id")
	}
	result.Name, _ = item.StringField("name")
	if status, _ := item.StringField("status"); status == "failed" {
		result.IsError = true
	}

	if output, ok := item.Get("output"); ok && output != nil {
		result.JSON = cloneRaw(output.Raw())
		switch output.Kind {
		case wire.String:
			result.Text = output.Str
		case wire.Array:
			result.Content = decodeContentList(output)
			result.Text = joinTextParts(result.Content)
		case wire.Null:
			// An empty output is still a valid function_call_output.
		default:
			result.Text = string(output.Raw())
		}
	}

	return xir.Message{
		Role:  xir.RoleTool,
		Parts: []xir.Part{{Kind: xir.PartToolResult, ToolResult: result, Ref: item}},
		Ref:   item,
	}
}

func decodeReasoningItem(item *wire.Node) xir.Message {
	reasoning := &xir.Reasoning{}
	if summary, ok := item.Get("summary"); ok && summary != nil && summary.Kind == wire.Array {
		for _, entry := range summary.Items {
			if text, ok := entry.StringField("text"); ok {
				reasoning.Summary = append(reasoning.Summary, text)
			} else if text, ok := entry.String(); ok {
				reasoning.Summary = append(reasoning.Summary, text)
			}
		}
	}
	if content, ok := item.Get("content"); ok && content != nil && content.Kind == wire.Array {
		var texts []string
		for _, entry := range content.Items {
			if text, ok := entry.StringField("text"); ok {
				texts = append(texts, text)
			}
		}
		reasoning.Text = strings.Join(texts, "\n")
	}
	if encrypted, ok := item.StringField("encrypted_content"); ok && encrypted != "" {
		reasoning.EncryptedContent = encrypted
		// The whole item is sealed: only the Responses codec may re-emit it.
		reasoning.Sealed = &xir.Sealed{
			Protocol: xir.ProtocolResponses,
			Kind:     "reasoning",
			Raw:      cloneRaw(item.Raw()),
		}
	}

	return xir.Message{
		Role:  xir.RoleAssistant,
		Parts: []xir.Part{{Kind: xir.PartReasoning, Reasoning: reasoning, Ref: item}},
		Ref:   item,
	}
}

// decodeOutput flattens the typed output items into parts.
func decodeOutput(root *wire.Node) []xir.Part {
	output, ok := root.Get("output")
	if !ok || output == nil || output.Kind != wire.Array {
		return nil
	}
	parts := make([]xir.Part, 0, len(output.Items))
	for _, item := range output.Items {
		parts = append(parts, decodeOutputItem(item)...)
	}
	return parts
}

func decodeOutputItem(item *wire.Node) []xir.Part {
	if item == nil {
		return nil
	}
	if item.Kind == wire.String {
		return []xir.Part{{Kind: xir.PartText, Text: item.Str, Ref: item}}
	}
	if item.Kind != wire.Object {
		return []xir.Part{unknownPart(item)}
	}

	switch typ := nodeType(item); typ {
	case "message":
		return decodeOutputMessage(item)
	case "function_call":
		return decodeFunctionCallItem(item).Parts
	case "function_call_output":
		return decodeFunctionCallOutputItem(item).Parts
	case "reasoning":
		return decodeReasoningItem(item).Parts
	case "refusal":
		return []xir.Part{decodeRefusalPart(item)}
	case "output_text", "text":
		text, _ := item.StringField("text")
		return []xir.Part{{Kind: xir.PartText, Text: text, Ref: item}}
	}
	return []xir.Part{unknownPart(item)}
}

func decodeOutputMessage(item *wire.Node) []xir.Part {
	if content, ok := item.Get("content"); ok && content != nil {
		return decodeContentList(content)
	}
	if text, ok := item.StringField("text"); ok {
		return []xir.Part{{Kind: xir.PartText, Text: text, Ref: item}}
	}
	return nil
}

func decodeRefusalPart(item *wire.Node) xir.Part {
	text, _ := item.StringField("refusal")
	if text == "" {
		text, _ = item.StringField("text")
	}
	return xir.Part{Kind: xir.PartRefusal, Text: text, Ref: item}
}

func decodeContentList(node *wire.Node) []xir.Part {
	if node == nil {
		return nil
	}
	switch node.Kind {
	case wire.String:
		return []xir.Part{{Kind: xir.PartText, Text: node.Str, Ref: node}}
	case wire.Object:
		if part, ok := decodeContentPart(node); ok {
			return []xir.Part{part}
		}
	case wire.Array:
		parts := make([]xir.Part, 0, len(node.Items))
		for _, item := range node.Items {
			if part, ok := decodeContentPart(item); ok {
				parts = append(parts, part)
			}
		}
		return parts
	}
	return nil
}

// decodeContentPart decodes one typed content block. The second result is
// false only for a non-object value that is not a string.
func decodeContentPart(node *wire.Node) (xir.Part, bool) {
	if node == nil {
		return xir.Part{}, false
	}
	if node.Kind == wire.String {
		return xir.Part{Kind: xir.PartText, Text: node.Str, Ref: node}, true
	}
	if node.Kind != wire.Object {
		return xir.Part{}, false
	}

	switch typ := nodeType(node); typ {
	case "input_text", "output_text", "text", "summary_text", "reasoning_text":
		text, _ := node.StringField("text")
		return xir.Part{Kind: xir.PartText, Text: text, Ref: node}, true
	case "refusal":
		return decodeRefusalPart(node), true
	case "input_image", "image_url", "image":
		return decodeImagePart(node), true
	case "input_file", "file":
		return decodeFilePart(node), true
	case "input_audio", "audio":
		return decodeAudioPart(node), true
	case "":
		if text, ok := node.StringField("text"); ok {
			return xir.Part{Kind: xir.PartText, Text: text, Ref: node}, true
		}
	}
	return unknownPart(node), true
}

func decodeImagePart(node *wire.Node) xir.Part {
	media := &xir.Media{Kind: xir.MediaImage}
	if url, ok := node.StringField("image_url"); ok {
		media.URL = url
	} else if image, ok := node.Get("image_url"); ok && image != nil && image.Kind == wire.Object {
		media.URL, _ = image.StringField("url")
		media.Detail, _ = image.StringField("detail")
	} else if url, ok := node.StringField("url"); ok {
		media.URL = url
	}
	if media.URL == "" {
		media.Data, _ = node.StringField("image_data")
	}
	if detail, ok := node.StringField("detail"); ok {
		media.Detail = detail
	}
	return xir.Part{Kind: xir.PartImage, Media: media, Ref: node}
}

func decodeFilePart(node *wire.Node) xir.Part {
	media := &xir.Media{Kind: xir.MediaFile}
	// Responses input_file has file_id, file_data and filename; it has no URL
	// member, so a file_id is carried in Media.URL by this codec.
	media.URL, _ = node.StringField("file_id")
	media.Data, _ = node.StringField("file_data")
	media.Filename, _ = node.StringField("filename")
	media.MediaType, _ = node.StringField("media_type")
	return xir.Part{Kind: xir.PartFile, Media: media, Ref: node}
}

func decodeAudioPart(node *wire.Node) xir.Part {
	media := &xir.Media{Kind: xir.MediaAudio}
	if audio, ok := node.Get("input_audio"); ok && audio != nil && audio.Kind == wire.Object {
		media.Data, _ = audio.StringField("data")
		media.MediaType, _ = audio.StringField("format")
	} else {
		media.Data, _ = node.StringField("data")
		media.MediaType, _ = node.StringField("format")
	}
	return xir.Part{Kind: xir.PartAudio, Media: media, Ref: node}
}

// unknownPart preserves a content block or output item the semantic model does
// not type.
func unknownPart(node *wire.Node) xir.Part {
	return xir.Part{
		Kind: xir.PartUnknown,
		Sealed: &xir.Sealed{
			Protocol: xir.ProtocolResponses,
			Kind:     nodeType(node),
			Raw:      cloneRaw(node.Raw()),
		},
		Ref: node,
	}
}

func decodeTools(root *wire.Node) []xir.Tool {
	node, ok := root.Get("tools")
	if !ok || node == nil || node.Kind != wire.Array {
		return nil
	}
	tools := make([]xir.Tool, 0, len(node.Items))
	for _, item := range node.Items {
		if item == nil || item.Kind != wire.Object {
			continue
		}
		tool := xir.Tool{Ref: item}
		tool.Kind, _ = item.StringField("type")
		if tool.Kind == "" {
			tool.Kind = "function"
		}
		tool.Name, _ = item.StringField("name")
		tool.Description, _ = item.StringField("description")
		if parameters, ok := item.Get("parameters"); ok && parameters != nil {
			tool.Schema = cloneRaw(parameters.Raw())
		}
		if strict, ok := item.BoolField("strict"); ok {
			tool.Strict = &strict
		}

		// Tolerate the older nested {"function": {...}} spelling.
		if function, ok := item.Get("function"); ok && function != nil && function.Kind == wire.Object {
			if tool.Name == "" {
				tool.Name, _ = function.StringField("name")
			}
			if tool.Description == "" {
				tool.Description, _ = function.StringField("description")
			}
			if len(tool.Schema) == 0 {
				if parameters, ok := function.Get("parameters"); ok && parameters != nil {
					tool.Schema = cloneRaw(parameters.Raw())
				}
			}
			if tool.Strict == nil {
				if strict, ok := function.BoolField("strict"); ok {
					tool.Strict = &strict
				}
			}
		}
		tools = append(tools, tool)
	}
	return tools
}

// decodeToolChoice maps the representable tool_choice shapes. The second
// result is false for a shape the IR cannot represent, which the caller keeps
// in Extensions.
func decodeToolChoice(node *wire.Node) (xir.ToolChoice, bool) {
	if node == nil {
		return xir.ToolChoice{}, true
	}
	switch node.Kind {
	case wire.Null:
		return xir.ToolChoice{}, true
	case wire.String:
		switch node.Str {
		case "auto", "none", "required":
			return xir.ToolChoice{Mode: node.Str}, true
		}
	case wire.Object:
		typ, _ := node.StringField("type")
		name, _ := node.StringField("name")
		switch typ {
		case "function", "custom":
			if name != "" {
				return xir.ToolChoice{Mode: "tool", Name: name}, true
			}
		case "":
			if name != "" {
				return xir.ToolChoice{Mode: "tool", Name: name}, true
			}
		}
	}
	return xir.ToolChoice{}, false
}

func decodeRequestParams(root *wire.Node) xir.Params {
	var params xir.Params
	params.Temperature = floatField(root, "temperature")
	params.TopP = floatField(root, "top_p")
	if value, ok := root.IntField("top_k"); ok {
		topK := int(value)
		params.TopK = &topK
	}
	if value, ok := root.IntField("max_output_tokens"); ok {
		maxTokens := int(value)
		// max_output_tokens is the protocol-neutral output cap.
		params.MaxTokens = &maxTokens
	}
	params.ParallelTools = boolField(root, "parallel_tool_calls")
	params.User, _ = root.StringField("user")
	if metadata, ok := root.Get("metadata"); ok && metadata != nil {
		params.Metadata = cloneRaw(metadata.Raw())
	}

	if text, ok := root.Get("text"); ok && text != nil && text.Kind == wire.Object {
		if format, ok := text.Get("format"); ok && format != nil {
			params.ResponseFormat = cloneRaw(format.Raw())
		}
		if verbosity, ok := text.Get("verbosity"); ok && verbosity != nil {
			setExtra(&params, ParamTextVerbosity, verbosity.Raw())
		}
	}
	if reasoning, ok := root.Get("reasoning"); ok && reasoning != nil && reasoning.Kind == wire.Object {
		params.ReasoningEffort, _ = reasoning.StringField("effort")
		if summary, ok := reasoning.Get("summary"); ok && summary != nil {
			setExtra(&params, ParamReasoningSummary, summary.Raw())
		}
	}
	return params
}

func decodeState(root *wire.Node) xir.StateRefs {
	var state xir.StateRefs
	state.PreviousResponseID, _ = root.StringField("previous_response_id")
	if conversation, ok := root.Get("conversation"); ok && conversation != nil {
		switch conversation.Kind {
		case wire.String:
			state.ConversationID = conversation.Str
		case wire.Object:
			state.ConversationID, _ = conversation.StringField("id")
		}
	}
	state.PromptCacheKey, _ = root.StringField("prompt_cache_key")
	if store, ok := root.BoolField("store"); ok {
		state.Store = &store
	}
	return state
}

func decodeUsageNode(root *wire.Node) *usage.Usage {
	node, ok := root.Get("usage")
	if !ok || node == nil || node.Kind != wire.Object {
		return nil
	}
	u := &usage.Usage{
		InputTokens:  intField(node, "input_tokens"),
		OutputTokens: intField(node, "output_tokens"),
		TotalTokens:  intField(node, "total_tokens"),
		Source:       "responses",
	}
	if details, ok := node.Get("input_tokens_details"); ok && details != nil && details.Kind == wire.Object {
		u.CacheReadTokens = intField(details, "cached_tokens")
	}
	if u.CacheReadTokens == 0 {
		// Older/compatible shapes spell the cache counter out.
		u.CacheReadTokens = intField(node, "cache_read_input_tokens")
	}
	u.CacheCreationTokens = intField(node, "cache_creation_input_tokens")

	if details, ok := node.Get("output_tokens_details"); ok && details != nil && details.Kind == wire.Object {
		u.ReasoningTokens = intField(details, "reasoning_tokens")
	}
	if u.ReasoningTokens == 0 {
		u.ReasoningTokens = intField(node, "reasoning_tokens")
	}

	normalized := u.Normalize()
	return &normalized
}

func decodeErrorEnvelope(root *wire.Node) *xir.Error {
	node, ok := root.Get("error")
	if !ok || node == nil || node.Kind == wire.Null {
		return nil
	}
	if node.Kind == wire.String {
		return &xir.Error{Message: node.Str, Raw: cloneRaw(node.Raw())}
	}
	if node.Kind != wire.Object {
		return nil
	}
	providerError := &xir.Error{Raw: cloneRaw(node.Raw())}
	providerError.Message, _ = node.StringField("message")
	providerError.Type, _ = node.StringField("type")
	providerError.Param, _ = node.StringField("param")
	if code, ok := node.Get("code"); ok && code != nil {
		if text, ok := code.String(); ok {
			providerError.Code = text
		} else if code.Kind == wire.Number {
			providerError.Code = string(code.Raw())
		}
	}
	if providerError.Message == "" && providerError.Type == "" &&
		providerError.Code == "" && providerError.Param == "" {
		return nil
	}
	return providerError
}

// mapStopReason maps the Responses status/incomplete_details pair onto the
// normalized finish reason where the mapping is clear.
func mapStopReason(status, incomplete string, parts []xir.Part, providerError *xir.Error) (xir.FinishReason, string) {
	switch status {
	case "completed":
		if hasToolCall(parts) {
			return xir.FinishToolUse, ""
		}
		return xir.FinishStop, ""
	case "incomplete":
		switch incomplete {
		case "max_output_tokens", "max_tokens":
			return xir.FinishLength, incomplete
		case "content_filter":
			return xir.FinishContentFilter, incomplete
		}
		return xir.FinishUnknown, incomplete
	case "failed":
		detail := incomplete
		if detail == "" && providerError != nil {
			detail = providerError.Message
		}
		return xir.FinishError, detail
	case "cancelled", "canceled", "queued", "in_progress":
		return xir.FinishUnknown, incomplete
	}
	if providerError != nil {
		return xir.FinishError, providerError.Message
	}
	return xir.FinishUnknown, incomplete
}

func hasToolCall(parts []xir.Part) bool {
	for i := range parts {
		if parts[i].Kind == xir.PartToolCall {
			return true
		}
	}
	return false
}

func joinTextParts(parts []xir.Part) string {
	var texts []string
	for i := range parts {
		if parts[i].Kind == xir.PartText && parts[i].Text != "" {
			texts = append(texts, parts[i].Text)
		}
	}
	return strings.Join(texts, "\n")
}

func roleFromString(role string) xir.Role {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system":
		return xir.RoleSystem
	case "developer":
		return xir.RoleDeveloper
	case "user":
		return xir.RoleUser
	case "assistant":
		return xir.RoleAssistant
	case "tool":
		return xir.RoleTool
	case "model":
		return xir.RoleModel
	}
	return xir.RoleUnknown
}

func nodeType(node *wire.Node) string {
	if node == nil || node.Kind != wire.Object {
		return ""
	}
	typ, _ := node.StringField("type")
	return typ
}

func floatField(node *wire.Node, key string) *float64 {
	value, ok := node.Get(key)
	if !ok || value == nil {
		return nil
	}
	number, ok := value.Float()
	if !ok {
		return nil
	}
	return &number
}

func boolField(node *wire.Node, key string) *bool {
	value, ok := node.Get(key)
	if !ok || value == nil {
		return nil
	}
	flag, ok := value.Bool()
	if !ok {
		return nil
	}
	return &flag
}

func intField(node *wire.Node, key string) int {
	value, ok := node.IntField(key)
	if !ok {
		return 0
	}
	return int(value)
}

func setExtra(params *xir.Params, key string, raw []byte) {
	if params.Extra == nil {
		params.Extra = map[string]json.RawMessage{}
	}
	params.Extra[key] = cloneRaw(raw)
}
