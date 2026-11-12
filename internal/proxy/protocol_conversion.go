package proxy

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/gateway"
	"github.com/kingfs/Trajecta/internal/recorder"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/upstream"
	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/protocolconvert"
)

// protocolConversionRequestError marks a deterministic, client-caused
// conversion failure (unsupported content, a lossy field, an invalid model
// name). The proxy answers it with a 4xx instead of the 502 it uses for an
// upstream failure, and never counts it against the target's health.
type protocolConversionRequestError struct {
	err error
}

func (e *protocolConversionRequestError) Error() string { return e.err.Error() }
func (e *protocolConversionRequestError) Unwrap() error { return e.err }

func conversionRequestError(format string, args ...any) error {
	return &protocolConversionRequestError{err: fmt.Errorf(format, args...)}
}

// protocolConversion is the per-request conversion context: the client
// protocol, the upstream protocol the selected target was chosen for, and
// the wire model name. It is non-nil only when conversion is enabled in
// config and Plan decided to convert for this selection.
type protocolConversion struct {
	clientProtocol   protocolconvert.Protocol
	upstreamProtocol protocolconvert.Protocol
	wireModel        string
	vertex           bool
	// engine selects pkg/protocolconvert (legacy) or pkg/xir/convert (xir).
	engine           string
	requestWarnings  []protocolconvert.Warning
	responseWarnings []protocolconvert.Warning
}

// UpstreamProtocol names the wire protocol of the body ConvertRequest produces.
// The gateway uses it so protocol-aware masking parses the converted bytes with
// the target codec rather than the client's.
func (c *protocolConversion) UpstreamProtocol() string {
	if c == nil {
		return ""
	}
	return string(c.upstreamProtocol)
}

// conversionForSelection decides whether this selection needs conversion.
// It returns nil for the overwhelmingly common cases: conversion disabled,
// a non-convertible endpoint, or native passthrough. Correctness rules:
// streaming requests never convert (PlanForRequest rejects them), a Responses
// entrypoint is owned by the local runtime, and a decision that is not convert
// leaves every byte untouched.
func (h *Handler) conversionForSelection(r *http.Request, selection *router.Selection, upstreamBodyBytes []byte) (*protocolConversion, error) {
	if h == nil || h.cfg == nil || !h.cfg.Router.Selection.ProtocolConversion {
		return nil, nil
	}
	if selection == nil || selection.Target == nil || r == nil || r.URL == nil {
		return nil, nil
	}
	client, err := protocolconvert.DetectFromPath(r.URL.Path)
	if err != nil {
		// Endpoints without conversion vocabulary (models, embeddings,
		// tokenize) are never converted.
		return nil, nil
	}
	// /v1/responses is answered by the local Responses runtime, which owns
	// Codex compatibility, hosted tools and its native-vs-local policy. Running
	// a second, different translation in the forwarding path would bypass all
	// of it, so that entrypoint never converts; a chat-only upstream is reached
	// through the runtime's own Chat Completions orchestration.
	if client == protocolconvert.ProtocolResponses {
		return nil, nil
	}
	caps := selection.Target.Upstream.ProtocolCapabilitiesForModel(selection.Request.ModelName)
	decision, err := protocolconvert.PlanForRequest(protocolconvert.PlanRequest{
		Client: client,
		Caps:   caps,
		Mode:   protocolconvert.ModePassthroughPreferred,
		Stream: selection.Request.Stream,
	})
	if err != nil {
		return nil, nil
	}
	if !decision.Convert() {
		return nil, nil
	}
	// The checks below only apply when bytes must be translated; a native
	// passthrough forwards a compressed body or an SSE query verbatim.
	//
	// A compressed request body cannot be JSON-parsed for conversion.
	// Passthrough forwards it verbatim and works; conversion must fail
	// explicitly instead of trying to parse gzip bytes as JSON.
	if enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); enc != "" && enc != "identity" {
		return nil, conversionRequestError("protocol conversion: compressed request body (Content-Encoding: %s) cannot be converted; use a native passthrough target", enc)
	}
	// A Gemini client can also ask for SSE via ?alt=sse on the
	// generateContent surface. The router only sees URL.Path, so it cannot
	// tell this from a non-streaming request. A converting target cannot
	// deliver SSE, so reject; a native target never reaches this branch.
	if q := r.URL.Query(); strings.EqualFold(q.Get("alt"), "sse") {
		return nil, conversionRequestError("protocol conversion: Gemini SSE (?alt=sse) cannot be converted; use a native passthrough target")
	}
	wireModel := llm.ModelFromBody(upstreamBodyBytes)
	if wireModel == "" {
		wireModel = selection.Request.ModelName
	}
	wireModel = strings.TrimSpace(wireModel)
	if wireModel == "" {
		return nil, conversionRequestError("protocol conversion: no model name in the request body; the target protocol needs one")
	}
	// Re-check Plan with the wire model actually used. The router checked caps
	// with the requested model name; alias rewriting may have changed it, and
	// the per-model supports_* overrides are keyed by model. If the wire model
	// is not served by any convertible protocol, do not convert — fall back to
	// native (which will fail with the upstream's own error) rather than
	// sending a body the target cannot parse.
	if wireModel != selection.Request.ModelName {
		if caps2 := selection.Target.Upstream.ProtocolCapabilitiesForModel(wireModel); !caps2.Has(decision.UpstreamProtocol) {
			// Sending the client-protocol bytes instead would reach an upstream
			// that does not serve that surface, which is the silent corruption
			// this conversion path exists to avoid.
			return nil, conversionRequestError("protocol conversion: upstream model %q is not served over %s by the selected channel; the alias mapping makes the converted request invalid", wireModel, decision.UpstreamProtocol)
		}
	}
	return &protocolConversion{
		clientProtocol:   decision.ClientProtocol,
		upstreamProtocol: decision.UpstreamProtocol,
		wireModel:        wireModel,
		vertex:           isVertexTarget(selection.Target),
		engine:           h.conversionEngine,
	}, nil
}

