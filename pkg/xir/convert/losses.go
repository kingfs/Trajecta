package convert

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/xir"
)

// Loss ledger
//
// The hub reports these categories. Fatal means the field can change the
// model's answer or break an opaque protocol contract, so Strict and Drop
// reject it and only Warn proceeds.
//
// Fatal:
//   - previous_response_id / conversation when the target is not Responses;
//   - a non-empty source-protocol Extensions object (unmodeled members cannot
//     be proven harmless);
//   - sealed reasoning/parts, encrypted reasoning, and foreign reasoning
//     signatures (protocol-owned bytes are never re-signed);
//   - a non-function tool call type, and hosted tool declarations the target
//     cannot express;
//   - response_format/responseSchema when the target has no mapped equivalent
//     (Messages and Gemini in this hub), and reasoning effort/budget/toggle
//     fields the target does not define;
//   - seed, presence/frequency penalties and logprobs when the target has no
//     field for them;
//   - media kinds or media payload shapes the target codec cannot encode.
//
// Non-fatal:
//   - prompt_cache_key/store, metadata and user identifiers, message roles
//     the target normalizes, reasoning text the target omits, citations and
//     search metadata, media filename/type details, and unsupported parallel
//     tool flags or per-tool flags.
//

// lossSet collects losses in detection order and keeps one entry per field.
type lossSet struct {
	from   xir.Protocol
	to     xir.Protocol
	order  []string
	byName map[string]int
	losses []xir.Loss
}

func newLossSet(from, to xir.Protocol) *lossSet {
	return &lossSet{from: from, to: to, byName: make(map[string]int)}
}

func (s *lossSet) add(field string, kind xir.LossKind, fatal bool, reason string) {
	if index, ok := s.byName[field]; ok {
		// A fatal finding always wins over a non-fatal one for the same field.
		if fatal && !s.losses[index].Fatal {
			s.losses[index].Kind = kind
			s.losses[index].Reason = reason
			s.losses[index].Fatal = true
		}
		return
	}
	s.byName[field] = len(s.losses)
	s.order = append(s.order, field)
	s.losses = append(s.losses, xir.Loss{
		Field:  field,
		Kind:   kind,
		Reason: reason,
		Fatal:  fatal,
		From:   s.from,
		To:     s.to,
	})
}

func (s *lossSet) list() []xir.Loss {
	if len(s.losses) == 0 {
		return nil
	}
	return s.losses
}

// requestLosses inspects a decoded request for semantic values the target
// protocol cannot carry. The list is intentionally conservative: a field that
// can change the model's answer is fatal, while presentation, cache and
// metadata fields are non-fatal.
func requestLosses(from, to xir.Protocol, request *xir.Request) []xir.Loss {
	if request == nil {
		return nil
	}
	set := newLossSet(from, to)
	addStateLosses(request.State, set)
	addParamLosses(request.Params, request.ToolChoice, set)
	addExtensionLosses(request.Extensions, set)
	addPartLosses(request.System, from, to, partScope{system: true}, "system", set)
	for i := range request.Messages {
		message := &request.Messages[i]
		path := fmt.Sprintf("messages[%d]", i)
		addRoleLosses(message.Role, to, path, set)
		addPartLosses(message.Parts, from, to, partScope{role: message.Role}, path, set)
	}
	for i := range request.Tools {
		addToolLosses(&request.Tools[i], from, to, fmt.Sprintf("tools[%d]", i), set)
	}
	return set.list()
}

// responseLosses is requestLosses for a decoded response. Usage survives every
// target codec and is therefore not reported as a loss.
func responseLosses(from, to xir.Protocol, response *xir.Response) []xir.Loss {
	if response == nil {
		return nil
	}
	set := newLossSet(from, to)
	if response.Status != "" && to != xir.ProtocolResponses {
		set.add("status", xir.LossDegraded, false, "target protocol does not expose the response status")
	}
	addExtensionLosses(response.Extensions, set)
	addPartLosses(response.Parts, from, to, partScope{inResponse: true}, "response", set)
	return set.list()
}

func addStateLosses(state xir.StateRefs, set *lossSet) {
	if state.PreviousResponseID != "" && set.to != xir.ProtocolResponses {
		set.add("state.previous_response_id", xir.LossUnsupported, true,
			"server-side previous_response_id cannot be carried by the target protocol")
	}
	if state.ConversationID != "" && set.to != xir.ProtocolResponses {
		set.add("state.conversation", xir.LossUnsupported, true,
			"server-side conversation reference cannot be carried by the target protocol")
	}
	if state.PromptCacheKey != "" && set.to != xir.ProtocolChatCompletions && set.to != xir.ProtocolResponses {
		set.add("state.prompt_cache_key", xir.LossDegraded, false,
			"target protocol has no prompt cache key; cache affinity is lost")
	}
	if state.Store != nil && set.to != xir.ProtocolChatCompletions && set.to != xir.ProtocolResponses {
		set.add("state.store", xir.LossDegraded, false,
			"target protocol has no server-side store flag")
	}
}

