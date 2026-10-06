package proxy

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/kingfs/Trajecta/pkg/llm"
)

// proxyErrorEnvelope renders the error body for errors the proxy itself
// produced (routing failures, upstream transport failures, overload). Clients
// and the official SDKs parse `{"error":{...}}`, so a plain-text body makes the
// real cause unreadable on the caller side. The shape follows the protocol
// family of the incoming endpoint.
func proxyErrorEnvelope(r *http.Request, statusCode int, code, message string) []byte {
	message = strings.TrimSpace(message)
	if message == "" {
		message = http.StatusText(statusCode)
	}
	if code == "" {
		code = "proxy_error"
	}
	path := ""
	if r != nil && r.URL != nil {
		path = r.URL.Path
	}
	// Ollama's compatibility surface answers with `{"error":"<message>"}`: the field is a
	// string there, and the Ollama clients unmarshal it as one, so the OpenAI object shape
	// is not a substitute. `NormalizeEndpoint` is the shared normalizer the proxy already
	// routes `/api/show` with, rather than a second opinion about that path.
	if path != "" && llm.NormalizeEndpoint(path) == "/api/show" {
		body, err := json.Marshal(map[string]string{"error": message})
		if err != nil {
			return []byte(message)
		}
		return append(body, '\n')
	}
	// The envelope follows the protocol family of the endpoint, which is the same
	// classification the router, the recorder and the observer use. Deciding it
	// here a second time with this package's own path predicates is how
	// `/v1beta/models/<model>:countTokens` and `:embedContent` came to be answered
	// with an OpenAI-shaped error while the rest of the proxy called them Google.
	provider := ""
	if path != "" {
		provider = llm.ClassifyPath(path, "").Provider
	}
	var payload any
	switch provider {
	case llm.ProviderAnthropic:
		payload = map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    anthropicErrorType(statusCode),
				"message": message,
			},
		}
	case llm.ProviderGoogleGenAI, llm.ProviderVertexNative:
		payload = map[string]any{
			"error": map[string]any{
				"code":    statusCode,
				"message": message,
				"status":  googleErrorStatus(statusCode),
			},
		}
	default:
		payload = map[string]any{
			"error": map[string]any{
				"message": message,
				"type":    openAIErrorType(statusCode),
				"code":    code,
				"param":   nil,
			},
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return []byte(message)
	}
	return append(body, '\n')
}

func openAIErrorType(statusCode int) string {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return "rate_limit_error"
	case statusCode >= 500:
		return "api_error"
	case statusCode >= 400:
		return "invalid_request_error"
	default:
		return "api_error"
	}
}

// anthropicErrorType maps an HTTP status to the `error.type` Anthropic documents for
// it. The official SDKs choose their exception class from the status code, but the type
// string is part of the published contract and is what a caller reads in a log or a bug
// report, so a status answered with the wrong type misdescribes the failure: 401 as
// `api_error` reports a credential problem as a server fault, 403 as `api_error` hides a
// permissions problem, and 413 as `api_error` - the response the proxy's own request-body
// limit produces - hides that the client's request was the thing too large to accept.
func anthropicErrorType(statusCode int) string {
	switch statusCode {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable, 529: // 529 is Anthropic's own overloaded status
		return "overloaded_error"
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusGatewayTimeout:
		return "api_error"
	}
	if statusCode >= 500 {
		return "api_error"
	}
	if statusCode >= 400 {
		return "invalid_request_error"
	}
	return "api_error"
}

// googleErrorStatus maps an HTTP status to the canonical `google.rpc.Code` name the
// Google error envelope carries in `error.status`. A client-generated 4xx that the table
// does not name still belongs to the caller, so the fallback splits on the status class
// instead of answering every unlisted status with `INTERNAL`: a 413 was reported as an
// internal server error, which sends an operator looking at the proxy for a request the
// client should have shrunk.
func googleErrorStatus(statusCode int) string {
	switch statusCode {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return "DEADLINE_EXCEEDED"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	case http.StatusInternalServerError:
		return "INTERNAL"
	}
	if statusCode >= 500 {
		return "INTERNAL"
	}
	if statusCode >= 400 {
		return "INVALID_ARGUMENT"
	}
	return "INTERNAL"
}

// writeProxyError writes a protocol-shaped error response for a failure the
// proxy itself detected. `source` distinguishes proxy-originated failures from
// upstream ones in the response headers.
func writeProxyError(w http.ResponseWriter, r *http.Request, statusCode int, code, message string) {
	body := proxyErrorEnvelope(r, statusCode, code, message)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Trajecta-Error-Source", "proxy")
	w.WriteHeader(statusCode)
	_, _ = w.Write(body)
}

// proxyErrorContentType reports the content type of proxyErrorEnvelope so
// synthetic recordings match what the client received.
func proxyErrorContentType() string { return "application/json" }
