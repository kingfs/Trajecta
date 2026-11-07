// Package protocolconvert converts LLM API requests, responses and SSE
// streams between the four wire protocols Trajecta records: OpenAI Chat
// Completions, OpenAI Responses, Anthropic Messages and Gemini
// generateContent (Google AI Studio and Vertex share the wire shape).
//
// The package is intentionally thin: it owns the protocol vocabulary, the
// passthrough-first routing decision and the conversion entry points, while
// all per-protocol parsing/encoding is delegated to pkg/llm's adapters via
// their shared LLMRequest/LLMResponse intermediate representation. That
// mirrors the shape of chaitin/ai-api-protocol-bridge (one IR, one adapter
// per protocol) without forking a second set of protocol structs.
//
// Conversion is lossless only for the fields the IR carries (model, system
// prompt, messages with text/tool_use/tool_result content, tools,
// tool_choice, stop sequences, temperature/top_p/top_k/max_tokens, usage).
// Protocol-specific extras outside the IR (reasoning effort, presence /
// frequency penalties, safety settings beyond the IR's, provider options)
// are dropped; ConvertResult.Warnings reports the lossy combinations the IR
// cannot carry so a caller can decide to reject instead of silently degrade.
//
// The forwarding hot path keeps its current behaviour: a request whose
// protocol the selected upstream serves natively is passed through byte for
// byte (see Plan). Conversion only happens when the upstream cannot serve
// the client's protocol natively, mirroring magpie's masking-first design:
// passthrough is the default, translation is the fallback.
package protocolconvert

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/kingfs/Trajecta/pkg/llm"
)

// Protocol is one wire protocol a client can speak and an upstream can serve.
type Protocol string

const (
	ProtocolChatCompletions Protocol = "chat_completions"
	ProtocolResponses       Protocol = "responses"
	ProtocolMessages        Protocol = "messages"
	// ProtocolGemini is the generateContent wire shape shared by Google AI
	// Studio (/v1beta/models/{model}:generateContent) and Vertex AI
	// (/v1/publishers/{resource}/models/{model}:generateContent). The two
	// differ only in URL construction and auth, which the caller (upstream
	// resolution) owns; RequestPath takes a Vertex flag for that.
	ProtocolGemini Protocol = "gemini"
	// ProtocolGenerateContent is accepted as an alias for ProtocolGemini so
	// callers using modelprobe's naming keep working.
	ProtocolGenerateContent Protocol = "generate_content"
)

// AllProtocols lists every convertible protocol in conversion-preference
// order: Chat Completions first (broadest upstream support), then Responses,
// Messages, Gemini.
var AllProtocols = []Protocol{
	ProtocolChatCompletions,
	ProtocolResponses,
	ProtocolMessages,
	ProtocolGemini,
}

// ParseProtocol normalizes a protocol name. "" is an error: unlike
// modelprobe, conversion never guesses.
func ParseProtocol(value string) (Protocol, error) {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
	switch Protocol(normalized) {
	case ProtocolChatCompletions:
		return ProtocolChatCompletions, nil
	case ProtocolResponses, ProtocolMessages, ProtocolGemini, ProtocolGenerateContent:
		return canonical(Protocol(normalized)), nil
	default:
		return "", fmt.Errorf("protocolconvert: unknown protocol %q", strings.TrimSpace(value))
	}
}

func canonical(p Protocol) Protocol {
	if p == ProtocolGenerateContent {
		return ProtocolGemini
	}
	return p
}

// Valid reports whether p names a convertible protocol.
func (p Protocol) Valid() bool {
	switch canonical(p) {
	case ProtocolChatCompletions, ProtocolResponses, ProtocolMessages, ProtocolGemini:
		return true
	}
	return false
}

// Endpoint returns the canonical client-facing endpoint for the protocol.
func (p Protocol) Endpoint() string {
	switch canonical(p) {
	case ProtocolChatCompletions:
		return "/v1/chat/completions"
	case ProtocolResponses:
		return "/v1/responses"
	case ProtocolMessages:
		return "/v1/messages"
	case ProtocolGemini:
		return "/v1beta/models:generateContent"
	}
	return ""
}

// Provider returns the pkg/llm provider identifier used to parse/encode the
// protocol. Gemini maps to google_genai; Vertex callers use
// ProviderForGemini with vertex=true.
func (p Protocol) Provider() string {
	return ProviderFor(p, false)
}

// ProviderFor returns the pkg/llm provider for p; vertex selects the Vertex
// adapter for Gemini.
func ProviderFor(p Protocol, vertex bool) string {
	switch canonical(p) {
	case ProtocolMessages:
		return llm.ProviderAnthropic
	case ProtocolGemini:
		if vertex {
			return llm.ProviderVertexNative
		}
		return llm.ProviderGoogleGenAI
	default:
		return llm.ProviderOpenAICompatible
	}
}

// DetectFromPath classifies a client request path into a protocol. It is a
// pure path classification (no body read), matching the harness rule that
// classification happens from the request head only.
func DetectFromPath(rawPath string) (Protocol, error) {
	endpoint := llm.NormalizeEndpoint(rawPath)
	// count_tokens has a different response shape ({input_tokens}) and
	// different semantics from a generation request. Mapping it to
	// ProtocolMessages would send it to /v1/messages as a real generation.
	// It must stay on the native path (or the proxy's synthetic fallback),
	// never through conversion.
	if endpoint == "/v1/messages/count_tokens" {
		return "", fmt.Errorf("protocolconvert: count_tokens endpoint %q is not convertible", rawPath)
	}
	switch endpoint {
	case "/v1/chat/completions":
		return ProtocolChatCompletions, nil
	case "/v1/responses":
		return ProtocolResponses, nil
	case "/v1/messages":
		return ProtocolMessages, nil
	case "/v1beta/models:generateContent", "/v1beta/models:streamGenerateContent",
		"/v1/publishers/models:generateContent", "/v1/publishers/models:streamGenerateContent":
		return ProtocolGemini, nil
	default:
		return "", fmt.Errorf("protocolconvert: no protocol for path %q", rawPath)
	}
}

// RequestPath returns the upstream path a converted request for p should be
// sent to. For Gemini the model travels in the path; for the other three it
// stays in the body and model is ignored. stream selects the streaming
// endpoint variant where one exists.
func (p Protocol) RequestPath(model string, stream bool, vertex bool) string {
	switch canonical(p) {
	case ProtocolChatCompletions:
		return "/v1/chat/completions"
	case ProtocolResponses:
		return "/v1/responses"
	case ProtocolMessages:
		return "/v1/messages"
	case ProtocolGemini:
		// The model travels in the path; escape it so a model name
		// containing ":" or "/" cannot break the :generateContent suffix
		// or inject path segments. Publishers/projects prefixes are owned
		// by the caller's BaseURL, not by this function.
		escaped := url.PathEscape(model)
		if vertex {
			if stream {
				return "/v1/publishers/models/" + escaped + ":streamGenerateContent"
			}
			return "/v1/publishers/models/" + escaped + ":generateContent"
		}
		if stream {
			return "/v1beta/models/" + escaped + ":streamGenerateContent"
		}
		return "/v1beta/models/" + escaped + ":generateContent"
	}
	return ""
}
