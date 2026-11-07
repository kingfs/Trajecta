// Package modelprobe answers one narrow question about a channel model: does it
// really work over the protocol the gateway would use for it?
//
// A provider probe only inspects the channel's routes, and several providers
// answer a route probe with a generic 400 even when the route is not really
// served for a given model (NVIDIA NIM answers plain-text `404 page not found`
// for `/v1/responses` while listing the model under `/v1/models`). This package
// sends the smallest protocol-appropriate request that still exercises the
// model, classifies the answer and turns the result into suggestions an
// operator can apply by hand - it never writes to the store, never records a
// cassette and never touches `channel_models.last_probe_at`.
package modelprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/redaction"
	"github.com/kingfs/Trajecta/internal/upstream"
)

// Protocol is one wire protocol the gateway can use to reach a model.
type Protocol string

const (
	ProtocolAuto            Protocol = "auto"
	ProtocolChatCompletions Protocol = "chat_completions"
	ProtocolResponses       Protocol = "responses"
	ProtocolMessages        Protocol = "messages"
	ProtocolGenerateContent Protocol = "generate_content"
)

// protocolOrder is the fixed order the auto mode probes declared protocols in.
// Messages and Chat Completions come first because they are the entry points
// the forwarding path uses most; Responses is last among the OpenAI-compatible
// surfaces so a broken Responses route cannot hide a working Chat one.
var protocolOrder = []Protocol{
	ProtocolMessages,
	ProtocolChatCompletions,
	ProtocolResponses,
	ProtocolGenerateContent,
}

// ParseProtocol normalizes a client-supplied protocol name. The empty string is
// "auto", which is also what the HTTP API's default sends.
func ParseProtocol(value string) (Protocol, error) {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
	switch normalized {
	case "", string(ProtocolAuto):
		return ProtocolAuto, nil
	case string(ProtocolChatCompletions):
		return ProtocolChatCompletions, nil
	case string(ProtocolResponses):
		return ProtocolResponses, nil
	case string(ProtocolMessages):
		return ProtocolMessages, nil
	case string(ProtocolGenerateContent):
		return ProtocolGenerateContent, nil
	default:
		return "", fmt.Errorf("invalid protocol %q", strings.TrimSpace(value))
	}
}

// Result statuses. Only a status other than StatusSkipped and
// StatusUnsupportedProtocol means an HTTP call was actually made.
const (
	StatusOK                   = "ok"
	StatusInvalidResponse      = "invalid_response"
	StatusAuthFailed           = "auth_failed"
	StatusModelNotFound        = "model_not_found"
	StatusEndpointNotSupported = "endpoint_not_supported"
	StatusInvalidRequest       = "invalid_request"
	StatusRateLimited          = "rate_limited"
	StatusTimeout              = "timeout"
	StatusNetworkError         = "network_error"
	StatusUpstream5xx          = "upstream_5xx"
	StatusSkipped              = "skipped"
	StatusUnsupportedProtocol  = "unsupported_protocol"
)

// Report statuses describe the whole probe, not one protocol result.
const (
	ReportStatusOK          = "ok"
	ReportStatusPartial     = "partial"
	ReportStatusFailed      = "failed"
	ReportStatusUnsupported = "unsupported"
)

// Target is everything the probe needs to reach one model. The API key lives
// inside Resolved, which is never JSON-serialized.
type Target struct {
	ChannelID       string                    `json:"channel_id"`
	Model           string                    `json:"model"`
	WireModel       string                    `json:"wire_model"`
	ProtocolFamily  string                    `json:"protocol_family"`
	ChannelEnabled  bool                      `json:"channel_enabled"`
	ModelEnabled    bool                      `json:"model_enabled"`
	ModelState      string                    `json:"model_state"`
	Routable        bool                      `json:"routable"`
	Declared        map[Protocol]bool         `json:"declared"`
	NativeResponses bool                      `json:"native_responses"`
	Resolved        upstream.ResolvedUpstream `json:"-"`
}