func addRoleLosses(role xir.Role, to xir.Protocol, path string, set *lossSet) {
	switch role {
	case xir.RoleUnknown:
		set.add(path+".role", xir.LossDegraded, false, "unknown message role is carried as a user turn")
	case xir.RoleDeveloper:
		if to == xir.ProtocolMessages {
			set.add(path+".role", xir.LossDegraded, false, "developer role is carried as a system instruction")
		}
	}
}

func addParamLosses(params xir.Params, choice xir.ToolChoice, set *lossSet) {
	to := set.to

	if len(params.ResponseFormat) > 0 && to != xir.ProtocolChatCompletions && to != xir.ProtocolResponses {
		set.add("params.response_format", xir.LossUnsupported, true,
			"target protocol has no structured-output equivalent")
	}
	// Gemini's structured-output fields live in Params.Extra; the other codecs
	// do not emit them.
	for _, key := range []string{"responseMimeType", "responseSchema"} {
		if raw, ok := params.Extra[key]; ok && len(raw) > 0 && to != xir.ProtocolGemini {
			set.add("params."+key, xir.LossUnsupported, true,
				"target protocol has no Gemini structured-output field")
		}
	}
	if params.ReasoningEffort != "" && to == xir.ProtocolMessages {
		set.add("params.reasoning_effort", xir.LossUnsupported, true,
			"Messages cannot encode a reasoning effort level")
	}
	if params.ThinkingBudget != nil && (to == xir.ProtocolChatCompletions || to == xir.ProtocolResponses) {
		set.add("params.thinking_budget", xir.LossUnsupported, true,
			"target protocol has no thinking-token budget")
	}
	if params.Thinking != nil && (to == xir.ProtocolChatCompletions || to == xir.ProtocolResponses) {
		set.add("params.thinking", xir.LossUnsupported, true,
			"target protocol has no thinking enable/disable switch")
	}
	if params.Seed != nil && to != xir.ProtocolChatCompletions && to != xir.ProtocolGemini {
		set.add("params.seed", xir.LossUnsupported, true, "target protocol has no sampling seed")
	}
	if params.PresencePenalty != nil && to != xir.ProtocolChatCompletions && to != xir.ProtocolGemini {
		set.add("params.presence_penalty", xir.LossUnsupported, true,
			"target protocol has no presence penalty")
	}
	if params.FrequencyPenalty != nil && to != xir.ProtocolChatCompletions && to != xir.ProtocolGemini {
		set.add("params.frequency_penalty", xir.LossUnsupported, true,
			"target protocol has no frequency penalty")
	}
	if (params.Logprobs != nil || params.TopLogprobs != nil) && to != xir.ProtocolChatCompletions {
		set.add("params.logprobs", xir.LossUnsupported, true,
			"target protocol cannot return log probabilities")
	}
	if params.ParallelTools != nil {
		switch to {
		case xir.ProtocolGemini:
			set.add("params.parallel_tool_calls", xir.LossDegraded, false,
				"Gemini function calling has no parallel-tool flag")
		case xir.ProtocolMessages:
			if choice.Mode == "" {
				set.add("params.parallel_tool_calls", xir.LossDegraded, false,
					"Messages can only express this flag alongside tool_choice")
			}
		}
	}
	if to == xir.ProtocolGemini {
		if params.User != "" {
			set.add("params.user", xir.LossDegraded, false, "Gemini has no user field")
		}
		if len(params.Metadata) > 0 {
			set.add("params.metadata", xir.LossDegraded, false, "Gemini has no request metadata field")
		}
	}

	for key, raw := range params.Extra {
		if len(raw) == 0 || key == "responseMimeType" || key == "responseSchema" {
			continue
		}
		if targetCarriesParamExtra(to, key) {
			continue
		}
		reason := "protocol-specific generation parameter is not carried"
		if key == "thinking" || key == "text.verbosity" || key == "reasoning.summary" {
			reason = "target protocol has no equivalent for this option"
		}
		set.add("params."+key, xir.LossDegraded, false, reason)
	}
}

