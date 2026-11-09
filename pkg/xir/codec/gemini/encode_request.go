package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
)

// The wire structs below are the Gemini JSON shapes the codec emits. Fields
// are only present in the output when Gemini defines them: no model member in
// the request body, no stream member (the method selects streaming), and no
// OpenAI/Anthropic compatibility fields.

type requestWire struct {
	Contents          []contentWire   `json:"contents"`
	SystemInstruction *contentWire    `json:"systemInstruction,omitempty"`
	Tools             []toolWire      `json:"tools,omitempty"`
	ToolConfig        *toolConfigWire `json:"toolConfig,omitempty"`
	GenerationConfig  json.RawMessage `json:"generationConfig,omitempty"`
}

type contentWire struct {
	Role  string            `json:"role,omitempty"`
	Parts []json.RawMessage `json:"parts"`
}

type partWire struct {
	Text             *string               `json:"text,omitempty"`
	Thought          bool                  `json:"thought,omitempty"`
	ThoughtSignature string                `json:"thoughtSignature,omitempty"`
	InlineData       *inlineDataWire       `json:"inlineData,omitempty"`
	FileData         *fileDataWire         `json:"fileData,omitempty"`
	FunctionCall     *functionCallWire     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponseWire `json:"functionResponse,omitempty"`
}

type inlineDataWire struct {
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

type fileDataWire struct {
	MimeType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri,omitempty"`
}

type functionCallWire struct {
	Name string          `json:"name,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
}

type functionResponseWire struct {
	Name     string          `json:"name,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

type toolWire struct {
	FunctionDeclarations []functionDeclarationWire `json:"functionDeclarations,omitempty"`
}

type functionDeclarationWire struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type toolConfigWire struct {
	FunctionCallingConfig *functionCallingConfigWire `json:"functionCallingConfig,omitempty"`
}

type functionCallingConfigWire struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

// EncodeRequest encodes a neutral request as a Gemini generateContent body.
//
// Gemini takes the model and the streaming choice from the URL/RPC method, so
// EncodeOptions.Model and EncodeOptions.Stream do not add body members; the
// caller selects models/<model>:generateContent or streamGenerateContent.
func (c Codec) EncodeRequest(ctx context.Context, req *xir.Request, opts xir.EncodeOptions) ([]byte, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, fmt.Errorf("gemini: nil request")
	}

	systemParts := append([]xir.Part(nil), req.System...)
	callNames := toolCallNames(req)
	contents := make([]contentWire, 0, len(req.Messages))
	for i := range req.Messages {
		message := &req.Messages[i]
		if message.Role == xir.RoleSystem || message.Role == xir.RoleDeveloper {
			systemParts = append(systemParts, message.Parts...)
			continue
		}
		role := encodeRole(message.Role)
		parts, err := encodeParts(message.Parts, callNames)
		if err != nil {
			return nil, err
		}
		if len(parts) == 0 {
			continue
		}
		if n := len(contents); n > 0 && contents[n-1].Role == role {
			contents[n-1].Parts = append(contents[n-1].Parts, parts...)
			continue
		}
		contents = append(contents, contentWire{Role: role, Parts: parts})
	}

	var system *contentWire
	if parts := encodeSystemParts(systemParts); len(parts) > 0 {
		system = &contentWire{Parts: parts}
	}

	tools, err := encodeTools(req.Tools)
	if err != nil {
		return nil, err
	}
	generationConfig, err := encodeGenerationConfig(req.Params)
	if err != nil {
		return nil, err
	}
	extension, err := extensionObject(req.Extensions)
	if err != nil {
		return nil, err
	}
	if raw, ok := extension["safetySettings"]; ok {
		if err := validateSafetySettingsRaw(raw); err != nil {
			return nil, err
		}
	}

	return marshalWithExtensions(requestWire{
		Contents:          contents,
		SystemInstruction: system,
		Tools:             tools,
		ToolConfig:        encodeToolChoice(req.ToolChoice),
		GenerationConfig:  generationConfig,
	}, extensionRaw(req.Extensions), requestOwnedKeys)
}

func encodeSystemParts(parts []xir.Part) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(parts))
	for i := range parts {
		part := &parts[i]
		switch part.Kind {
		case xir.PartText:
			raw, ok, err := marshalPart(&partWire{Text: stringPointer(part.Text)})
			if err == nil && ok {
				out = append(out, raw)
			}
		case xir.PartReasoning:
			text := part.Text
			if part.Reasoning != nil && part.Reasoning.Text != "" {
				text = part.Reasoning.Text
			}
			// systemInstruction is text; a thought marker there is not a
			// Gemini system-instruction field.
			raw, ok, err := marshalPart(&partWire{Text: stringPointer(text)})
			if err == nil && ok {
				out = append(out, raw)
			}
		case xir.PartSealed:
			if raw, ok, err := encodeSealedPart(part); err == nil && ok {
				out = append(out, raw)
			}
		}
	}
	return out
}

