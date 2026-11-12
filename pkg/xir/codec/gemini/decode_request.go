package gemini

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// decodePart turns one Gemini Part into one neutral Part. Gemini can place a
// thoughtSignature next to text, a function call or media; the signature is
// kept as reasoning metadata on that part so the call stays a call. A part
// that is only a thought or only a signature becomes PartReasoning.
func decodePart(node *wire.Node, path string) (xir.Part, error) {
	if node.Kind != wire.Object {
		return xir.Part{}, fmt.Errorf("gemini: %s: expected part object, got %s", path, kindName(node.Kind))
	}

	text, hasText, err := stringField(node, "text", path)
	if err != nil {
		return xir.Part{}, err
	}
	thought, _, err := boolField(node, "thought", path)
	if err != nil {
		return xir.Part{}, err
	}
	signature, _, err := stringField(node, "thoughtSignature", path)
	if err != nil {
		return xir.Part{}, err
	}

	var reasoning *xir.Reasoning
	if thought || signature != "" {
		reasoning = &xir.Reasoning{
			Text:      text,
			Signature: signature,
			Sealed: &xir.Sealed{
				Protocol:      xir.ProtocolGemini,
				Kind:          reasoningSealedKind(thought, signature),
				Raw:           cloneRaw(node.Raw()),
				MustRoundTrip: true,
			},
		}
	}

	if call, ok, err := decodeFunctionCall(node, path); err != nil {
		return xir.Part{}, err
	} else if ok {
		return xir.Part{
			Kind:      xir.PartToolCall,
			ToolCall:  call,
			Reasoning: reasoning,
			Sealed:    sealedOf(reasoning),
			Ref:       node,
		}, nil
	}

	if result, ok, err := decodeFunctionResponse(node, path); err != nil {
		return xir.Part{}, err
	} else if ok {
		return xir.Part{
			Kind:       xir.PartToolResult,
			ToolResult: result,
			Reasoning:  reasoning,
			Sealed:     sealedOf(reasoning),
			Ref:        node,
		}, nil
	}

	if media, kind, ok, err := decodeInlineData(node, path); err != nil {
		return xir.Part{}, err
	} else if ok {
		return xir.Part{
			Kind:      partKindForMedia(kind),
			Media:     media,
			Reasoning: reasoning,
			Sealed:    sealedOf(reasoning),
			Ref:       node,
		}, nil
	}

	if media, kind, ok, err := decodeFileData(node, path); err != nil {
		return xir.Part{}, err
	} else if ok {
		return xir.Part{
			Kind:      partKindForMedia(kind),
			Media:     media,
			Reasoning: reasoning,
			Sealed:    sealedOf(reasoning),
			Ref:       node,
		}, nil
	}

	if reasoning != nil {
		return xir.Part{
			Kind:      xir.PartReasoning,
			Text:      text,
			Reasoning: reasoning,
			Sealed:    sealedOf(reasoning),
			Ref:       node,
		}, nil
	}

	if hasText {
		return xir.Part{Kind: xir.PartText, Text: text, Ref: node}, nil
	}

	// Preserve a Gemini-native part the neutral model does not name, for
	// example executableCode or codeExecutionResult, as a sealed block.
	if node.Len() > 0 {
		return xir.Part{
			Kind: xir.PartSealed,
			Sealed: &xir.Sealed{
				Protocol: xir.ProtocolGemini,
				Kind:     "part",
				Raw:      cloneRaw(node.Raw()),
			},
			Ref: node,
		}, nil
	}
	return xir.Part{Kind: xir.PartUnknown, Ref: node}, nil
}

func sealedOf(reasoning *xir.Reasoning) *xir.Sealed {
	if reasoning == nil {
		return nil
	}
	return reasoning.Sealed
}

func reasoningSealedKind(thought bool, signature string) string {
	if thought {
		return "thought"
	}
	if signature != "" {
		return "thoughtSignature"
	}
	return "part"
}

func decodeFunctionCall(node *wire.Node, path string) (*xir.ToolCall, bool, error) {
	callNode, ok, err := objectField(node, "functionCall", path)
	if err != nil || !ok {
		return nil, ok, err
	}
	callPath := path + ".functionCall"
	name, _, err := stringField(callNode, "name", callPath)
	if err != nil {
		return nil, false, err
	}
	call := &xir.ToolCall{Name: name, Type: "function", Ref: callNode}
	argsNode, ok := callNode.Get("args")
	if ok && argsNode.Kind != wire.Null {
		if argsNode.Kind != wire.Object {
			return nil, false, fmt.Errorf("gemini: %s.args: expected object, got %s", callPath, kindName(argsNode.Kind))
		}
		call.Args = cloneRaw(argsNode.Raw())
		call.ArgsText = compactJSON(argsNode.Raw())
	}
	return call, true, nil
}

