package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
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
	var payload any
	switch {
	case isAnthropicMessagesPath(path):
		payload = map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    anthropicErrorType(statusCode),
				"message": message,
			},
		}
	case strings.HasPrefix(path, "/v1beta/") || strings.Contains(path, ":generateContent"):
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

func isAnthropicMessagesPath(path string) bool {
	return path == "/v1/messages" || strings.HasPrefix(path, "/v1/messages/")
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

func anthropicErrorType(statusCode int) string {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return "rate_limit_error"
	case statusCode == http.StatusBadRequest || statusCode == http.StatusNotFound:
		return "invalid_request_error"
	case statusCode >= 500:
		return "api_error"
	default:
		return "api_error"
	}
}

func googleErrorStatus(statusCode int) string {
	switch statusCode {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return "UNAVAILABLE"
	default:
		return "INTERNAL"
	}
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
