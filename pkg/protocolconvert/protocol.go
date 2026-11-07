// Package protocolconvert converts non-streaming LLM API requests and
// responses between the four wire protocols Trajecta records: OpenAI Chat
// Completions, OpenAI Responses, Anthropic Messages and Gemini
// generateContent (Google AI Studio and Vertex share the wire shape).
// Streaming conversion is deliberately not implemented: it needs a
// frame-by-frame translator, so streaming requests must use a native
// passthrough target.
//
// The package is intentionally thin: it owns the protocol vocabulary, the
// passthrough-first routing decision and the conversion entry points, while
// all per-protocol parsing/encoding is delegated to pkg/llm's adapters via
// their shared LLMRequest/LLMResponse intermediate representation. That
// mirrors the shape of chaitin/ai-api-protocol-bridge (one IR, one adapter
// per protocol) without forking a second set of protocol structs.
//
// Conversion is faithful only for the fields the IR carries (model, system
// prompt, messages with text/tool_use/tool_result content, tools,
// tool_choice, stop sequences, temperature/top_p/top_k/max_tokens, usage).
// Protocol-specific extras outside the IR (reasoning effort, presence /
// frequency penalties, provider options) either change the answer and are
// rejected as fatal warnings, or are reported as non-fatal warnings so the
// caller can see the documented loss.
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
//
// The returned path is the surface path the upstream resolver expands (a Vertex
// base URL adds the project/location/publisher resource). A model name that
// cannot be placed in a path segment is an error: url.PathEscape does not
// escape ":", and going through a URL parse turns an escaped ".." segment back
// into traversal.
func (p Protocol) RequestPath(model string, stream bool, vertex bool) (string, error) {
	switch canonical(p) {
	case ProtocolChatCompletions:
		return "/v1/chat/completions", nil
	case ProtocolResponses:
		return "/v1/responses", nil
	case ProtocolMessages:
		return "/v1/messages", nil
	case ProtocolGemini:
		if err := ValidateModelName(model); err != nil {
			return "", err
		}
		escaped := url.PathEscape(model)
		if vertex {
			if stream {
				return "/v1/publishers/models/" + escaped + ":streamGenerateContent", nil
			}
			return "/v1/publishers/models/" + escaped + ":generateContent", nil
		}
		if stream {
			return "/v1beta/models/" + escaped + ":streamGenerateContent", nil
		}
		return "/v1beta/models/" + escaped + ":generateContent", nil
	}
	return "", fmt.Errorf("protocolconvert: no request path for protocol %q", p)
}

// ValidateModelName guards a model name that will be placed in a URL path. The
// character set is the practical intersection of the upstreams' model ids; in
// particular ":" is rejected because it terminates the Gemini action suffix,
// and ".." is rejected because it can escape the model segment.
func ValidateModelName(model string) error {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return fmt.Errorf("protocolconvert: model name is empty")
	}
	if trimmed != model {
		return fmt.Errorf("protocolconvert: model name %q has surrounding whitespace", model)
	}
	if len(trimmed) > 256 {
		return fmt.Errorf("protocolconvert: model name is too long")
	}
	if strings.Contains(trimmed, "..") || strings.Contains(trimmed, "//") ||
		strings.HasPrefix(trimmed, "/") || strings.HasSuffix(trimmed, "/") {
		return fmt.Errorf("protocolconvert: model name %q is not a valid path segment", model)
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == '/', r == '@':
		default:
			return fmt.Errorf("protocolconvert: model name %q contains unsupported character %q", model, r)
		}
	}
	return nil
}