func targetCarriesParamExtra(to xir.Protocol, key string) bool {
	switch to {
	case xir.ProtocolChatCompletions:
		return key == "reasoning"
	case xir.ProtocolResponses:
		return key == "text.verbosity" || key == "reasoning.summary"
	case xir.ProtocolMessages:
		return key == "thinking"
	case xir.ProtocolGemini:
		return key == "responseMimeType" || key == "responseSchema" || strings.HasPrefix(key, "thinkingConfig.")
	default:
		return false
	}
}

func addExtensionLosses(extensions map[string]json.RawMessage, set *lossSet) {
	for protocol, raw := range extensions {
		if len(strings.TrimSpace(string(raw))) == 0 || strings.TrimSpace(string(raw)) == "null" {
			continue
		}
		if protocol == string(set.to) {
			continue
		}
		set.add("extensions."+protocol, xir.LossUnsupported, true,
			"protocol-specific extension members cannot be carried to another protocol")
	}
}

func addToolLosses(tool *xir.Tool, from, to xir.Protocol, path string, set *lossSet) {
	kind := tool.Kind
	if kind == "" {
		kind = "function"
	}
	if tool.Namespace != "" {
		set.add(path+".namespace", xir.LossDegraded, false, "target protocol has no tool namespace")
	}
	switch to {
	case xir.ProtocolChatCompletions:
		if kind != "function" && (tool.Name != "" || tool.Description != "" || len(tool.Schema) > 0) {
			set.add(path, xir.LossUnsupported, true,
				"Chat Completions can only declare this hosted tool by type; its declaration is lost")
		}
	case xir.ProtocolGemini:
		if kind != "function" && kind != "custom" {
			set.add(path, xir.LossUnsupported, true,
				"Gemini functionDeclarations cannot express this hosted tool")
		}
		if kind == "custom" {
			set.add(path+".kind", xir.LossDegraded, false,
				"custom tool is encoded as a Gemini function declaration")
		}
		if tool.Strict != nil {
			set.add(path+".strict", xir.LossDegraded, false,
				"Gemini function declarations have no strict flag")
		}
	case xir.ProtocolMessages:
		if kind != "function" && kind != "custom" {
			set.add(path+".kind", xir.LossDegraded, false,
				"Messages does not model this hosted tool's payload")
		}
	case xir.ProtocolResponses:
		if kind == "custom" && from != xir.ProtocolResponses {
			set.add(path+".kind", xir.LossDegraded, false,
				"custom tool format is protocol-native and is not carried")
		}
	}
}

// partScope describes where a part appears so the loss check can use the
// target codec's actual capability rather than a single flat matrix.
type partScope struct {
	inResponse bool
	system     bool
	role       xir.Role
}

func addPartLosses(parts []xir.Part, from, to xir.Protocol, scope partScope, path string, set *lossSet) {
	for i := range parts {
		addPartLoss(&parts[i], from, to, scope, fmt.Sprintf("%s[%d]", path, i), set)
	}
}

func addPartLoss(part *xir.Part, from, to xir.Protocol, scope partScope, path string, set *lossSet) {
	if part == nil {
		return
	}
	if part.Sealed != nil && part.Sealed.Protocol != to {
		set.add(path+".sealed", xir.LossUnsupported, true,
			fmt.Sprintf("sealed %q data may only be emitted by its source protocol", part.Sealed.Kind))
	}

	switch part.Kind {
	case xir.PartSealed:
		if part.Sealed == nil {
			set.add(path, xir.LossUnsupported, true, "sealed part has no payload")
		}
	case xir.PartUnknown:
		if part.Sealed == nil {
			if part.Text != "" {
				set.add(path, xir.LossDegraded, false, "unknown part is carried as plain text")
			} else {
				set.add(path, xir.LossDegraded, false, "unknown part has no equivalent in the target protocol")
			}
		}
	case xir.PartReasoning:
		addReasoningLosses(part, from, to, path, set)
	case xir.PartCitation:
		set.add(path+".citation", xir.LossDegraded, false, "citation metadata is not carried")
	case xir.PartSearch:
		set.add(path+".search", xir.LossDegraded, false, "search metadata is not carried")
	case xir.PartToolCall:
		addToolCallLosses(part, path, set)
	case xir.PartToolResult:
		if part.ToolResult != nil {
			addPartLosses(part.ToolResult.Content, from, to, scope, path+".content", set)
		}
	case xir.PartImage, xir.PartFile, xir.PartAudio, xir.PartVideo:
		addMediaLosses(part, to, scope, path, set)
	}
}