// Result is one protocol attempt. It is appended even when the attempt was
// intentionally not made, so the UI can explain what was skipped and why.
type Result struct {
	Protocol   Protocol `json:"protocol"`
	Declared   bool     `json:"declared"`
	Method     string   `json:"method"`
	Path       string   `json:"path"`
	URL        string   `json:"url"`
	Status     string   `json:"status"`
	HTTPStatus int      `json:"http_status"`
	LatencyMS  int64    `json:"latency_ms"`
	Error      string   `json:"error"`
	Hint       string   `json:"hint"`
}

// Suggestion is an operator-facing hint derived from the results. Nothing is
// applied automatically.
type Suggestion struct {
	Kind       string `json:"kind"`
	Capability string `json:"capability"`
	Scope      string `json:"scope"`
	Message    string `json:"message"`
}

// Report is the HTTP API contract: target identity, declared capabilities, the
// per-protocol results and any suggestions/warnings.
type Report struct {
	ChannelID       string            `json:"channel_id"`
	Model           string            `json:"model"`
	WireModel       string            `json:"wire_model"`
	ProtocolFamily  string            `json:"protocol_family"`
	ChannelEnabled  bool              `json:"channel_enabled"`
	ModelEnabled    bool              `json:"model_enabled"`
	ModelState      string            `json:"model_state"`
	Routable        bool              `json:"routable"`
	Declared        map[Protocol]bool `json:"declared"`
	NativeResponses bool              `json:"native_responses"`
	Status          string            `json:"status"`
	StartedAt       time.Time         `json:"started_at"`
	CompletedAt     time.Time         `json:"completed_at"`
	DurationMS      int64             `json:"duration_ms"`
	Results         []Result          `json:"results"`
	Suggestions     []Suggestion      `json:"suggestions"`
	Warnings        []string          `json:"warnings"`
}

// Run probes target with protocol and returns a fully classified report.
//
// `protocol` is ProtocolAuto to probe every declared protocol the upstream can
// structurally serve, or one concrete protocol to probe only that surface. The
// function never returns an error: logical failures are result statuses, and a
// request that could not even be built is classified too.
func Run(ctx context.Context, target Target, protocol Protocol, client *http.Client) Report {
	started := time.Now().UTC()
	report := Report{
		ChannelID:       target.ChannelID,
		Model:           target.Model,
		WireModel:       target.WireModel,
		ProtocolFamily:  target.ProtocolFamily,
		ChannelEnabled:  target.ChannelEnabled,
		ModelEnabled:    target.ModelEnabled,
		ModelState:      target.ModelState,
		Routable:        target.Routable,
		Declared:        copyDeclared(target.Declared),
		NativeResponses: target.NativeResponses,
		Status:          ReportStatusUnsupported,
		StartedAt:       started,
		Results:         []Result{},
		Suggestions:     []Suggestion{},
		Warnings:        []string{},
	}
	if client == nil {
		client = defaultHTTPClient()
	}
	// The channel's own proxy policy wins over the system policy for every
	// protocol probe. A disabled policy returns the client unchanged.
	client = target.Resolved.HTTPClient(client)
	selected, deferred := selectProtocols(&report, target, protocol)
	for _, candidate := range selected {
		result := runOne(ctx, target, candidate, client)
		if protocol == ProtocolAuto && candidate == ProtocolResponses && !target.NativeResponses {
			// Declared-at-any-level Responses support that is not native pass-through:
			// the route is still worth probing (that is how the NVIDIA NIM incident is
			// found), but the answer has to say the gateway will not use it natively.
			nativeHint := "declared Responses is not used natively; the gateway will answer /v1/responses through the local runtime unless the model is marked supports_responses=true"
			if result.Hint == "" {
				result.Hint = nativeHint
			} else {
				result.Hint = nativeHint + "; " + result.Hint
			}
			report.Warnings = append(report.Warnings, nativeHint)
		}
		report.Results = append(report.Results, result)
	}
	report.Results = append(report.Results, deferred...)
	report.CompletedAt = time.Now().UTC()
	report.DurationMS = report.CompletedAt.Sub(report.StartedAt).Milliseconds()
	report.Status = overallStatus(report.Results)
	report.Suggestions = buildSuggestions(report)
	return report
}