func decodeFunctionResponse(node *wire.Node, path string) (*xir.ToolResult, bool, error) {
	responseNode, ok, err := objectField(node, "functionResponse", path)
	if err != nil || !ok {
		return nil, ok, err
	}
	responsePath := path + ".functionResponse"
	name, _, err := stringField(responseNode, "name", responsePath)
	if err != nil {
		return nil, false, err
	}
	result := &xir.ToolResult{Name: name, Ref: responseNode}
	valueNode, ok := responseNode.Get("response")
	if ok && valueNode.Kind != wire.Null {
		if valueNode.Kind != wire.Object {
			return nil, false, fmt.Errorf("gemini: %s.response: expected object, got %s", responsePath, kindName(valueNode.Kind))
		}
		result.JSON = cloneRaw(valueNode.Raw())
		if text, ok := valueNode.StringField("result"); ok {
			result.Text = text
		}
	}
	return result, true, nil
}

func decodeInlineData(node *wire.Node, path string) (*xir.Media, xir.MediaKind, bool, error) {
	blob, ok, err := objectField(node, "inlineData", path)
	if err != nil || !ok {
		return nil, "", false, err
	}
	blobPath := path + ".inlineData"
	mimeType, _, err := stringField(blob, "mimeType", blobPath)
	if err != nil {
		return nil, "", false, err
	}
	data, _, err := stringField(blob, "data", blobPath)
	if err != nil {
		return nil, "", false, err
	}
	kind := mediaKindForMime(mimeType, xir.MediaFile)
	return &xir.Media{Kind: kind, Data: data, MediaType: mimeType}, kind, true, nil
}

func decodeFileData(node *wire.Node, path string) (*xir.Media, xir.MediaKind, bool, error) {
	file, ok, err := objectField(node, "fileData", path)
	if err != nil || !ok {
		return nil, "", false, err
	}
	filePath := path + ".fileData"
	mimeType, _, err := stringField(file, "mimeType", filePath)
	if err != nil {
		return nil, "", false, err
	}
	uri, _, err := stringField(file, "fileUri", filePath)
	if err != nil {
		return nil, "", false, err
	}
	kind := mediaKindForMime(mimeType, xir.MediaFile)
	return &xir.Media{Kind: kind, URL: uri, MediaType: mimeType}, kind, true, nil
}

func mediaKindForMime(mimeType string, fallback xir.MediaKind) xir.MediaKind {
	mime := strings.ToLower(strings.TrimSpace(mimeType))
	switch {
	case strings.HasPrefix(mime, "image/"):
		return xir.MediaImage
	case strings.HasPrefix(mime, "audio/"):
		return xir.MediaAudio
	case strings.HasPrefix(mime, "video/"):
		return xir.MediaVideo
	case mime != "":
		return xir.MediaFile
	default:
		return fallback
	}
}

func partKindForMedia(kind xir.MediaKind) xir.PartKind {
	switch kind {
	case xir.MediaImage:
		return xir.PartImage
	case xir.MediaAudio:
		return xir.PartAudio
	case xir.MediaVideo:
		return xir.PartVideo
	default:
		return xir.PartFile
	}
}

func decodeTools(root *wire.Node) ([]xir.Tool, error) {
	toolNodes, ok, err := arrayField(root, "tools", "$")
	if err != nil || !ok {
		return nil, err
	}
	var tools []xir.Tool
	for i, toolNode := range toolNodes {
		toolPath := fmt.Sprintf("$.tools[%d]", i)
		if toolNode.Kind != wire.Object {
			return nil, fmt.Errorf("gemini: %s: expected tool object, got %s", toolPath, kindName(toolNode.Kind))
		}
		declNodes, ok, err := arrayField(toolNode, "functionDeclarations", toolPath)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		for j, declNode := range declNodes {
			declPath := fmt.Sprintf("%s.functionDeclarations[%d]", toolPath, j)
			if declNode.Kind != wire.Object {
				return nil, fmt.Errorf("gemini: %s: expected declaration object, got %s", declPath, kindName(declNode.Kind))
			}
			name, _, err := stringField(declNode, "name", declPath)
			if err != nil {
				return nil, err
			}
			description, _, err := stringField(declNode, "description", declPath)
			if err != nil {
				return nil, err
			}
			schema, err := decodeToolSchema(declNode, declPath)
			if err != nil {
				return nil, err
			}
			tools = append(tools, xir.Tool{
				Name:        name,
				Description: description,
				Schema:      schema,
				Kind:        "function",
				Ref:         declNode,
			})
		}
	}
	return tools, nil
}

func decodeToolSchema(declNode *wire.Node, path string) (json.RawMessage, error) {
	for _, key := range []string{"parameters", "parametersJsonSchema"} {
		value, ok := declNode.Get(key)
		if !ok || value.Kind == wire.Null {
			continue
		}
		if value.Kind != wire.Object {
			return nil, fmt.Errorf("gemini: %s.%s: expected object, got %s", path, key, kindName(value.Kind))
		}
		return cloneRaw(value.Raw()), nil
	}
	return nil, nil
}