func encodeParts(parts []xir.Part, callNames map[string]string) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(parts))
	for i := range parts {
		raw, ok, err := encodePart(&parts[i], callNames)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, raw)
		}
	}
	return out, nil
}

func encodePart(part *xir.Part, callNames map[string]string) (json.RawMessage, bool, error) {
	if part == nil {
		return nil, false, nil
	}
	switch part.Kind {
	case xir.PartText:
		return marshalPart(&partWire{Text: stringPointer(part.Text)})
	case xir.PartReasoning:
		return encodeReasoningPart(part)
	case xir.PartToolCall:
		return encodeToolCallPart(part)
	case xir.PartToolResult:
		return encodeToolResultPart(part, callNames)
	case xir.PartImage, xir.PartFile, xir.PartAudio, xir.PartVideo:
		return encodeMediaPart(part)
	case xir.PartRefusal:
		if part.Text == "" {
			return nil, false, nil
		}
		// Gemini has no refusal part; a model refusal is plain text.
		return marshalPart(&partWire{Text: stringPointer(part.Text)})
	case xir.PartSealed:
		return encodeSealedPart(part)
	case xir.PartUnknown:
		return nil, false, nil
	}

	// Fall back to the populated field so a caller that leaves Kind empty but
	// fills a typed part still round-trips.
	switch {
	case part.ToolCall != nil:
		return encodeToolCallPart(part)
	case part.ToolResult != nil:
		return encodeToolResultPart(part, callNames)
	case part.Media != nil:
		return encodeMediaPart(part)
	case part.Reasoning != nil:
		return encodeReasoningPart(part)
	case part.Sealed != nil:
		return encodeSealedPart(part)
	default:
		return nil, false, nil
	}
}

func encodeReasoningPart(part *xir.Part) (json.RawMessage, bool, error) {
	text := part.Text
	signature := ""
	thought := true
	var sealed *xir.Sealed
	if part.Reasoning != nil {
		if part.Reasoning.Text != "" {
			text = part.Reasoning.Text
		}
		signature = part.Reasoning.Signature
		sealed = part.Reasoning.Sealed
	}
	if sealed == nil {
		sealed = part.Sealed
	}
	if sealed != nil && sealed.Protocol == xir.ProtocolGemini {
		switch sealed.Kind {
		case "thoughtSignature":
			thought = false
		case "thought":
			thought = true
		}
		if signature == "" && len(sealed.Raw) > 0 {
			signature = signatureFromRaw(sealed.Raw)
		}
	}

	partJSON := &partWire{ThoughtSignature: signature}
	if thought {
		partJSON.Thought = true
	}
	if text != "" || (thought && signature == "") {
		partJSON.Text = stringPointer(text)
	}
	return marshalPart(partJSON)
}

func signatureFromRaw(raw json.RawMessage) string {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	value, ok := object["thoughtSignature"]
	if !ok {
		return ""
	}
	var signature string
	if err := json.Unmarshal(value, &signature); err != nil {
		return ""
	}
	return signature
}

func encodeToolCallPart(part *xir.Part) (json.RawMessage, bool, error) {
	if part.ToolCall == nil {
		return nil, false, nil
	}
	args, err := functionCallArgs(part.ToolCall)
	if err != nil {
		return nil, false, err
	}
	partJSON := &partWire{
		FunctionCall: &functionCallWire{Name: part.ToolCall.Name, Args: args},
	}
	applyReasoning(partJSON, part.Reasoning)
	return marshalPart(partJSON)
}

