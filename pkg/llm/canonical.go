package llm

import (
	"strings"
)

// canonicalRole folds the provider spellings of a message role onto the four
// roles the IR understands. Gemini calls the assistant role "model"; OpenAI
// Responses still emits "developer" for the system prompt; a legacy Chat
// function message is a tool result.
func canonicalRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant", "model":
		return "assistant"
	case "user":
		return "user"
	case "system", "developer":
		return "system"
	case "tool", "function":
		return "tool"
	case "":
		// A missing role is a user turn on every wire surface that allows it.
		return "user"
	default:
		return strings.ToLower(strings.TrimSpace(role))
	}
}

// parseToolChoiceValue accepts every wire spelling the adapters may hand it:
// the Chat/Responses string form, the Chat nested object
// ({"type":"function","function":{"name":...}}) and the Anthropic/Responses
// flat object ({"type":"tool"|"function","name":...}). Unknown shapes are
// dropped rather than guessed.
func parseToolChoiceValue(value any) *ToolChoice {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "auto":
			return &ToolChoice{Mode: "auto"}
		case "none":
			return &ToolChoice{Mode: "none"}
		case "required", "any":
			return &ToolChoice{Mode: "required"}
		}
		return nil
	case map[string]any:
		kind, _ := typed["type"].(string)
		name, _ := typed["name"].(string)
		if name == "" {
			if function, ok := typed["function"].(map[string]any); ok {
				name, _ = function["name"].(string)
			}
		}
		switch strings.ToLower(strings.TrimSpace(kind)) {
		case "auto":
			return &ToolChoice{Mode: "auto"}
		case "none":
			return &ToolChoice{Mode: "none"}
		case "any", "required":
			return &ToolChoice{Mode: "required"}
		case "tool", "function":
			if name == "" {
				return nil
			}
			return &ToolChoice{Mode: "tool", Name: name}
		}
		return nil
	default:
		return nil
	}
}

// encodeChatToolChoice renders the canonical directive in the Chat Completions
// spelling: a string for the modes and an object for a forced function.
func encodeChatToolChoice(choice *ToolChoice) any {
	if choice == nil {
		return nil
	}
	switch choice.Mode {
	case "auto", "none", "required":
		return choice.Mode
	case "tool":
		if choice.Name == "" {
			return nil
		}
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": choice.Name},
		}
	}
	return nil
}

// encodeResponsesToolChoice renders the canonical directive for the Responses
// API: the modes are strings and a forced function is a flat object.
func encodeResponsesToolChoice(choice *ToolChoice) any {
	if choice == nil {
		return nil
	}
	switch choice.Mode {
	case "auto", "none", "required":
		return choice.Mode
	case "tool":
		if choice.Name == "" {
			return nil
		}
		return map[string]any{"type": "function", "name": choice.Name}
	}
	return nil
}

// encodeAnthropicToolChoice renders the canonical directive for Messages,
// which only accepts an object. "required" is spelled "any" there.
func encodeAnthropicToolChoice(choice *ToolChoice) any {
	if choice == nil {
		return nil
	}
	switch choice.Mode {
	case "auto":
		return map[string]any{"type": "auto"}
	case "none":
		return map[string]any{"type": "none"}
	case "required":
		return map[string]any{"type": "any"}
	case "tool":
		if choice.Name == "" {
			return nil
		}
		return map[string]any{"type": "tool", "name": choice.Name}
	}
	return nil
}