// RunForced probes each protocol in protocols regardless of Target.Declared, as
// long as the protocol belongs to the target's protocol family. It exists for
// interface detection: the current declaration is exactly what must not
// constrain the probe, because it may be wrong or unset.
//
// ProtocolAuto entries are ignored, duplicates are collapsed and the order is
// kept. A protocol the target's family cannot serve becomes an
// unsupported_protocol result with no HTTP call, exactly as in Run.
func RunForced(ctx context.Context, target Target, protocols []Protocol, client *http.Client) Report {
	started := time.Now().UTC()
	report := Report{
		ChannelID:       target.ChannelID,
		Model:           target.Model,
		WireModel:       target.WireModel,
		ProtocolFamily:  target.ProtocolFamily,
		ChannelEnabled:  target.ChannelEnabled,
		ModelEnabled:    target.ModelEnabled,
		ModelState:      target.ModelState,
		Routable:        target.Routable,
		Declared:        copyDeclared(target.Declared),
		NativeResponses: target.NativeResponses,
		Status:          ReportStatusUnsupported,
		StartedAt:       started,
		Results:         []Result{},
		Suggestions:     []Suggestion{},
		Warnings:        []string{},
	}
	if client == nil {
		client = defaultHTTPClient()
	}
	client = target.Resolved.HTTPClient(client)
	seen := make(map[Protocol]bool, len(protocols))
	for _, protocol := range protocols {
		if protocol == ProtocolAuto || seen[protocol] {
			continue
		}
		seen[protocol] = true
		if !structurallyServable(protocol, target.ProtocolFamily) {
			report.Results = append(report.Results, Result{
				Protocol: protocol,
				Declared: target.Declared[protocol],
				Method:   http.MethodPost,
				Path:     probePath(protocol, target),
				Status:   StatusUnsupportedProtocol,
				Hint:     fmt.Sprintf("protocol %s is not part of protocol family %s", protocol, displayFamily(target.ProtocolFamily)),
			})
			continue
		}
		report.Results = append(report.Results, runOne(ctx, target, protocol, client))
	}
	report.CompletedAt = time.Now().UTC()
	report.DurationMS = report.CompletedAt.Sub(report.StartedAt).Milliseconds()
	report.Status = overallStatus(report.Results)
	report.Suggestions = buildSuggestions(report)
	return report
}

func defaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		// A redirect from a model endpoint is never what the gateway will see;
		// report the 3xx instead of following it and losing the classification.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func copyDeclared(in map[Protocol]bool) map[Protocol]bool {
	out := map[Protocol]bool{
		ProtocolChatCompletions: false,
		ProtocolResponses:       false,
		ProtocolMessages:        false,
		ProtocolGenerateContent: false,
	}
	for protocol, declared := range in {
		out[protocol] = declared
	}
	return out
}

// selectProtocols decides which protocols to actually run. It returns the
// protocols to execute and the not-run results (skipped or structurally
// unsupported), which are appended after the real attempts.
func selectProtocols(report *Report, target Target, protocol Protocol) ([]Protocol, []Result) {
	if protocol == ProtocolAuto {
		var selected []Protocol
		var deferred []Result
		for _, candidate := range protocolOrder {
			if !target.Declared[candidate] {
				continue
			}
			if !structurallyServable(candidate, target.ProtocolFamily) {
				continue
			}
			selected = append(selected, candidate)
		}
		if target.Declared[ProtocolChatCompletions] && !target.Declared[ProtocolResponses] {
			report.Warnings = append(report.Warnings, "/v1/responses is not declared; the local Responses runtime will translate /v1/responses requests into /v1/chat/completions")
		}
		return selected, deferred
	}

	if !structurallyServable(protocol, target.ProtocolFamily) {
		return nil, []Result{{
			Protocol: protocol,
			Declared: target.Declared[protocol],
			Method:   http.MethodPost,
			Path:     probePath(protocol, target),
			Status:   StatusUnsupportedProtocol,
			Hint:     fmt.Sprintf("protocol %s is not part of protocol family %s", protocol, displayFamily(target.ProtocolFamily)),
		}}
	}
	if !target.Declared[protocol] {
		return nil, []Result{skippedResult(protocol, false, probePath(protocol, target), "the channel and model do not declare this protocol")}
	}
	return []Protocol{protocol}, nil
}

