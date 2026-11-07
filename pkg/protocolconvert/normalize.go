package protocolconvert

import (
	"strings"

	"github.com/kingfs/Trajecta/pkg/llm"
)

// finishKind is the protocol-neutral meaning of a stop reason. Each protocol
// spells the same four outcomes differently, and a converted response must use
// the target's vocabulary or a client that branches on it (an agent deciding
// whether to run another tool round, for example) stops working.
type finishKind int

const (
	finishUnknown finishKind = iota
	finishStop
	finishLength
	finishToolUse
	finishContentFilter
	finishRefusal
)

func classifyFinishReason(raw string, from Protocol) finishKind {
	normalized := strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(raw)))
	switch normalized {
	case "stop", "endturn", "stopsequence", "complete", "completed", "pause", "pauseturn":
		return finishStop
	case "length", "maxtokens", "maxoutputtokens", "maxcompletiontokens":
		return finishLength
	case "tooluse", "toolcalls", "functioncall", "functioncalls":
		return finishToolUse
	case "contentfilter", "safety", "recitation", "blocked", "blocklist", "prohibitedcontent", "spii":
		return finishContentFilter
	case "refusal":
		return finishRefusal
	}
	// Responses carries no finish reason on the response object; an empty value
	// is not an error, and the caller infers it from the output below.
	return finishUnknown
}

func wireFinishReason(kind finishKind, to Protocol, hasToolCalls bool) string {
	if kind == finishUnknown {
		if hasToolCalls {
			kind = finishToolUse
		} else {
			kind = finishStop
		}
	}
	switch to {
	case ProtocolChatCompletions:
		switch kind {
		case finishLength:
			return "length"
		case finishToolUse:
			return "tool_calls"
		case finishContentFilter, finishRefusal:
			return "content_filter"
		default:
			return "stop"
		}
	case ProtocolMessages:
		switch kind {
		case finishLength:
			return "max_tokens"
		case finishToolUse:
			return "tool_use"
		case finishRefusal:
			return "refusal"
		case finishContentFilter:
			return "refusal"
		default:
			return "end_turn"
		}
	case ProtocolGemini:
		switch kind {
		case finishLength:
			return "MAX_TOKENS"
		case finishContentFilter, finishRefusal:
			return "SAFETY"
		default:
			return "STOP"
		}
	default:
		// Responses has no finish_reason field. Carry the canonical Chat
		// spelling so the Responses encoder can derive status and
		// incomplete_details without guessing from the source protocol.
		return wireFinishReason(kind, ProtocolChatCompletions, hasToolCalls)
	}
}

// applyFinishReason rewrites every candidate's finish reason into the target's
// vocabulary. It runs on the parsed upstream response, before encoding.
func applyFinishReason(resp *llm.LLMResponse, from Protocol, to Protocol) {
	if resp == nil || from == to {
		return
	}
	for i := range resp.Candidates {
		candidate := &resp.Candidates[i]
		hasToolCalls := len(candidate.ResponseToolCalls()) > 0
		kind := classifyFinishReason(candidate.FinishReason, from)
		// A refusal is an answer outcome of its own and must not be reported
		// as a normal stop.
		if candidate.Refusal != nil && kind != finishToolUse {
			kind = finishRefusal
		}
		// Gemini reports STOP for a function-call turn, and some
		// OpenAI-compatible providers report stop even when they emitted tool
		// calls. The output is the stronger signal: a client that branches on
		// "did the model ask for a tool" must see tool_use/tool_calls.
		if hasToolCalls && (kind == finishUnknown || kind == finishStop || kind == finishRefusal) {
			kind = finishToolUse
		}
		candidate.FinishReason = wireFinishReason(kind, to, hasToolCalls)
	}
}