func decodeToolChoice(root *wire.Node) (xir.ToolChoice, error) {
	toolConfig, ok, err := objectField(root, "toolConfig", "$")
	if err != nil || !ok {
		return xir.ToolChoice{}, err
	}
	config, ok, err := objectField(toolConfig, "functionCallingConfig", "$.toolConfig")
	if err != nil || !ok {
		return xir.ToolChoice{}, err
	}
	configPath := "$.toolConfig.functionCallingConfig"
	mode, _, err := stringField(config, "mode", configPath)
	if err != nil {
		return xir.ToolChoice{}, err
	}
	allowed, _, err := stringArrayField(config, "allowedFunctionNames", configPath)
	if err != nil {
		return xir.ToolChoice{}, err
	}
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "AUTO":
		return xir.ToolChoice{Mode: "auto"}, nil
	case "NONE":
		return xir.ToolChoice{Mode: "none"}, nil
	case "ANY":
		if len(allowed) == 1 {
			return xir.ToolChoice{Mode: "tool", Name: allowed[0]}, nil
		}
		return xir.ToolChoice{Mode: "required"}, nil
	default:
		return xir.ToolChoice{}, nil
	}
}

func decodeGenerationConfig(root *wire.Node, params *xir.Params) error {
	config, ok, err := objectField(root, "generationConfig", "$")
	if err != nil || !ok {
		return err
	}
	path := "$.generationConfig"

	if v, ok, err := floatField(config, "temperature", path); err != nil {
		return err
	} else if ok {
		params.Temperature = &v
	}
	if v, ok, err := floatField(config, "topP", path); err != nil {
		return err
	} else if ok {
		params.TopP = &v
	}
	if v, ok, err := intField(config, "topK", path); err != nil {
		return err
	} else if ok {
		params.TopK = &v
	}
	if v, ok, err := intField(config, "maxOutputTokens", path); err != nil {
		return err
	} else if ok {
		params.MaxTokens = &v
	}
	if values, ok, err := stringArrayField(config, "stopSequences", path); err != nil {
		return err
	} else if ok {
		params.Stop = values
	}
	if v, ok, err := int64Field(config, "seed", path); err != nil {
		return err
	} else if ok {
		params.Seed = &v
	}
	if v, ok, err := floatField(config, "presencePenalty", path); err != nil {
		return err
	} else if ok {
		params.PresencePenalty = &v
	}
	if v, ok, err := floatField(config, "frequencyPenalty", path); err != nil {
		return err
	} else if ok {
		params.FrequencyPenalty = &v
	}

	if value, ok := config.Get(ParamResponseMimeType); ok && value.Kind != wire.Null {
		if value.Kind != wire.String {
			return fmt.Errorf("gemini: %s.%s: expected string, got %s", path, ParamResponseMimeType, kindName(value.Kind))
		}
		setParamExtra(params, ParamResponseMimeType, value.Raw())
	}
	if value, ok := config.Get(ParamResponseSchema); ok && value.Kind != wire.Null {
		if value.Kind != wire.Object {
			return fmt.Errorf("gemini: %s.%s: expected object, got %s", path, ParamResponseSchema, kindName(value.Kind))
		}
		setParamExtra(params, ParamResponseSchema, value.Raw())
	}

	thinking, ok, err := objectField(config, "thinkingConfig", path)
	if err != nil {
		return err
	}
	if ok {
		thinkingPath := path + ".thinkingConfig"
		if v, ok, err := intField(thinking, "thinkingBudget", thinkingPath); err != nil {
			return err
		} else if ok {
			params.ThinkingBudget = &v
		}
		if v, ok, err := boolField(thinking, "includeThoughts", thinkingPath); err != nil {
			return err
		} else if ok {
			params.Thinking = &v
		}
		if level, ok, err := stringField(thinking, "thinkingLevel", thinkingPath); err != nil {
			return err
		} else if ok {
			params.ReasoningEffort = level
			if value, present := thinking.Get("thinkingLevel"); present {
				// Keep the raw token too: the neutral effort string cannot
				// express a future enum value byte for byte.
				setParamExtra(params, "thinkingConfig.thinkingLevel", value.Raw())
			}
		}
		for _, member := range thinking.Members {
			switch member.Key {
			case "thinkingBudget", "includeThoughts", "thinkingLevel":
				continue
			}
			if member.Value.Kind == wire.Null {
				continue
			}
			setParamExtra(params, "thinkingConfig."+member.Key, member.Value.Raw())
		}
	}

	known := map[string]bool{
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
	for _, member := range config.Members {
		if known[member.Key] || member.Value.Kind == wire.Null {
			continue
		}
		setParamExtra(params, member.Key, member.Value.Raw())
	}
	return nil
}

func setParamExtra(params *xir.Params, key string, raw []byte) {
	if params.Extra == nil {
		params.Extra = make(map[string]json.RawMessage)
	}
	params.Extra[key] = cloneRaw(raw)
}

func validateSafetySettings(root *wire.Node) error {
	value, ok := root.Get("safetySettings")
	if !ok || value.Kind == wire.Null {
		return nil
	}
	if value.Kind != wire.Array {
		return fmt.Errorf("gemini: $.safetySettings: expected array, got %s", kindName(value.Kind))
	}
	return nil
}