func skippedResult(protocol Protocol, declared bool, path, hint string) Result {
	return Result{
		Protocol: protocol,
		Declared: declared,
		Method:   http.MethodPost,
		Path:     path,
		Status:   StatusSkipped,
		Hint:     hint,
	}
}

func structurallyServable(protocol Protocol, family string) bool {
	family = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(family)), "-", "_")
	switch protocol {
	case ProtocolMessages:
		return family == upstream.ProtocolFamilyAnthropicMessages
	case ProtocolGenerateContent:
		return family == upstream.ProtocolFamilyGoogleGenAI || family == upstream.ProtocolFamilyVertexNative
	case ProtocolChatCompletions, ProtocolResponses:
		return family == "" || family == upstream.ProtocolFamilyOpenAICompatible
	default:
		return false
	}
}

func displayFamily(family string) string {
	if strings.TrimSpace(family) == "" {
		return upstream.ProtocolFamilyOpenAICompatible
	}
	return family
}

func probePath(protocol Protocol, target Target) string {
	if protocol == ProtocolGenerateContent {
		return generateContentPath(target.ProtocolFamily, target.WireModel)
	}
	return probePathForDeclared(protocol)
}

func probePathForDeclared(protocol Protocol) string {
	switch protocol {
	case ProtocolChatCompletions:
		return "/v1/chat/completions"
	case ProtocolResponses:
		return "/v1/responses"
	case ProtocolMessages:
		return "/v1/messages"
	default:
		return ""
	}
}

func generateContentPath(family, wireModel string) string {
	escaped := url.PathEscape(wireModel)
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(family)), "-", "_") {
	case upstream.ProtocolFamilyVertexNative:
		return "/v1/publishers/models/" + escaped + ":generateContent"
	default:
		return "/v1beta/models/" + escaped + ":generateContent"
	}
}

