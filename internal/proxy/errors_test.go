package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/llm"
)

// TestProxyErrorTypeMatchesTheDocumentedContract pins the `error.type` /
// `error.status` value each family documents for the statuses the proxy answers with.
//
// The envelope's *shape* was pinned by the test above, but its *type* came from a
// three-branch guess that answered several statuses with the server-fault value: an
// Anthropic 401 said `api_error` (a credential problem described as a proxy fault), a
// 413 said `api_error` even though the proxy's own body limit produced it, and Google's
// 413 said `INTERNAL`. The type is what a caller reads in a log and what an SDK records
// in its error object, so each one now carries the documented name for its status.
func TestProxyErrorTypeMatchesTheDocumentedContract(t *testing.T) {
	for _, tc := range []struct {
		status    int
		openai    string
		anthropic string
		google    string
	}{
		{status: http.StatusBadRequest, openai: "invalid_request_error", anthropic: "invalid_request_error", google: "INVALID_ARGUMENT"},
		{status: http.StatusUnauthorized, openai: "invalid_request_error", anthropic: "authentication_error", google: "UNAUTHENTICATED"},
		{status: http.StatusForbidden, openai: "invalid_request_error", anthropic: "permission_error", google: "PERMISSION_DENIED"},
		{status: http.StatusNotFound, openai: "invalid_request_error", anthropic: "not_found_error", google: "NOT_FOUND"},
		{status: http.StatusRequestEntityTooLarge, openai: "invalid_request_error", anthropic: "request_too_large", google: "INVALID_ARGUMENT"},
		{status: http.StatusTooManyRequests, openai: "rate_limit_error", anthropic: "rate_limit_error", google: "RESOURCE_EXHAUSTED"},
		{status: http.StatusInternalServerError, openai: "api_error", anthropic: "api_error", google: "INTERNAL"},
		{status: http.StatusBadGateway, openai: "api_error", anthropic: "api_error", google: "UNAVAILABLE"},
		{status: http.StatusServiceUnavailable, openai: "api_error", anthropic: "overloaded_error", google: "UNAVAILABLE"},
		{status: http.StatusGatewayTimeout, openai: "api_error", anthropic: "api_error", google: "DEADLINE_EXCEEDED"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			for _, fc := range []struct {
				family, path, want string
			}{
				{family: "openai", path: "/v1/chat/completions", want: tc.openai},
				{family: "anthropic", path: "/v1/messages", want: tc.anthropic},
				{family: "google", path: "/v1beta/models/gemini-2.5-flash:generateContent", want: tc.google},
			} {
				t.Run(fc.family, func(t *testing.T) {
					payload := decodeProxyErrorBody(t, proxyErrorEnvelope(httptest.NewRequest(http.MethodPost, fc.path, nil), tc.status, "proxy_error", "boom"))
					if got := errorFieldOf(t, payload, fc.family); got != fc.want {
						t.Fatalf("%s error type for status %d = %q, want %q (payload %#v)", fc.family, tc.status, got, fc.want, payload)
					}
				})
			}
		})
	}
}

// TestRequestBodyErrorsCarryTheProtocolEnvelope pins the one failure a client reaches by
// sending too much. The body limit did answer with `http.Error`, so a base64 image over
// the configured limit - the case the limit exists for - returned `text/plain` and every
// SDK failed to parse the response instead of reading the documented cause. The same
// helper answers a body-read failure with 400, which is covered below through a reader
// that fails mid-body.
func TestRequestBodyErrorsCarryTheProtocolEnvelope(t *testing.T) {
	const limit = 4096
	handler, received, _ := newBodyLimitHandler(t, limit)
	oversized := `{"model":"gpt-5","input":"` + strings.Repeat("x", limit*2) + `"}`

	for _, tc := range []struct {
		family, path, wantType string
	}{
		{family: "openai", path: "/v1/chat/completions", wantType: "invalid_request_error"},
		{family: "anthropic", path: "/v1/messages", wantType: "request_too_large"},
		{family: "google", path: "/v1beta/models/gemini-2.5-flash:generateContent", wantType: "INVALID_ARGUMENT"},
	} {
		t.Run(tc.family, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(oversized))
			req.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413 (body %q)", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
				t.Fatalf("Content-Type = %q, want application/json (body %q)", got, rec.Body.String())
			}
			payload := decodeProxyErrorBody(t, rec.Body.Bytes())
			if got := errorFieldOf(t, payload, tc.family); got != tc.wantType {
				t.Fatalf("%s error type = %q, want %q (payload %#v)", tc.family, got, tc.wantType, payload)
			}
			errObj, _ := payload["error"].(map[string]any)
			if errObj["message"] != "request body exceeds the configured limit" {
				t.Fatalf("message = %v, want the limit explanation", errObj["message"])
			}
		})
	}
	if len(*received) != 0 {
		t.Fatalf("an oversized body reached the upstream: %v", *received)
	}

	// Ollama's surface carries the message in a string field, not in the OpenAI object.
	t.Run("ollama", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/show", strings.NewReader(oversized))
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413 (body %q)", rec.Code, rec.Body.String())
		}
		var payload map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("the Ollama error body must hold `error` as a string: %v (%s)", err, rec.Body.String())
		}
		if payload["error"] != "request body exceeds the configured limit" {
			t.Fatalf("error = %q, want the limit explanation", payload["error"])
		}
	})

	// A body that fails mid-read is the 400 branch of the same helper.
	t.Run("unreadable body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", failingReader{})
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body %q)", rec.Code, rec.Body.String())
		}
		payload := decodeProxyErrorBody(t, rec.Body.Bytes())
		if got := payload["type"]; got != "error" {
			t.Fatalf("payload = %#v, want the Anthropic envelope", payload)
		}
		errObj, _ := payload["error"].(map[string]any)
		if errObj["type"] != "invalid_request_error" {
			t.Fatalf("error.type = %v, want invalid_request_error", errObj["type"])
		}
	})
}

