package convert

import (
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
)

// prepareRequest removes protocol-owned values that cannot be re-emitted by
// the target and applies the small role/part normalizations the target codec
// expects. It runs only after the loss ledger has been computed, so every
// removal here has a corresponding loss entry.
func prepareRequest(request *xir.Request, to xir.Protocol) {
	sanitizeParams(&request.Params, to)
	request.System = sanitizeParts(request.System, to, partScope{system: true})
	for i := range request.Messages {
		message := &request.Messages[i]
		scope := partScope{role: message.Role}
		message.Parts = sanitizeParts(message.Parts, to, scope)
	}
	request.Tools = sanitizeTools(request.Tools, to)

	switch to {
	case xir.ProtocolChatCompletions:
		request.Messages = splitToolResultTurns(request.Messages)
	case xir.ProtocolMessages:
		liftSystemMessages(request)
	}
}

// prepareResponse removes protocol-owned or unrepresentable response parts.
func prepareResponse(response *xir.Response, to xir.Protocol) {
	response.Parts = sanitizeParts(response.Parts, to, partScope{inResponse: true})
}

func sanitizeParams(params *xir.Params, to xir.Protocol) {
	if len(params.Extra) == 0 {
		return
	}
	for key := range params.Extra {
		if !targetCarriesParamExtra(to, key) {
			delete(params.Extra, key)
		}
	}
	if len(params.Extra) == 0 {
		params.Extra = nil
	}
}

func sanitizeTools(tools []xir.Tool, to xir.Protocol) []xir.Tool {
	if to != xir.ProtocolGemini || len(tools) == 0 {
		return tools
	}
	out := make([]xir.Tool, 0, len(tools))
	for _, tool := range tools {
		kind := tool.Kind
		if kind == "" {
			kind = "function"
		}
		if kind == "function" || kind == "custom" {
			out = append(out, tool)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sanitizeParts(parts []xir.Part, to xir.Protocol, scope partScope) []xir.Part {
	if len(parts) == 0 {
		return nil
	}
	out := make([]xir.Part, 0, len(parts))
	for i := range parts {
		part := parts[i]
		if !sanitizePart(&part, to, scope) {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sanitizePart mutates one part into the closest target-safe representation.
// It reports false when the part has no representation at all.
func sanitizePart(part *xir.Part, to xir.Protocol, scope partScope) bool {
	if part.Sealed != nil && part.Sealed.Protocol != to {
		part.Sealed = nil
	}
	if part.Reasoning != nil {
		reasoning := part.Reasoning
		if reasoning.Sealed != nil && reasoning.Sealed.Protocol != to {
			reasoning.Sealed = nil
		}
		if reasoning.EncryptedContent != "" && to != xir.ProtocolResponses {
			reasoning.EncryptedContent = ""
		}
		// A signature is bound to the protocol that produced it.
		reasoning.Signature = ""
		if to == xir.ProtocolChatCompletions {
			return false
		}
		if reasoning.Text == "" && len(reasoning.Summary) == 0 && reasoning.Sealed == nil {
			return false
		}
	}

	switch part.Kind {
	case xir.PartSealed:
		return part.Sealed != nil
	case xir.PartUnknown:
		if part.Sealed != nil {
			return true
		}
		if part.Text == "" {
			return false
		}
		part.Kind = xir.PartText
		return true
	case xir.PartReasoning:
		return part.Reasoning != nil
	case xir.PartCitation:
		if part.Text == "" {
			return false
		}
		part.Kind = xir.PartText
		part.Citation = nil
		return true
	case xir.PartSearch:
		return false
	case xir.PartImage, xir.PartFile, xir.PartAudio, xir.PartVideo:
		if part.Media == nil || !mediaCarried(part.Media.Kind, to, scope) {
			return false
		}
		if part.Media.Data == "" && part.Media.URL == "" {
			return false
		}
		if to == xir.ProtocolChatCompletions && !scope.inResponse {
			switch part.Media.Kind {
			case xir.MediaAudio:
				if part.Media.Data == "" {
					return false
				}
			case xir.MediaFile:
				if part.Media.Data == "" {
					return false
				}
			case xir.MediaImage:
				chatImageURL(part.Media)
			}
		}
		return true
	case xir.PartToolCall:
		if part.ToolCall == nil {
			return false
		}
		if part.ToolCall.Type != "" && part.ToolCall.Type != "function" {
			// Keep the call and its arguments; the native call type is lost.
			part.ToolCall.Type = "function"
		}
		return true
	case xir.PartToolResult:
		if part.ToolResult == nil {
			return false
		}
		part.ToolResult.Content = sanitizeParts(part.ToolResult.Content, to, scope)
		return true
	default:
		return true
	}
}

// chatImageURL turns inline image bytes into the data URI Chat Completions
// expects. The codec would otherwise place the raw base64 in image_url.
func chatImageURL(media *xir.Media) {
	if media.URL != "" || media.Data == "" {
		return
	}
	if strings.HasPrefix(media.Data, "data:") {
		media.URL = media.Data
		media.Data = ""
		return
	}
	mediaType := media.MediaType
	if mediaType == "" {
		mediaType = "image/png"
	}
	media.URL = "data:" + mediaType + ";base64," + media.Data
	media.Data = ""
}

// splitToolResultTurns rewrites a turn that carries tool_result blocks into the
// Chat Completions shape: one tool message per result, followed by the
// remaining content of the original turn.
func splitToolResultTurns(messages []xir.Message) []xir.Message {
	out := make([]xir.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == xir.RoleTool || !hasToolResult(message.Parts) {
			out = append(out, message)
			continue
		}
		rest := make([]xir.Part, 0, len(message.Parts))
		for _, part := range message.Parts {
			if part.Kind != xir.PartToolResult || part.ToolResult == nil {
				rest = append(rest, part)
				continue
			}
			out = append(out, xir.Message{
				Role:  xir.RoleTool,
				Name:  part.ToolResult.Name,
				Parts: []xir.Part{part},
				Ref:   message.Ref,
			})
		}
		if len(rest) > 0 {
			out = append(out, xir.Message{
				Role:  message.Role,
				Name:  message.Name,
				Parts: rest,
				Ref:   message.Ref,
			})
		}
	}
	return out
}

func hasToolResult(parts []xir.Part) bool {
	for i := range parts {
		if parts[i].Kind == xir.PartToolResult {
			return true
		}
	}
	return false
}

// liftSystemMessages moves system/developer turns into Request.System, which
// is the only system slot the Messages codec encodes. Chat puts both roles in
// Messages, so a hub conversion has to lift them itself.
func liftSystemMessages(request *xir.Request) {
	if len(request.Messages) == 0 {
		return
	}
	out := make([]xir.Message, 0, len(request.Messages))
	for _, message := range request.Messages {
		if message.Role == xir.RoleSystem || message.Role == xir.RoleDeveloper {
			request.System = append(request.System, message.Parts...)
			continue
		}
		out = append(out, message)
	}
	request.Messages = out
}