// ConvertRequest implements gateway.Converter.
func (c *protocolConversion) ConvertRequest(body []byte) ([]byte, []protocolconvert.Warning, error) {
	converted, err := c.convertRequestBody(body)
	return converted, append([]protocolconvert.Warning(nil), c.requestWarnings...), err
}

// UpstreamPath implements gateway.Converter.
func (c *protocolConversion) UpstreamPath() (string, error) { return c.upstreamPath() }

var _ gateway.Converter = (*protocolConversion)(nil)

func isVertexTarget(target *router.Target) bool {
	if target == nil {
		return false
	}
	switch target.Upstream.RoutingProfile {
	case upstream.RoutingProfileVertexExpress, upstream.RoutingProfileVertexProject:
		return true
	}
	return false
}

// protocolConversionEvent records the conversion in the cassette so a human
// reading a trace can see that the upstream bytes differ from the client
// bytes and why (including what the IR could not carry across).
func protocolConversionEvent(c *protocolConversion, start time.Time) recorder.RecordEvent {
	fields := make([]string, 0, len(c.requestWarnings)+len(c.responseWarnings))
	for _, warning := range c.requestWarnings {
		fields = append(fields, warning.Field)
	}
	for _, warning := range c.responseWarnings {
		fields = append(fields, "response:"+warning.Field)
	}
	return recorder.RecordEvent{
		Type: "protocol_conversion",
		Time: start,
		Attributes: map[string]interface{}{
			"client_protocol":   string(c.clientProtocol),
			"upstream_protocol": string(c.upstreamProtocol),
			"wire_model":        c.wireModel,
			"warning_fields":    fields,
		},
	}
}

// responseConversionEvent records the response-side warnings (the request
// event is created before the upstream call, when response warnings are
// still unknown).
func responseConversionEvent(c *protocolConversion, start time.Time) recorder.RecordEvent {
	fields := make([]string, 0, len(c.responseWarnings))
	for _, warning := range c.responseWarnings {
		fields = append(fields, warning.Field)
	}
	return recorder.RecordEvent{
		Type: "protocol_conversion_response",
		Time: start,
		Attributes: map[string]interface{}{
			"client_protocol":   string(c.clientProtocol),
			"upstream_protocol": string(c.upstreamProtocol),
			"warning_fields":    fields,
		},
	}
}