func encodeToolResultPart(part *xir.Part, callNames map[string]string) (json.RawMessage, bool, error) {
	if part.ToolResult == nil {
		return nil, false, nil
	}
	response, err := functionResponseValue(part.ToolResult)
	if err != nil {
		return nil, false, err
	}
	name := part.ToolResult.Name
	if name == "" && callNames != nil {
		name = callNames[part.ToolResult.CallID]
	}
	partJSON := &partWire{
		FunctionResponse: &functionResponseWire{Name: name, Response: response},
	}
	applyReasoning(partJSON, part.Reasoning)
	return marshalPart(partJSON)
}

func applyReasoning(part *partWire, reasoning *xir.Reasoning) {
	if reasoning == nil {
		return
	}
	if reasoning.Signature != "" {
		part.ThoughtSignature = reasoning.Signature
	}
	if reasoning.Sealed != nil && reasoning.Sealed.Protocol == xir.ProtocolGemini {
		if reasoning.Sealed.Kind == "thought" {
			part.Thought = true
		}
		if part.ThoughtSignature == "" {
			part.ThoughtSignature = signatureFromRaw(reasoning.Sealed.Raw)
		}
	}
}

func encodeMediaPart(part *xir.Part) (json.RawMessage, bool, error) {
	media := part.Media
	if media == nil {
		return nil, false, nil
	}
	if media.Data == "" && media.URL == "" {
		return nil, false, nil
	}
	partJSON := &partWire{}
	if media.Data != "" {
		partJSON.InlineData = &inlineDataWire{MimeType: media.MediaType, Data: media.Data}
	} else {
		partJSON.FileData = &fileDataWire{MimeType: media.MediaType, FileURI: media.URL}
	}
	applyReasoning(partJSON, part.Reasoning)
	return marshalPart(partJSON)
}

func encodeSealedPart(part *xir.Part) (json.RawMessage, bool, error) {
	if part.Sealed == nil || part.Sealed.Protocol != xir.ProtocolGemini || len(part.Sealed.Raw) == 0 {
		return nil, false, nil
	}
	raw := bytes.TrimSpace(part.Sealed.Raw)
	if len(raw) == 0 || !json.Valid(raw) || raw[0] != '{' {
		return nil, false, nil
	}
	return cloneRaw(raw), true, nil
}

func functionCallArgs(call *xir.ToolCall) (json.RawMessage, error) {
	raw := call.Args
	if len(bytes.TrimSpace(raw)) == 0 && strings.TrimSpace(call.ArgsText) != "" {
		raw = []byte(call.ArgsText)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return json.RawMessage("{}"), nil
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("gemini: functionCall args are not valid JSON")
	}
	if raw[0] != '{' {
		return nil, fmt.Errorf("gemini: functionCall args must be a JSON object, got %c", raw[0])
	}
	return cloneRaw(raw), nil
}

func functionResponseValue(result *xir.ToolResult) (json.RawMessage, error) {
	if result == nil {
		return json.RawMessage("{}"), nil
	}
	raw := bytes.TrimSpace(result.JSON)
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if !json.Valid(raw) {
			return nil, fmt.Errorf("gemini: functionResponse response is not valid JSON")
		}
		if raw[0] == '{' {
			return cloneRaw(raw), nil
		}
		// Gemini defines response as a Struct; wrap a scalar/array rather
		// than emitting a type the API rejects.
		wrapped, err := json.Marshal(map[string]json.RawMessage{"result": raw})
		if err != nil {
			return nil, fmt.Errorf("gemini: encode functionResponse response: %w", err)
		}
		return wrapped, nil
	}
	if result.Text != "" {
		wrapped, err := json.Marshal(map[string]string{"result": result.Text})
		if err != nil {
			return nil, fmt.Errorf("gemini: encode functionResponse response: %w", err)
		}
		return wrapped, nil
	}
	if text := textFromParts(result.Content); text != "" {
		wrapped, err := json.Marshal(map[string]string{"result": text})
		if err != nil {
			return nil, fmt.Errorf("gemini: encode functionResponse response: %w", err)
		}
		return wrapped, nil
	}
	return json.RawMessage("{}"), nil
}