func addReasoningLosses(part *xir.Part, from, to xir.Protocol, path string, set *lossSet) {
	reasoning := part.Reasoning
	if reasoning == nil {
		if part.Sealed == nil && part.Text != "" {
			addReasoningTextLoss(part.Text, to, path, set)
		}
		return
	}
	if reasoning.Sealed != nil && reasoning.Sealed.Protocol != to {
		set.add(path+".sealed", xir.LossUnsupported, true,
			"sealed reasoning may only be emitted by its source protocol")
	}
	if reasoning.EncryptedContent != "" && to != xir.ProtocolResponses {
		set.add(path+".encrypted_content", xir.LossUnsupported, true,
			"encrypted reasoning cannot be carried by the target protocol")
	}
	if reasoning.Signature != "" {
		set.add(path+".signature", xir.LossUnsupported, true,
			"reasoning signature is protocol-owned and cannot be transferred")
	}
	if to == xir.ProtocolChatCompletions {
		if reasoning.Text != "" || len(reasoning.Summary) > 0 {
			set.add(path+".reasoning", xir.LossDegraded, false,
				"Chat Completions does not carry assistant reasoning blocks")
		}
	} else if len(reasoning.Summary) > 0 && to != xir.ProtocolResponses {
		set.add(path+".summary", xir.LossDegraded, false,
			"target protocol has no reasoning summary field")
	}
	if reasoning.Text != "" {
		addReasoningTextLoss(reasoning.Text, to, path, set)
	}
}

func addReasoningTextLoss(text string, to xir.Protocol, path string, set *lossSet) {
	if to == xir.ProtocolChatCompletions && text != "" {
		set.add(path+".reasoning", xir.LossDegraded, false,
			"Chat Completions does not carry assistant reasoning blocks")
	}
}

func addToolCallLosses(part *xir.Part, path string, set *lossSet) {
	if part.ToolCall == nil {
		set.add(path, xir.LossUnsupported, true, "tool-call part has no payload")
		return
	}
	// Convert runs only between different protocols. A protocol-native call
	// type cannot be rebuilt by another protocol's encoder.
	if part.ToolCall.Type != "" && part.ToolCall.Type != "function" {
		set.add(path+".type", xir.LossUnsupported, true,
			"target protocol cannot represent this non-function tool call type")
	}
}

func addMediaLosses(part *xir.Part, to xir.Protocol, scope partScope, path string, set *lossSet) {
	media := part.Media
	if media == nil {
		set.add(path, xir.LossUnsupported, true, "media part has no payload")
		return
	}
	if !mediaCarried(media.Kind, to, scope) {
		set.add(path, xir.LossUnsupported, true,
			fmt.Sprintf("%s media cannot be carried by the target protocol in this position", media.Kind))
		return
	}
	if media.Data == "" && media.URL == "" {
		set.add(path, xir.LossUnsupported, true, "media part has neither inline data nor a URL")
		return
	}
	if to == xir.ProtocolChatCompletions && !scope.inResponse {
		switch media.Kind {
		case xir.MediaAudio:
			if media.Data == "" {
				set.add(path+".url", xir.LossUnsupported, true,
					"Chat Completions cannot carry an audio URL")
			}
		case xir.MediaFile:
			if media.Data == "" {
				set.add(path+".url", xir.LossUnsupported, true,
					"Chat Completions cannot carry a file reference without inline data")
			}
		}
	}
	if media.Filename != "" && to == xir.ProtocolGemini {
		set.add(path+".filename", xir.LossDegraded, false, "Gemini media has no filename field")
	}
	if media.MediaType != "" && to == xir.ProtocolChatCompletions && media.Kind == xir.MediaFile {
		set.add(path+".media_type", xir.LossDegraded, false,
			"Chat Completions file parts do not carry a media type")
	}
}

// mediaCarried reports whether target can encode media of kind in scope.
func mediaCarried(kind xir.MediaKind, target xir.Protocol, scope partScope) bool {
	if scope.system {
		return target == xir.ProtocolChatCompletions || target == xir.ProtocolResponses
	}
	if scope.inResponse {
		switch target {
		case xir.ProtocolMessages:
			return kind == xir.MediaImage || kind == xir.MediaFile
		case xir.ProtocolGemini:
			return true
		default:
			// The Chat response encoder drops assistant media, and the
			// Responses response encoder rejects it.
			return false
		}
	}
	if scope.role == xir.RoleAssistant && target == xir.ProtocolChatCompletions {
		return false
	}
	switch target {
	case xir.ProtocolChatCompletions:
		return kind == xir.MediaImage || kind == xir.MediaAudio || kind == xir.MediaFile
	case xir.ProtocolResponses:
		return kind == xir.MediaImage || kind == xir.MediaFile || kind == xir.MediaAudio
	case xir.ProtocolMessages:
		return kind == xir.MediaImage || kind == xir.MediaFile
	case xir.ProtocolGemini:
		return true
	default:
		return false
	}
}