func runOne(ctx context.Context, target Target, protocol Protocol, client *http.Client) Result {
	started := time.Now()
	method := http.MethodPost
	path := probePath(protocol, target)
	result := Result{
		Protocol: protocol,
		Declared: target.Declared[protocol],
		Method:   method,
		Path:     path,
	}

	body, err := requestBody(protocol, target.WireModel)
	if err != nil {
		result.Status = StatusInvalidRequest
		result.Error = redactSnippet(err.Error(), target.Resolved.APIKey)
		result.Hint = hintFor(StatusInvalidRequest)
		result.LatencyMS = time.Since(started).Milliseconds()
		return result
	}
	targetURL, err := target.Resolved.BuildURL(path)
	if err != nil {
		result.Status = StatusInvalidRequest
		result.Error = redactSnippet(err.Error(), target.Resolved.APIKey)
		result.Hint = hintFor(StatusInvalidRequest)
		result.LatencyMS = time.Since(started).Milliseconds()
		return result
	}
	result.URL = targetURL

	req, err := http.NewRequestWithContext(ctx, method, targetURL, bytes.NewReader(body))
	if err != nil {
		result.Status = StatusInvalidRequest
		result.Error = redactSnippet(err.Error(), target.Resolved.APIKey)
		result.Hint = hintFor(StatusInvalidRequest)
		result.LatencyMS = time.Since(started).Milliseconds()
		return result
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	target.Resolved.ApplyAuthHeaders(req.Header)

	resp, err := client.Do(req)
	if err != nil {
		result.Status = classifyTransportError(ctx, err)
		result.Error = redactSnippet(err.Error(), target.Resolved.APIKey)
		result.Hint = hintFor(result.Status)
		result.LatencyMS = time.Since(started).Milliseconds()
		return result
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))

	result.HTTPStatus = resp.StatusCode
	status, snippet := classify(protocol, resp.StatusCode, resp.Header.Get("Content-Type"), string(raw), target.Resolved.APIKey)
	result.Status = status
	if status == StatusOK {
		result.Error = ""
	} else {
		result.Error = redactSnippet(snippet, target.Resolved.APIKey)
	}
	result.Hint = hintFor(result.Status)
	result.LatencyMS = time.Since(started).Milliseconds()
	return result
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func requestBody(protocol Protocol, wireModel string) ([]byte, error) {
	switch protocol {
	case ProtocolChatCompletions:
		return json.Marshal(map[string]any{
			"model":      wireModel,
			"messages":   []chatMessage{{Role: "user", Content: "ping"}},
			"max_tokens": 16,
			"stream":     false,
		})
	case ProtocolResponses:
		return json.Marshal(map[string]any{
			"model":             wireModel,
			"input":             "ping",
			"max_output_tokens": 16,
			"store":             false,
			"stream":            false,
		})
	case ProtocolMessages:
		return json.Marshal(map[string]any{
			"model":      wireModel,
			"max_tokens": 16,
			"messages":   []chatMessage{{Role: "user", Content: "ping"}},
		})
	case ProtocolGenerateContent:
		return json.Marshal(map[string]any{
			"contents": []map[string]any{
				{"parts": []map[string]string{{"text": "ping"}}},
			},
			"generationConfig": map[string]any{"maxOutputTokens": 16},
		})
	default:
		return nil, fmt.Errorf("unsupported protocol %q", protocol)
	}
}

var modelNotFoundKeywords = []string{
	"model",
	"does not exist",
	"not found",
	"not_found",
	"unknown",
	"no such",
	"not supported",
}

// bodyBlamesModel reports whether a 400/422 body clearly blames the model rather
// than the request shape. The "model" token is required, so a generic provider
// complaint such as "unknown field messages" stays invalid_request.
func bodyBlamesModel(lowerBody string) bool {
	if !strings.Contains(lowerBody, "model") {
		return false
	}
	for _, needle := range []string{
		"not supported",
		"unsupported",
		"unknown",
		"not found",
		"not_found",
		"does not exist",
		"not exist",
		"no such",
		"invalid model",
		"model is invalid",
	} {
		if strings.Contains(lowerBody, needle) {
			return true
		}
	}
	return false
}

var authFailedKeywords = []string{
	"api key",
	"api_key",
	"api-key",
	"apikey",
	"invalid key",
	"incorrect api key",
	"authentication",
	"unauthorized",
	"authorization",
	"access token",
	"invalid token",
	"credential",
}

func classify(protocol Protocol, statusCode int, contentType, body, apiKey string) (string, string) {
	trimmed := strings.TrimSpace(body)
	lower := strings.ToLower(trimmed)
	switch {
	case statusCode >= 200 && statusCode < 300:
		return classifySuccess(protocol, trimmed)
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return StatusAuthFailed, trimmed
	case statusCode == http.StatusBadRequest || statusCode == http.StatusUnprocessableEntity:
		if bodyMentionsSecret(trimmed, apiKey) || containsAny(lower, authFailedKeywords...) {
			return StatusAuthFailed, trimmed
		}
		// Some providers answer an unknown or unsupported model with 400/422 instead
		// of 404. Detection has to read that as "the model does not work here" rather
		// than as a malformed probe request.
		if code, _ := errorCode(trimmed); isModelNotFoundCode(code) {
			return StatusModelNotFound, trimmed
		}
		if bodyBlamesModel(lower) {
			return StatusModelNotFound, trimmed
		}
		return StatusInvalidRequest, trimmed
	case statusCode == http.StatusNotFound:
		return classifyNotFound(contentType, trimmed), trimmed
	case statusCode == http.StatusMethodNotAllowed || statusCode == http.StatusNotImplemented:
		return StatusEndpointNotSupported, trimmed
	case statusCode == http.StatusTooManyRequests:
		return StatusRateLimited, trimmed
	case statusCode >= 300 && statusCode < 400:
		return StatusInvalidResponse, trimmed
	case statusCode >= 500:
		return StatusUpstream5xx, trimmed
	default:
		return StatusInvalidRequest, trimmed
	}
}

func classifySuccess(protocol Protocol, body string) (string, string) {
	if strings.TrimSpace(body) == "" {
		return StatusInvalidResponse, ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return StatusInvalidResponse, body
	}
	if rawError, ok := payload["error"]; ok && rawError != nil {
		return StatusInvalidResponse, body
	}
	switch protocol {
	case ProtocolChatCompletions:
		if _, ok := payload["choices"].([]any); ok {
			return StatusOK, ""
		}
		if object, ok := payload["object"].(string); ok && object == "chat.completion" {
			return StatusOK, ""
		}
	case ProtocolResponses:
		if _, ok := payload["output"].([]any); ok {
			return StatusOK, ""
		}
		if _, ok := payload["id"].(string); ok {
			return StatusOK, ""
		}
		if object, ok := payload["object"].(string); ok && object == "response" {
			return StatusOK, ""
		}
	case ProtocolMessages:
		if _, ok := payload["content"]; ok {
			return StatusOK, ""
		}
	case ProtocolGenerateContent:
		if _, ok := payload["candidates"].([]any); ok {
			return StatusOK, ""
		}
	}
	return StatusInvalidResponse, body
}

func classifyNotFound(contentType, body string) string {
	lower := strings.ToLower(body)
	if code, _ := errorCode(body); isModelNotFoundCode(code) {
		return StatusModelNotFound
	}
	if lower == "" || strings.Contains(lower, "page not found") {
		return StatusEndpointNotSupported
	}
	// A plain-text or HTML 404 is a router answer, not a model error. NVIDIA
	// NIM answers exactly `404 page not found` for an endpoint it does not
	// serve, which is the case this whole probe exists for.
	if isPlainOrHTML(contentType, body) {
		return StatusEndpointNotSupported
	}
	if containsAny(lower, modelNotFoundKeywords...) {
		return StatusModelNotFound
	}
	return StatusEndpointNotSupported
}

func isModelNotFoundCode(code string) bool {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(code)), "-", "_")
	return normalized == "model_not_found" || normalized == "model_not_exist" || normalized == "modelnotfound"
}