func textFromParts(parts []xir.Part) string {
	var builder strings.Builder
	for i := range parts {
		switch parts[i].Kind {
		case xir.PartText:
			builder.WriteString(parts[i].Text)
		case xir.PartReasoning:
			if parts[i].Reasoning != nil {
				builder.WriteString(parts[i].Reasoning.Text)
			} else {
				builder.WriteString(parts[i].Text)
			}
		}
	}
	return builder.String()
}

func toolCallNames(req *xir.Request) map[string]string {
	names := make(map[string]string)
	for i := range req.Messages {
		for j := range req.Messages[i].Parts {
			call := req.Messages[i].Parts[j].ToolCall
			if call != nil && call.ID != "" && call.Name != "" {
				names[call.ID] = call.Name
			}
		}
	}
	return names
}

func encodeTools(tools []xir.Tool) ([]toolWire, error) {
	declarations := make([]functionDeclarationWire, 0, len(tools))
	for i := range tools {
		tool := &tools[i]
		if tool.Name == "" {
			continue
		}
		if tool.Kind != "" && tool.Kind != "function" && tool.Kind != "custom" {
			// Gemini's generateContent functionDeclarations cannot express
			// hosted/server-side tools, so an unsupported tool kind is not
			// invented as a function.
			continue
		}
		parameters, err := encodeToolSchema(tool.Schema)
		if err != nil {
			return nil, err
		}
		declarations = append(declarations, functionDeclarationWire{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  parameters,
		})
	}
	if len(declarations) == 0 {
		return nil, nil
	}
	return []toolWire{{FunctionDeclarations: declarations}}, nil
}

func encodeToolSchema(schema json.RawMessage) (json.RawMessage, error) {
	raw := bytes.TrimSpace(schema)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("gemini: tool schema is not valid JSON")
	}
	if raw[0] != '{' {
		return nil, fmt.Errorf("gemini: tool schema must be a JSON object")
	}
	return cloneRaw(raw), nil
}

func encodeToolChoice(choice xir.ToolChoice) *toolConfigWire {
	mode := strings.ToLower(strings.TrimSpace(choice.Mode))
	name := strings.TrimSpace(choice.Name)
	if mode == "" && name == "" {
		return nil
	}
	config := &functionCallingConfigWire{}
	switch mode {
	case "auto":
		config.Mode = "AUTO"
	case "none":
		config.Mode = "NONE"
	case "required", "any":
		config.Mode = "ANY"
	case "tool":
		config.Mode = "ANY"
		if name != "" {
			config.AllowedFunctionNames = []string{name}
		}
	default:
		config.Mode = "ANY"
		if name != "" {
			config.AllowedFunctionNames = []string{name}
		}
	}
	return &toolConfigWire{FunctionCallingConfig: config}
}

