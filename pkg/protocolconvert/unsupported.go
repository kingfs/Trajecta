package protocolconvert

import (
	"encoding/json"
	"fmt"
	"strings"
)

// unsupportedContentField inspects the structured request body and reports the
// first content shape the conversion IR cannot represent. It replaces a byte
// substring blacklist: that approach rejected ordinary prose mentioning
// "image_url" and still missed media shapes it did not list, and it could not
// see the difference between a content part and a tool schema property.
func unsupportedContentField(body []byte, from Protocol) (string, bool) {
	switch canonical(from) {
	case ProtocolChatCompletions:
		return unsupportedChatContent(body)
	case ProtocolResponses:
		return unsupportedResponsesContent(body)
	case ProtocolMessages:
		return unsupportedMessagesContent(body)
	case ProtocolGemini:
		return unsupportedGeminiContent(body)
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Chat Completions

func unsupportedChatContent(body []byte) (string, bool) {
	var payload struct {
		Messages []struct {
			Role         string          `json:"role"`
			Content      json.RawMessage `json:"content"`
			FunctionCall json.RawMessage `json:"function_call"`
			ToolCalls    []struct {
				Type string `json:"type"`
			} `json:"tool_calls"`
			ReasoningContent json.RawMessage `json:"reasoning_content"`
			Reasoning        json.RawMessage `json:"reasoning"`
			Signature        json.RawMessage `json:"signature"`
		} `json:"messages"`
		Tools []struct {
			Type string `json:"type"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false
	}
	for i := range payload.Tools {
		if kind := strings.TrimSpace(payload.Tools[i].Type); kind != "" && !strings.EqualFold(kind, "function") {
			return fmt.Sprintf("tools[%d].type=%s (hosted tool)", i, kind), true
		}
	}
	for i := range payload.Messages {
		message := &payload.Messages[i]
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "", "user", "assistant", "system", "developer", "tool", "function":
		default:
			return fmt.Sprintf("messages[%d].role=%s", i, message.Role), true
		}
		for j := range message.ToolCalls {
			if kind := strings.TrimSpace(message.ToolCalls[j].Type); kind != "" && !strings.EqualFold(kind, "function") {
				return fmt.Sprintf("messages[%d].tool_calls[%d].type=%s", i, j, kind), true
			}
		}
		if rawPresent(message.FunctionCall) {
			return fmt.Sprintf("messages[%d].function_call", i), true
		}
		// Reasoning and opaque signatures travel on the message in several
		// provider dialects; the IR has no faithful representation for them.
		if rawPresent(message.ReasoningContent) {
			return fmt.Sprintf("messages[%d].reasoning_content", i), true
		}
		if rawPresent(message.Reasoning) {
			return fmt.Sprintf("messages[%d].reasoning", i), true
		}
		if rawPresent(message.Signature) {
			return fmt.Sprintf("messages[%d].signature", i), true
		}
		field, bad := unsupportedChatContentValue(message.Content)
		if bad {
			return fmt.Sprintf("messages[%d].%s", i, field), true
		}
	}
	return "", false
}

func unsupportedChatContentValue(raw json.RawMessage) (string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", false
	}
	if strings.HasPrefix(trimmed, `"`) {
		return "", false
	}
	if !strings.HasPrefix(trimmed, "[") {
		return "content", true
	}
	var parts []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "content", true
	}
	for _, part := range parts {
		if !strings.EqualFold(strings.TrimSpace(part.Type), "text") {
			return fmt.Sprintf("content[].type=%s", part.Type), true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// OpenAI Responses

func unsupportedResponsesContent(body []byte) (string, bool) {
	var payload struct {
		Input              json.RawMessage `json:"input"`
		PreviousResponseID string          `json:"previous_response_id"`
		Tools              []struct {
			Type string `json:"type"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false
	}
	if strings.TrimSpace(payload.PreviousResponseID) != "" {
		return "previous_response_id", true
	}
	for i := range payload.Tools {
		if kind := strings.TrimSpace(payload.Tools[i].Type); kind != "" && !strings.EqualFold(kind, "function") {
			return fmt.Sprintf("tools[%d].type=%s (hosted tool)", i, kind), true
		}
	}
	return unsupportedResponsesInput(payload.Input)
}

func unsupportedResponsesInput(raw json.RawMessage) (string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", false
	}
	if strings.HasPrefix(trimmed, `"`) {
		return "", false
	}
	if strings.HasPrefix(trimmed, "{") {
		return unsupportedResponsesItem(raw, "input")
	}
	if !strings.HasPrefix(trimmed, "[") {
		return "input", true
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return "input", true
	}
	for index, item := range items {
		if field, bad := unsupportedResponsesItem(item, fmt.Sprintf("input[%d]", index)); bad {
			return field, true
		}
	}
	return "", false
}

func unsupportedResponsesItem(raw json.RawMessage, prefix string) (string, bool) {
	var item struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return prefix, true
	}
	switch strings.TrimSpace(item.Type) {
	case "message":
		switch strings.ToLower(strings.TrimSpace(item.Role)) {
		case "", "user", "assistant", "system", "developer":
		default:
			return fmt.Sprintf("%s.role=%s", prefix, item.Role), true
		}
		for _, part := range item.Content {
			switch strings.TrimSpace(part.Type) {
			case "input_text", "output_text", "text", "refusal":
			default:
				return fmt.Sprintf("%s.content[].type=%s", prefix, part.Type), true
			}
		}
		return "", false
	case "function_call", "function_call_output":
		return "", false
	default:
		return fmt.Sprintf("%s.type=%s", prefix, item.Type), true
	}
}

// ---------------------------------------------------------------------------
// Anthropic Messages

func unsupportedMessagesContent(body []byte) (string, bool) {
	var payload struct {
		System json.RawMessage `json:"system"`
		Tools  []struct {
			Type string `json:"type"`
		} `json:"tools"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false
	}
	if field, bad := unsupportedAnthropicBlocks(payload.System, "system"); bad {
		return field, true
	}
	for i := range payload.Tools {
		if kind := strings.TrimSpace(payload.Tools[i].Type); kind != "" && !strings.EqualFold(kind, "custom") {
			return fmt.Sprintf("tools[%d].type=%s (server tool)", i, kind), true
		}
	}
	for i := range payload.Messages {
		if role := strings.ToLower(strings.TrimSpace(payload.Messages[i].Role)); role != "" && role != "user" && role != "assistant" {
			return fmt.Sprintf("messages[%d].role=%s", i, payload.Messages[i].Role), true
		}
		if field, bad := unsupportedAnthropicBlocks(payload.Messages[i].Content, fmt.Sprintf("messages[%d].content", i)); bad {
			return field, true
		}
	}
	return "", false
}

func unsupportedAnthropicBlocks(raw json.RawMessage, prefix string) (string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", false
	}
	if strings.HasPrefix(trimmed, `"`) {
		return "", false
	}
	if !strings.HasPrefix(trimmed, "[") {
		return prefix, true
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return prefix, true
	}
	for index := range blocks {
		block := &blocks[index]
		switch strings.TrimSpace(block.Type) {
		case "text", "tool_use":
		case "tool_result":
			if field, bad := unsupportedAnthropicToolResult(block.Content, fmt.Sprintf("%s[%d].content", prefix, index)); bad {
				return field, true
			}
		default:
			return fmt.Sprintf("%s[%d].type=%s", prefix, index, block.Type), true
		}
	}
	return "", false
}

func unsupportedAnthropicToolResult(raw json.RawMessage, prefix string) (string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || strings.HasPrefix(trimmed, `"`) {
		return "", false
	}
	if !strings.HasPrefix(trimmed, "[") {
		return prefix, true
	}
	var parts []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return prefix, true
	}
	for _, part := range parts {
		if !strings.EqualFold(strings.TrimSpace(part.Type), "text") {
			return fmt.Sprintf("%s[].type=%s", prefix, part.Type), true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Gemini generateContent

func unsupportedGeminiContent(body []byte) (string, bool) {
	var payload struct {
		Contents []struct {
			Role  string            `json:"role"`
			Parts []json.RawMessage `json:"parts"`
		} `json:"contents"`
		SystemInstruction *struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"systemInstruction"`
		Tools                 []map[string]json.RawMessage `json:"tools"`
		GenerationConfig      map[string]json.RawMessage   `json:"generationConfig"`
		GenerationConfigSnake json.RawMessage              `json:"generation_config"`
		SystemInstrSnake      json.RawMessage              `json:"system_instruction"`
		SafetySettingsSnake   json.RawMessage              `json:"safety_settings"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false
	}
	// Gemini's wire spelling is camelCase; a snake_case key would be silently
	// ignored by the adapter, so reject it rather than lose the setting.
	if rawPresent(payload.GenerationConfigSnake) {
		return "generation_config", true
	}
	if rawPresent(payload.SystemInstrSnake) {
		return "system_instruction", true
	}
	if rawPresent(payload.SafetySettingsSnake) {
		return "safety_settings", true
	}
	for i := range payload.Contents {
		if role := strings.ToLower(strings.TrimSpace(payload.Contents[i].Role)); role != "" && role != "user" && role != "model" {
			return fmt.Sprintf("contents[%d].role=%s", i, payload.Contents[i].Role), true
		}
		if field, bad := unsupportedGeminiParts(payload.Contents[i].Parts, fmt.Sprintf("contents[%d].parts", i)); bad {
			return field, true
		}
	}
	if payload.SystemInstruction != nil {
		if field, bad := unsupportedGeminiParts(payload.SystemInstruction.Parts, "systemInstruction.parts"); bad {
			return field, true
		}
	}
	for i, tool := range payload.Tools {
		for key := range tool {
			if key != "functionDeclarations" {
				return fmt.Sprintf("tools[%d].%s", i, key), true
			}
		}
	}
	for key := range payload.GenerationConfig {
		switch key {
		case "temperature", "topP", "topK", "maxOutputTokens", "stopSequences":
		default:
			return fmt.Sprintf("generationConfig.%s", key), true
		}
	}
	return "", false
}

func unsupportedGeminiParts(parts []json.RawMessage, prefix string) (string, bool) {
	for index, raw := range parts {
		var part map[string]json.RawMessage
		if err := json.Unmarshal(raw, &part); err != nil {
			return fmt.Sprintf("%s[%d]", prefix, index), true
		}
		for key, value := range part {
			switch key {
			case "text":
			case "thought":
				// A provider may spell the default explicitly; only a true
				// value is reasoning the IR cannot carry.
				var thought bool
				if err := json.Unmarshal(value, &thought); err != nil || thought {
					return fmt.Sprintf("%s[%d].thought", prefix, index), true
				}
			case "thoughtSignature":
				trimmed := strings.TrimSpace(string(value))
				if trimmed != "" && trimmed != "null" && trimmed != `""` {
					return fmt.Sprintf("%s[%d].thoughtSignature", prefix, index), true
				}
			default:
				return fmt.Sprintf("%s[%d].%s", prefix, index, key), true
			}
		}
	}
	return "", false
}

func rawPresent(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}