// convertRequestBody converts the wire request body. The caller passes the
// client-visible body; the result is what goes upstream. A warning whose loss
// changes the answer is a refusal, not a silent degradation.
func (c *protocolConversion) convertRequestBody(body []byte) ([]byte, error) {
	if c.engine == "xir" {
		converted, warnings, err := c.convertRequestBodyXIR(body)
		if err != nil {
			return nil, err
		}
		c.requestWarnings = warnings
		return converted, nil
	}
	result, err := protocolconvert.ConvertRequest(body, c.clientProtocol, c.upstreamProtocol, c.wireModel, c.vertex)
	if err != nil {
		return nil, &protocolConversionRequestError{err: err}
	}
	if fatal := result.FatalWarnings(); len(fatal) > 0 {
		return nil, &protocolConversionRequestError{err: fmt.Errorf("protocol conversion refused: %s", describeFatalWarnings(fatal))}
	}
	c.requestWarnings = result.Warnings
	return result.Body, nil
}

func describeFatalWarnings(warnings []protocolconvert.Warning) string {
	parts := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		parts = append(parts, warning.Field)
	}
	return fmt.Sprintf("the request carries %s, which the conversion IR cannot carry faithfully; use a native passthrough target", strings.Join(parts, ", "))
}

// upstreamPath is the path the converted request must be sent to. For Gemini
// the model travels in the path; the client query string is deliberately not
// carried over, because query options (beta flags, alt=sse) belong to the
// client protocol's surface.
func (c *protocolConversion) upstreamPath() (string, error) {
	path, err := c.upstreamProtocol.RequestPath(c.wireModel, false, c.vertex)
	if err != nil {
		return "", &protocolConversionRequestError{err: err}
	}
	return path, nil
}

// applyConvertedHeaders fixes protocol-scoped headers after a conversion.
// Authorization is handled separately by ApplyAuthHeaders (target profile),
// but version/beta/key headers belong to the client protocol's surface and
// must not leak, nor be missing, on the new surface.

// convertResponseBody converts a successful upstream response back to the
// client protocol. Error responses are never converted: their bodies are
// provider-specific diagnostics, and routing them through the IR would
// replace a useful upstream error with an empty shell in the client
// protocol. They reach the client byte for byte, as before conversion
// existed. An empty successful body (e.g., 204) also passes through: there
// is nothing to translate, and failing the candidate over it would be a
// spurious 502. Conversion buffers the whole body, so it is capped at
// maxBytes (the same ceiling the request path uses) instead of growing
// without bound.
func (c *protocolConversion) convertResponseBody(resp *http.Response, maxBytes int64) (*http.Response, error) {
	converted, _, err := (gateway.ResponseTransform{Converter: c, MaxBytes: maxBytes}).Apply(resp)
	if err != nil {
		return nil, err
	}
	return converted, nil
}

// ConvertResponse implements gateway.ResponseConverter. The engine choice and
// loss ledger stay here; the response envelope/buffering lives in gateway.
func (c *protocolConversion) ConvertResponse(body []byte) ([]byte, []protocolconvert.Warning, error) {
	if c.engine == "xir" {
		converted, warnings, err := c.convertResponseBodyXIR(body)
		c.responseWarnings = warnings
		return converted, warnings, err
	}
	result, err := protocolconvert.ConvertResponse(body, c.upstreamProtocol, c.clientProtocol, c.wireModel, c.vertex)
	if err != nil {
		return nil, nil, fmt.Errorf("protocol conversion: convert %s response to %s: %w", c.upstreamProtocol, c.clientProtocol, err)
	}
	c.responseWarnings = result.Warnings
	return result.Body, result.Warnings, nil
}

var _ gateway.ResponseConverter = (*protocolConversion)(nil)