// errorCode pulls the error code out of an OpenAI-style error envelope.
func errorCode(body string) (code string, message string) {
	var payload struct {
		Error struct {
			Code    any    `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return "", ""
	}
	if payload.Error.Code != nil {
		code = fmt.Sprint(payload.Error.Code)
	}
	if payload.Error.Message != "" {
		message = payload.Error.Message
	} else {
		message = payload.Error.Type
	}
	return code, message
}

func isPlainOrHTML(contentType, body string) bool {
	mime := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch {
	case strings.HasPrefix(mime, "text/plain"):
		return true
	case strings.HasPrefix(mime, "text/html"):
		return true
	case strings.HasPrefix(mime, "application/xhtml"):
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(body))
	return strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html") || strings.HasPrefix(lower, "<")
}

func bodyMentionsSecret(body, apiKey string) bool {
	apiKey = strings.TrimSpace(apiKey)
	return len(apiKey) >= 4 && strings.Contains(body, apiKey)
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func classifyTransportError(ctx context.Context, err error) string {
	if err == nil {
		return StatusNetworkError
	}
	if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return StatusTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return StatusTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return StatusTimeout
	}
	// DNS failures, connection refused and TLS handshake failures all land here:
	// the request never reached a model endpoint.
	return StatusNetworkError
}

func hintFor(status string) string {
	switch status {
	case StatusOK:
		return ""
	case StatusInvalidResponse:
		return "the upstream answered 2xx but the body is not the JSON shape this protocol returns"
	case StatusAuthFailed:
		return "verify the channel API key and auth headers"
	case StatusModelNotFound:
		return "the upstream does not know this model; re-sync the model list or fix the model's upstream_model"
	case StatusEndpointNotSupported:
		return "the upstream does not serve this path for this model; the gateway must use a different protocol"
	case StatusInvalidRequest:
		return "the upstream rejected the probe request as malformed"
	case StatusRateLimited:
		return "the upstream is rate limiting; retry later"
	case StatusTimeout:
		return "the probe exceeded the request timeout"
	case StatusNetworkError:
		return "the upstream could not be reached (DNS, connection refused or TLS)"
	case StatusUpstream5xx:
		return "the upstream returned a server error"
	case StatusSkipped:
		return "the protocol was not probed"
	case StatusUnsupportedProtocol:
		return "the protocol does not belong to this upstream's protocol family"
	default:
		return ""
	}
}

func overallStatus(results []Result) string {
	tried := 0
	succeeded := 0
	for _, result := range results {
		if result.Status == StatusSkipped || result.Status == StatusUnsupportedProtocol {
			continue
		}
		tried++
		if result.Status == StatusOK {
			succeeded++
		}
	}
	switch {
	case tried == 0:
		return ReportStatusUnsupported
	case succeeded == tried:
		return ReportStatusOK
	case succeeded > 0:
		return ReportStatusPartial
	default:
		return ReportStatusFailed
	}
}

func buildSuggestions(report Report) []Suggestion {
	suggestions := []Suggestion{}
	byProtocol := map[Protocol]string{}
	tried := make([]Result, 0, len(report.Results))
	for _, result := range report.Results {
		byProtocol[result.Protocol] = result.Status
		if result.Status == StatusSkipped || result.Status == StatusUnsupportedProtocol {
			continue
		}
		tried = append(tried, result)
	}
	if byProtocol[ProtocolChatCompletions] == StatusOK &&
		byProtocol[ProtocolResponses] == StatusEndpointNotSupported &&
		report.Declared[ProtocolResponses] {
		suggestions = append(suggestions, Suggestion{
			Kind:       "disable_capability",
			Capability: "responses",
			Scope:      "model",
			Message:    "chat_completions works but /v1/responses is not served for this model; clear its Responses support so the gateway uses the local Responses runtime",
		})
	}
	if len(tried) > 0 && allStatus(tried, StatusEndpointNotSupported) {
		suggestions = append(suggestions, Suggestion{
			Kind:    "check_channel_config",
			Scope:   "channel",
			Message: "none of the probed protocol endpoints is served for this model; verify the channel base_url and api_type",
		})
	}
	if len(tried) > 0 && allStatus(tried, StatusModelNotFound) {
		suggestions = append(suggestions, Suggestion{
			Kind:    "resync_models",
			Scope:   "channel",
			Message: "the upstream does not list this model on any probed protocol; re-sync the channel model list or correct the model's upstream_model",
		})
	}
	if anyStatus(tried, StatusAuthFailed) {
		suggestions = append(suggestions, Suggestion{
			Kind:    "verify_credentials",
			Scope:   "channel",
			Message: "the upstream rejected the channel credential; verify the API key and auth headers",
		})
	}
	return suggestions
}

func allStatus(results []Result, status string) bool {
	for _, result := range results {
		if result.Status != status {
			return false
		}
	}
	return true
}

func anyStatus(results []Result, status string) bool {
	for _, result := range results {
		if result.Status == status {
			return true
		}
	}
	return false
}

// redactSnippet collapses a diagnostic snippet to one line, strips anything
// that looks like a credential (including the channel's own key) and bounds it.
func redactSnippet(raw, apiKey string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	text := redaction.MetadataText(raw)
	if key := strings.TrimSpace(apiKey); len(key) >= 4 {
		text = strings.ReplaceAll(text, key, "REDACTED")
	}
	text = strings.Join(strings.Fields(text), " ")
	const maxRunes = 200
	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return text
}