func encodeGenerationConfig(params xir.Params) (json.RawMessage, error) {
	fields := make(map[string]json.RawMessage)

	addFloat(fields, "temperature", params.Temperature)
	addFloat(fields, "topP", params.TopP)
	addInt(fields, "topK", params.TopK)
	maxTokens := params.MaxTokens
	if maxTokens == nil {
		maxTokens = params.MaxCompletion
	}
	addInt(fields, "maxOutputTokens", maxTokens)
	if len(params.Stop) > 0 {
		raw, err := json.Marshal(params.Stop)
		if err != nil {
			return nil, fmt.Errorf("gemini: encode stopSequences: %w", err)
		}
		fields["stopSequences"] = raw
	}
	if raw, err := encodeResponseMimeType(params.Extra[ParamResponseMimeType]); err != nil {
		return nil, err
	} else if raw != nil {
		fields[ParamResponseMimeType] = raw
	}
	if raw := params.Extra[ParamResponseSchema]; len(bytes.TrimSpace(raw)) > 0 {
		if !json.Valid(raw) {
			return nil, fmt.Errorf("gemini: responseSchema is not valid JSON")
		}
		fields[ParamResponseSchema] = cloneRaw(bytes.TrimSpace(raw))
	}
	addInt64(fields, "seed", params.Seed)
	addFloat(fields, "presencePenalty", params.PresencePenalty)
	addFloat(fields, "frequencyPenalty", params.FrequencyPenalty)

	thinking := make(map[string]json.RawMessage)
	addInt(thinking, "thinkingBudget", params.ThinkingBudget)
	addBool(thinking, "includeThoughts", params.Thinking)
	if effort := strings.TrimSpace(params.ReasoningEffort); effort != "" {
		// Gemini spells the level as an enum; OpenAI-style lower-case effort
		// values still produce a valid Gemini request.
		raw, err := json.Marshal(strings.ToUpper(effort))
		if err != nil {
			return nil, fmt.Errorf("gemini: encode thinkingLevel: %w", err)
		}
		thinking["thinkingLevel"] = raw
	}
	for key, raw := range params.Extra {
		if !strings.HasPrefix(key, "thinkingConfig.") {
			continue
		}
		name := strings.TrimPrefix(key, "thinkingConfig.")
		if name == "" {
			continue
		}
		if name == "thinkingLevel" && params.ReasoningEffort != "" {
			continue
		}
		if raw = bytes.TrimSpace(raw); len(raw) == 0 {
			continue
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("gemini: thinkingConfig.%s is not valid JSON", name)
		}
		thinking[name] = cloneRaw(raw)
	}
	if len(thinking) > 0 {
		raw, err := json.Marshal(thinking)
		if err != nil {
			return nil, fmt.Errorf("gemini: encode thinkingConfig: %w", err)
		}
		fields["thinkingConfig"] = raw
	}

	reserved := map[string]bool{
		"temperature":         true,
		"topP":                true,
		"topK":                true,
		"maxOutputTokens":     true,
		"stopSequences":       true,
		ParamResponseMimeType: true,
		ParamResponseSchema:   true,
		"seed":                true,
		"presencePenalty":     true,
		"frequencyPenalty":    true,
		"thinkingConfig":      true,
	}
	for key, raw := range params.Extra {
		if reserved[key] || strings.HasPrefix(key, "thinkingConfig.") {
			continue
		}
		if raw = bytes.TrimSpace(raw); len(raw) == 0 {
			continue
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("gemini: generationConfig.%s is not valid JSON", key)
		}
		fields[key] = cloneRaw(raw)
	}

	if len(fields) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("gemini: encode generationConfig: %w", err)
	}
	return raw, nil
}

func encodeResponseMimeType(raw json.RawMessage) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return cloneRaw(raw), nil
	}
	// Accept an unquoted literal and quote it instead of emitting invalid
	// JSON. A numeric or object value is a caller error.
	quoted, err := json.Marshal(string(raw))
	if err != nil {
		return nil, fmt.Errorf("gemini: encode responseMimeType: %w", err)
	}
	return quoted, nil
}

func validateSafetySettingsRaw(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return fmt.Errorf("gemini: safetySettings must be a JSON array: %w", err)
	}
	return nil
}

func addFloat(fields map[string]json.RawMessage, key string, value *float64) {
	if value == nil {
		return
	}
	raw, err := json.Marshal(*value)
	if err == nil {
		fields[key] = raw
	}
}

func addInt(fields map[string]json.RawMessage, key string, value *int) {
	if value == nil {
		return
	}
	raw, err := json.Marshal(*value)
	if err == nil {
		fields[key] = raw
	}
}

func addInt64(fields map[string]json.RawMessage, key string, value *int64) {
	if value == nil {
		return
	}
	raw, err := json.Marshal(*value)
	if err == nil {
		fields[key] = raw
	}
}

func addBool(fields map[string]json.RawMessage, key string, value *bool) {
	if value == nil {
		return
	}
	raw, err := json.Marshal(*value)
	if err == nil {
		fields[key] = raw
	}
}

func marshalPart(part *partWire) (json.RawMessage, bool, error) {
	raw, err := json.Marshal(part)
	if err != nil {
		return nil, false, fmt.Errorf("gemini: encode part: %w", err)
	}
	return raw, true, nil
}

func stringPointer(value string) *string { return &value }
