package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProxyErrorEnvelopeMatchesTheRequestEntrypoint pins the error body shape
// per entrypoint. An OpenAI, Anthropic or Google SDK parses only its own error
// shape, so answering one family with another family's envelope turns a real,
// readable failure into a client-side parse error. The Google branch originally
// matched `:generateContent` alone, which missed the streaming variant and the
// token-counting endpoints.
func TestProxyErrorEnvelopeMatchesTheRequestEntrypoint(t *testing.T) {
	for _, tc := range []struct {
		name  string
		path  string
		check func(t *testing.T, payload map[string]any)
	}{
		{
			name: "openai chat",
			path: "/v1/chat/completions",
			check: func(t *testing.T, payload map[string]any) {
				t.Helper()
				errObj, ok := payload["error"].(map[string]any)
				if !ok {
					t.Fatalf("error = %#v, want an object", payload["error"])
				}
				if errObj["type"] != "api_error" {
					t.Fatalf("error.type = %v, want api_error", errObj["type"])
				}
				if _, ok := errObj["param"]; !ok {
					t.Fatalf("openai envelope must carry `param`: %#v", errObj)
				}
			},
		},
		{
			name: "anthropic messages",
			path: "/v1/messages",
			check: func(t *testing.T, payload map[string]any) {
				t.Helper()
				if payload["type"] != "error" {
					t.Fatalf("type = %v, want error", payload["type"])
				}
			},
		},
		{
			name:  "google v1beta generateContent",
			path:  "/v1beta/models/gemini-2.0-flash:generateContent",
			check: assertGoogleEnvelope,
		},
		{
			name:  "google v1beta streamGenerateContent",
			path:  "/v1beta/models/gemini-2.0-flash:streamGenerateContent",
			check: assertGoogleEnvelope,
		},
		{
			name:  "google v1beta countTokens",
			path:  "/v1beta/models/gemini-2.0-flash:countTokens",
			check: assertGoogleEnvelope,
		},
		{
			name:  "vertex express streamGenerateContent",
			path:  "/v1/publishers/google/models/gemini-2.0-flash:streamGenerateContent",
			check: assertGoogleEnvelope,
		},
		{
			name:  "vertex short form streamGenerateContent",
			path:  "/v1/models/gemini-2.0-flash:streamGenerateContent",
			check: assertGoogleEnvelope,
		},
		{
			name:  "vertex short form countTokens",
			path:  "/v1/models/gemini-2.0-flash:countTokens",
			check: assertGoogleEnvelope,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			writeProxyError(rec, req, http.StatusBadGateway, "upstream_error", "boom")

			if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			if got := rec.Header().Get("X-Trajecta-Error-Source"); got != "proxy" {
				t.Fatalf("X-Trajecta-Error-Source = %q, want proxy", got)
			}
			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("body is not JSON: %v (%s)", err, rec.Body.String())
			}
			tc.check(t, payload)
		})
	}
}

// TestServeHTTPUnauthorizedUsesTheProtocolEnvelope pins the first failure a
// misconfigured SDK hits. The proxy entrypoint rejected an unauthenticated
// request with `http.Error`, so a plain `text/plain "Unauthorized"` reached an
// SDK that can only parse its own error shape.
func TestServeHTTPUnauthorizedUsesTheProtocolEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{name: "openai", path: "/v1/chat/completions", body: `"error"`},
		{name: "anthropic", path: "/v1/messages", body: `"type"`},
		{name: "google", path: "/v1beta/models/gemini:generateContent", body: `"status"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := &Handler{authVerifier: proxyTestVerifier{}}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{}`))
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="trajecta-proxy"` {
				t.Fatalf("WWW-Authenticate = %q", got)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.body) {
				t.Fatalf("body = %s, want the %s family's error shape", body, tc.name)
			}
			if !strings.Contains(body, "Unauthorized") {
				t.Fatalf("body = %s, want the reason `Unauthorized`", body)
			}
		})
	}
}

func assertGoogleEnvelope(t *testing.T, payload map[string]any) {
	t.Helper()

	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("error = %#v, want an object", payload["error"])
	}
	if errObj["status"] != "UNAVAILABLE" {
		t.Fatalf("error.status = %v, want UNAVAILABLE", errObj["status"])
	}
	if _, ok := errObj["message"]; !ok {
		t.Fatalf("google envelope must carry `message`: %#v", errObj)
	}
	if _, leaked := payload["type"]; leaked {
		t.Fatalf("google envelope must not carry the anthropic `type` field: %#v", payload)
	}
}