// failingReader fails on its first read, so the handler's body read returns a
// non-MaxBytesError error.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// decodeProxyErrorBody parses an error body, insisting it is JSON (the point of the
// envelope) rather than plain text.
func decodeProxyErrorBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("error body is not JSON: %v (%s)", err, body)
	}
	return payload
}

// errorFieldOf reads the family's own type field out of a decoded envelope.
func errorFieldOf(t *testing.T, payload map[string]any, family string) string {
	t.Helper()
	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("%s envelope has no error object: %#v", family, payload)
	}
	field := "type"
	if family == "google" {
		field = "status"
	}
	value, _ := errObj[field].(string)
	return value
}

// TestProxyErrorsNeverFallBackToPlainText is a structural gate over the package.
//
// Every error the proxy produces must carry the protocol-shaped envelope: the official
// SDKs parse only their own error shape, and `http.Error` writes `text/plain`, which turns
// a readable failure into a client-side parse error. That single mistake has been the root
// cause of three separate defects here - the 401 entrypoint, the response to an oversized
// request body, and the locally answered count_tokens and model endpoints - so the
// invariant is asserted over the source rather than one call site at a time, and a new
// call site cannot quietly reintroduce it.
func TestProxyErrorsNeverFallBackToPlainText(t *testing.T) {
	for _, dir := range []string{".", "../responses/httpapi"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			source, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read %s: %v", filepath.Join(dir, name), err)
			}
			for i, line := range strings.Split(string(source), "\n") {
				if !strings.Contains(line, "http.Error(") {
					continue
				}
				t.Errorf("%s:%d answers with http.Error, which writes text/plain; use writeProxyError so the client can parse the failure:\n\t%s",
					filepath.Join(dir, name), i+1, strings.TrimSpace(line))
			}
		}
	}
}

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
		name     string
		path     string
		body     string
		wantType string
	}{
		{name: "openai", path: "/v1/chat/completions", body: `"error"`, wantType: "invalid_request_error"},
		{name: "anthropic", path: "/v1/messages", body: `"type"`, wantType: "authentication_error"},
		{name: "google", path: "/v1beta/models/gemini:generateContent", body: `"status"`, wantType: "UNAUTHENTICATED"},
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
			payload := decodeProxyErrorBody(t, rec.Body.Bytes())
			if got := errorFieldOf(t, payload, tc.name); got != tc.wantType {
				t.Fatalf("%s error type = %q, want %q (payload %#v)", tc.name, got, tc.wantType, payload)
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

// TestProxyErrorEnvelopeFamilyFollowsTheSharedClassification pins that the
// envelope is a consumer of the shared path classification rather than a second
// implementation of it.
//
// The proxy used to decide the family with its own path predicates, so the
// envelope and the rest of the proxy could disagree about the same request. For
// `/v1beta/models/<model>:countTokens` the local predicate said Google while
// `llm.ClassifyPath` said `unknown`, and it was the local one that was right by
// accident; every path a Google-family provider is classified for must produce a
// Google-shaped envelope.
func TestProxyErrorEnvelopeFamilyFollowsTheSharedClassification(t *testing.T) {
	googleFamily := []string{
		"/v1beta/models/gemini-2.0-flash:generateContent",
		"/v1beta/models/gemini-2.0-flash:streamGenerateContent",
		"/v1beta/models/gemini-2.0-flash:countTokens",
		"/v1beta/models/text-embedding-004:embedContent",
		"/v1/publishers/google/models/gemini-2.0-flash:countTokens",
		"/v1/models/gemini-2.0-flash:countTokens",
	}
	for _, path := range googleFamily {
		t.Run(path, func(t *testing.T) {
			provider := llm.ClassifyPath(path, "").Provider
			if provider != llm.ProviderGoogleGenAI && provider != llm.ProviderVertexNative {
				t.Fatalf("ClassifyPath(%q).Provider = %q, want a Google family", path, provider)
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, nil)
			writeProxyError(rec, req, http.StatusBadGateway, "upstream_error", "boom")

			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("body is not JSON: %v (%s)", err, rec.Body.String())
			}
			assertGoogleEnvelope(t, payload)
		})
	}

	t.Run("anthropic family", func(t *testing.T) {
		path := "/v1/messages"
		if got := llm.ClassifyPath(path, "").Provider; got != llm.ProviderAnthropic {
			t.Fatalf("ClassifyPath(%q).Provider = %q, want %q", path, got, llm.ProviderAnthropic)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		writeProxyError(rec, req, http.StatusBadGateway, "upstream_error", "boom")

		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("body is not JSON: %v (%s)", err, rec.Body.String())
		}
		if payload["type"] != "error" {
			t.Fatalf("type = %v, want the Anthropic envelope", payload["type"])
		}
	})
}
